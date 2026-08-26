package web_test

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

// logSink collects one map per log record, so a test can assert on fields
// rather than on the shape of a formatted line.
type logSink struct {
	mu   sync.Mutex
	buf  strings.Builder
	logr *slog.Logger
}

func newLogSink() *logSink {
	s := &logSink{}
	s.logr = slog.New(slog.NewJSONHandler(s, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return s
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for line := range strings.Lines(s.buf.String()) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// requests keeps only the access-log lines, which is what most of these
// tests are about.
func (s *logSink) requests(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.records(t) {
		if r["msg"] == "request" {
			out = append(out, r)
		}
	}
	return out
}

// serve mounts pattern on a real test server wrapped in mw, outermost first,
// and returns its base URL. The error log is discarded so a deliberate panic
// does not dump a stack trace over the test output.
func serve(t *testing.T, mw []web.Middleware, pattern string, h http.HandlerFunc) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	var wrapped http.Handler = mux
	for i := len(mw) - 1; i >= 0; i-- {
		wrapped = mw[i](wrapped)
	}
	ts := httptest.NewUnstartedServer(wrapped)
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL
}

func get(t *testing.T, url string, header ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestDefaultMiddlewareIsAnEditableValue pins docs/spec.md 4.3: the default
// set is a slice the developer can print and edit, not hidden behaviour, and
// its order is RequestID, Logging, Recovery.
func TestDefaultMiddlewareIsAnEditableValue(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	mw := web.DefaultMiddleware(sink.logr)
	if len(mw) != 3 {
		t.Fatalf("DefaultMiddleware has %d entries, want 3", len(mw))
	}

	// Editable: drop the recovery entry and the panic reaches net/http.
	url := serve(t, mw[:2], "GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/boom", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("got %d with recovery removed, want no response at all", resp.StatusCode)
	}
}

// TestRecoveryTurnsAPanicIntoAProblem pins that a panicking handler answers
// 500 as an RFC 7807 document instead of dropping the connection.
func TestRecoveryTurnsAPanicIntoAProblem(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})

	resp := get(t, url+"/boom")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var problem map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem["status"] != float64(500) || problem["title"] != "Internal Server Error" {
		t.Fatalf("problem = %v, want status 500 and a title", problem)
	}
	if problem["detail"] == "boom" {
		t.Fatal("the panic value leaked to the client")
	}
}

// TestRecoverySitsInsideLogging pins the reason for that order: the 500
// recovery writes passes back out through Logging, so the access-log line for
// a panic says 500 and is at ERROR.
func TestRecoverySitsInsideLogging(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	get(t, url+"/boom")

	lines := sink.requests(t)
	if len(lines) != 1 {
		t.Fatalf("got %d access-log lines, want 1", len(lines))
	}
	if lines[0]["status"] != float64(500) {
		t.Fatalf("logged status = %v, want 500", lines[0]["status"])
	}
	if lines[0]["level"] != "ERROR" {
		t.Fatalf("logged level = %v, want ERROR for a 5xx", lines[0]["level"])
	}
}

// TestAccessLogLine pins the one line per request and every field on it.
func TestAccessLogLine(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	})
	resp := get(t, url+"/users/7")

	lines := sink.requests(t)
	if len(lines) != 1 {
		t.Fatalf("got %d access-log lines, want exactly 1", len(lines))
	}
	got := lines[0]
	want := map[string]any{
		"level":  "INFO",
		"method": "GET",
		"path":   "/users/7",
		"route":  "GET /users/{id}", // r.Pattern: the low-cardinality label
		"status": float64(200),
		"bytes":  float64(5),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if got["duration"] == nil {
		t.Error("duration is missing")
	}
	if got["requestId"] != resp.Header.Get("X-Request-Id") {
		t.Errorf("requestId = %v, want the one sent back to the client %q",
			got["requestId"], resp.Header.Get("X-Request-Id"))
	}
}

// TestTheAccessLogNamesAFailedRPC pins docs/spec.md 4.3. A gRPC status rides
// in trailers, so the HTTP status line says 200 whether the call worked or
// not, and the access line has to read the trailer to tell the two apart.
//
// Both shapes connect writes are covered, because they are not the same
// header: plain gRPC always writes a real HTTP trailer, which net/http takes
// through the "Trailer:" prefix on the header map, while a gRPC-Web response
// that fails before its first message writes the same key as an ordinary
// header.
func TestTheAccessLogNamesAFailedRPC(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		key   string
		code  string
		want  any // the rpcCode expected on the line, nil for no field at all
		level string
	}{
		{"grpc failed", http.TrailerPrefix + "Grpc-Status", "2", "2", "ERROR"},
		{"grpc ok", http.TrailerPrefix + "Grpc-Status", "0", nil, "INFO"},
		{"grpc-web failed", "Grpc-Status", "5", "5", "ERROR"},
		{"not an RPC at all", "", "", nil, "INFO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := newLogSink()
			url := serve(t, web.DefaultMiddleware(sink.logr), "GET /rpc", func(w http.ResponseWriter, r *http.Request) {
				if tc.key != "" {
					w.Header().Set(tc.key, tc.code)
				}
				_, _ = io.WriteString(w, "x")
			})
			get(t, url+"/rpc")

			lines := sink.requests(t)
			if len(lines) != 1 {
				t.Fatalf("got %d access-log lines, want exactly 1", len(lines))
			}
			got := lines[0]
			// 200 is what went on the wire, so 200 is what the line says.
			// The access log reports the response, it does not translate it.
			if got["status"] != float64(200) {
				t.Errorf("status = %v, want the 200 that actually went on the wire", got["status"])
			}
			if got["rpcCode"] != tc.want {
				t.Errorf("rpcCode = %v, want %v", got["rpcCode"], tc.want)
			}
			if got["level"] != tc.level {
				t.Errorf("level = %v, want %v", got["level"], tc.level)
			}
		})
	}
}

// TestProbePathsAreNotLogged pins the three hardcoded skips. Kubernetes hits
// the first two every ten seconds, which is ~17,000 lines a day saying
// nothing.
func TestProbePathsAreNotLogged(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /", func(http.ResponseWriter, *http.Request) {})

	for _, p := range []string{"/livez", "/readyz", "/actuator/health", "/actuator/metrics"} {
		get(t, url+p)
	}
	if lines := sink.requests(t); len(lines) != 0 {
		t.Fatalf("probe paths produced %d access-log lines, want 0: %v", len(lines), lines)
	}

	// A path that merely starts the same way is still logged.
	get(t, url+"/actuators")
	if lines := sink.requests(t); len(lines) != 1 {
		t.Fatalf("/actuators produced %d lines, want 1", len(lines))
	}
}

// TestRequestIDGeneratedHonouredAndReplaced pins all three arms of the
// inbound header rule. An unbounded attacker-controlled string flowing into
// every log line is a log-injection hole, so a header that is too long or
// carries the wrong characters is thrown away, not truncated.
func TestRequestIDGeneratedHonouredAndReplaced(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	t.Run("generated when absent", func(t *testing.T) {
		resp := get(t, url+"/x")
		id := resp.Header.Get("X-Request-Id")
		if id == "" {
			t.Fatal("no X-Request-Id sent back")
		}
		if len(id) < 16 {
			t.Fatalf("generated id %q is too short to be unguessable", id)
		}
	})

	t.Run("honoured when sane", func(t *testing.T) {
		resp := get(t, url+"/x", "X-Request-Id", "abc-123_XYZ.9")
		if got := resp.Header.Get("X-Request-Id"); got != "abc-123_XYZ.9" {
			t.Fatalf("X-Request-Id = %q, want the inbound value", got)
		}
	})

}

// TestRequestIDReplacesAnUnsafeHeader covers the values an http.Client
// refuses to send at all, so they are driven straight at the middleware. A
// newline is the one that matters: it would forge a second log line.
func TestRequestIDReplacesAnUnsafeHeader(t *testing.T) {
	t.Parallel()
	for name, bad := range map[string]string{
		"too long":       strings.Repeat("a", 65),
		"whitespace":     "id with spaces",
		"newline":        "id\nlevel=ERROR msg=injected",
		"non-ascii":      "id\u00e9",
		"empty":          "",
		"control-escape": "id\x1b[31m",
		"semicolon":      "id;drop",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.Header["X-Request-Id"] = []string{bad}
			rec := httptest.NewRecorder()
			web.RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, r)

			got := rec.Header().Get("X-Request-Id")
			if got == bad {
				t.Fatalf("X-Request-Id %q was honoured, want it replaced", bad)
			}
			if !isHex32(got) {
				t.Fatalf("replacement %q is not a generated id", got)
			}
		})
	}
}

// isHex32 matches what newRequestID produces: 16 random bytes as hex.
func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// TestRequestScopedLoggerCarriesTheID pins that the logger a handler pulls
// out of the context is already tagged, so a Service Layer log line can be
// joined to the access-log line for the same request.
func TestRequestScopedLoggerCarriesTheID(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /x", func(w http.ResponseWriter, r *http.Request) {
		goboot.LoggerFrom(r.Context()).Info("handler ran")
	})
	resp := get(t, url+"/x", "X-Request-Id", "known-id")

	if got := resp.Header.Get("X-Request-Id"); got != "known-id" {
		t.Fatalf("X-Request-Id = %q, want known-id", got)
	}
	for _, rec := range sink.records(t) {
		if rec["msg"] == "handler ran" {
			if rec["requestId"] != "known-id" {
				t.Fatalf("handler log line requestId = %v, want known-id", rec["requestId"])
			}
			return
		}
	}
	t.Fatal("the handler's own log line never reached the App logger")
}

// TestRecoveryUsesItsOwnLogger pins that the logger handed to Recovery is the
// one it writes to. DefaultMiddleware is a slice you can edit, so Logging can
// be dropped — and then there is no request-scoped logger in the context, only
// slog.Default(). Recovery must not quietly fall through to that.
func TestRecoveryUsesItsOwnLogger(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	mw := web.DefaultMiddleware(sink.logr)
	url := serve(t, []web.Middleware{mw[0], mw[2]}, "GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})

	resp := get(t, url+"/boom")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	for _, rec := range sink.records(t) {
		if rec["msg"] == "panic" {
			if rec["requestId"] != resp.Header.Get("X-Request-Id") {
				t.Fatalf("panic line requestId = %v, want %q",
					rec["requestId"], resp.Header.Get("X-Request-Id"))
			}
			return
		}
	}
	t.Fatal("the panic never reached the logger Recovery was given")
}

// TestRecoveryLeavesACommittedResponseAlone pins that a handler which writes
// a 200 and then panics does not get a problem document glued onto its body.
// The status line is already on the wire; nothing can take it back.
func TestRecoveryLeavesACommittedResponseAlone(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /half", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "half a body")
		panic("boom")
	})

	resp := get(t, url+"/half")
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "problem") || strings.Contains(string(body), "about:blank") {
		t.Fatalf("body = %q, want no problem document appended", body)
	}
	if lines := sink.requests(t); len(lines) != 1 || lines[0]["status"] != float64(200) {
		t.Fatalf("access log = %v, want one line with the 200 that was actually sent", lines)
	}
}

// TestAnInformationalStatusIsNotTheFinalOne pins #47. net/http lets a handler
// send a 1xx and then the status it really means — an Early Hints response is
// exactly that shape — so the recorder must let the 1xx through without
// closing over it. Treating it as final costs twice: the 103 is swallowed,
// the real status never reaches the wire, and net/http writes an implicit 200
// that neither the handler nor the access line chose.
func TestAnInformationalStatusIsNotTheFinalOne(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	url := serve(t, web.DefaultMiddleware(sink.logr), "GET /hints", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusNoContent)
	})
	if resp := get(t, url+"/hints"); resp.StatusCode != http.StatusNoContent {
		t.Errorf("GET /hints = %d, want 204: the 1xx was taken for the final status", resp.StatusCode)
	}

	lines := sink.requests(t)
	if len(lines) != 1 {
		t.Fatalf("got %d access-log lines, want exactly 1", len(lines))
	}
	if got := lines[0]["status"]; got != float64(http.StatusNoContent) {
		t.Errorf("access line status = %v, want 204", got)
	}
}
