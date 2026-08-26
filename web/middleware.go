package web

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/squall-chua/go-boot"
)

// maxRequestIDLen caps an inbound X-Request-Id. The header is
// attacker-controlled and lands in every log line for the request, so it is
// checked rather than trusted. A value that fails is replaced, not truncated:
// truncating still lets the caller choose the first 64 characters.
const maxRequestIDLen = 64

// requestIDHeader is both what is read on the way in and what is written back
// on the way out, so a caller can correlate without parsing a body.
const requestIDHeader = "X-Request-Id"

// rpcStatusHeader is where gRPC and gRPC-Web put the code that the HTTP
// status line does not carry. Named here rather than imported: goboot/web
// links no connect-go, and this is a wire constant, not an API.
const rpcStatusHeader = "Grpc-Status"

// DefaultMiddleware is a slice you can edit, not hidden behaviour. Print it,
// drop an entry, or splice one in:
//
//	srv.Use(web.DefaultMiddleware(app.Log)...)
//
// The order is RequestID, Logging, Recovery, outermost first. Recovery sits
// INSIDE Logging on purpose: the 500 it writes passes back out through the
// logging wrapper, so a panic is recorded as a 500 rather than as whatever
// the handler never got round to writing.
func DefaultMiddleware(log *slog.Logger) []Middleware {
	return []Middleware{RequestID, Logging(log), Recovery(log)}
}

// RequestID puts a request ID on the response, generating one from
// crypto/rand when the caller sent none and when the one they sent fails the
// length or character-set check. It takes no logger because the request-
// scoped logger is attached by Logging, which has one.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !saneRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

// saneRequestID accepts a non-empty run of unreserved URI characters within
// the length cap. Anything else — a newline that would forge a second log
// line, a terminal escape, a 4 KB string — is rejected outright.
func saneRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// newRequestID is 16 bytes of crypto/rand as hex. crypto/rand.Read cannot
// fail on any supported platform — it panics inside the runtime instead — so
// there is no error to handle here.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Logging writes one line per request, on the way out. It also attaches the
// request-scoped logger, because it is the middleware that holds the App's
// logger: a handler calling goboot.LoggerFrom gets that logger already
// tagged with the request ID, so its lines join up with this one.
//
// 5xx goes to ERROR and everything else to INFO, so a server error is
// findable by level alone.
func Logging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// RequestID runs outside this, so the header is already set.
			// Tagged once and used for both the handler's own lines and the
			// access line below, so the two cannot drift apart.
			reqLog := log.With("requestId", w.Header().Get(requestIDHeader))
			r = r.WithContext(goboot.WithLogger(r.Context(), reqLog))

			if isProbePath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			// r.Pattern is only filled in once ServeMux has routed, so it is
			// read here rather than above. path is what was asked for; route
			// is the low-cardinality label to group by.
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"route", r.Pattern,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration", time.Since(start),
			}
			// A failed RPC left 200 on the status line, so without this the
			// line above reads as a success. ERROR is the level the gRPC
			// Starter's own "rpc failed" line uses, so one requestId finds
			// both at the same level.
			if code, failed := rpcStatus(rec.Header()); failed {
				level = slog.LevelError
				attrs = append(attrs, "rpcCode", code)
			}
			reqLog.Log(r.Context(), level, "request", attrs...)
		})
	}
}

// rpcStatus reads the gRPC status code the handler left on the response, and
// reports whether it is a failing one. Absent counts as success, so an
// ordinary HTTP response never gains an rpcCode.
//
// Two header keys, because connect writes the status in two places. A real
// HTTP trailer that was never announced reaches net/http as a header key with
// the "Trailer:" prefix, which is what a plain gRPC handler writes. A
// trailers-only response — nothing in the body yet — writes the same key as an
// ordinary header instead, and so does connect's ErrorWriter, which announces
// its trailers up front. Both are read, so neither shape is missed.
//
// What IS missed is a failure connect writes into the response BODY, where no
// HTTP middleware can see it: a gRPC-Web call that fails after its first
// message, and a Connect-protocol stream. docs/spec.md 9 records both.
//
// The code is passed through as the string on the wire rather than parsed.
// goboot/web may not import connect-go, so the name behind the number is not
// available here, and a copy of connect's table would only drift.
func rpcStatus(h http.Header) (code string, failed bool) {
	code = h.Get(http.TrailerPrefix + rpcStatusHeader)
	if code == "" {
		code = h.Get(rpcStatusHeader)
	}
	return code, code != "" && code != "0"
}

// isProbePath names the three paths that are never logged. Kubernetes hits
// /livez and /readyz every ten seconds, which is roughly 17,000 log lines a
// day saying nothing. These are hardcoded, not a config key: the paths belong
// to go-boot.
func isProbePath(p string) bool {
	return p == "/livez" || p == "/readyz" || strings.HasPrefix(p, "/actuator/")
}

// Recovery turns a panicking handler into a 500 RFC 7807 document. Without
// it, a panic on a bare net/http server gives the client EOF — no response at
// all — so the caller sees a network fault rather than a bug in the service.
func Recovery(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Logging already wraps w in a recorder, so in the default set
			// this reuses that one rather than adding a second layer.
			rec, ok := w.(*recorder)
			if !ok {
				rec = &recorder{ResponseWriter: w, status: http.StatusOK}
				w = rec
			}
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				// ErrAbortHandler is a handler saying "drop this connection
				// quietly". Swallowing it would turn a deliberate abort into
				// a 500, so it goes back to net/http untouched.
				if v == http.ErrAbortHandler {
					panic(v)
				}
				// The logger Recovery was handed, not the one in the
				// context: drop Logging from the slice and the context
				// still has one, but it is slog.Default(), not the App's.
				// The request ID comes off the header for the same reason.
				log.With("requestId", w.Header().Get(requestIDHeader)).Error("panic",
					"err", v, "method", r.Method, "path", r.URL.Path, "route", r.Pattern)
				// A response already on the wire cannot be taken back:
				// its status line is gone and a problem document appended
				// here would only corrupt the body. Log it and let the
				// connection close.
				if rec.wrote {
					return
				}
				// The panic value never reaches the client: it routinely
				// carries paths, queries and keys.
				WriteProblem(w, http.StatusInternalServerError, "internal error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// recorder remembers the status and byte count Logging reports. Unwrap and
// Flush keep it transparent to http.ResponseController and to streaming
// handlers, which matters because gRPC shares this server (ADR 0006).
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (r *recorder) WriteHeader(status int) {
	// A 1xx is informational, not an answer: net/http sends it straight out
	// and the handler still owes the client a real status. The condition is
	// net/http's own, 101 and all — a Switching Protocols response IS final,
	// and server.go takes it down the ordinary path. Recording a 1xx costs
	// twice, both measured in #47: the recorder swallows the status the
	// handler meant, so the client gets an implicit 200, and the status this
	// remembers is one nobody received. Leaving wrote false is right too —
	// nothing final is on the wire, so Recovery may still write its 500.
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(status)
		return
	}
	if r.wrote {
		return // net/http already logs the superfluous call; do not double-count
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush marks the response written: flushing puts the status line on the
// wire, so nothing can be taken back after it.
func (r *recorder) Flush() {
	r.wrote = true
	//nolint:errcheck // a ResponseWriter that cannot flush is not an error here
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}
