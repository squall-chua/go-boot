package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

// TestWriteProblem pins the RFC 7807 shape: a panic and a hand-written 400
// come out looking the same, which is the whole point of one writer.
func TestWriteProblem(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	web.WriteProblem(rec, http.StatusBadRequest, "name is required")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := map[string]any{
		"type":   "about:blank",
		"title":  "Bad Request",
		"status": float64(400),
		"detail": "name is required",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	web.WriteJSON(rec, http.StatusCreated, map[string]int{"id": 7})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"id":7}` {
		t.Fatalf("body = %q, want %q", got, `{"id":7}`)
	}
}

type person struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func postBody(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// TestDecodeJSONAccepts is the happy path.
func TestDecodeJSONAccepts(t *testing.T) {
	t.Parallel()
	var p person
	if err := web.DecodeJSON(postBody(`{"name":"ada","age":36}`), &p); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if p != (person{Name: "ada", Age: 36}) {
		t.Fatalf("decoded %+v", p)
	}
}

// TestDecodeJSONRejects pins that every failure comes back as an error whose
// message is safe to hand straight to WriteProblem as the detail.
func TestDecodeJSONRejects(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body string
		want string // a substring the caller can show the client
	}{
		"empty body":     {``, "empty"},
		"malformed":      {`{"name":`, "malformed"},
		"wrong type":     {`{"name":"ada","age":"old"}`, "age"},
		"unknown field":  {`{"name":"ada","nickname":"a"}`, "nickname"},
		"trailing value": {`{"name":"ada"}{"name":"bob"}`, "single"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var p person
			err := web.DecodeJSON(postBody(tc.body), &p)
			if err == nil {
				t.Fatalf("DecodeJSON(%q) = nil, want an error", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestDecodeJSONCapsTheBody pins the default 1 MiB cap, and that the error is
// one a caller can act on: it is an *http.MaxBytesError, so a handler can
// answer 413 rather than 400.
func TestDecodeJSONCapsTheBody(t *testing.T) {
	t.Parallel()
	huge := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	var p person
	err := web.DecodeJSON(postBody(huge), &p)
	if err == nil {
		t.Fatal("a 2 MiB body decoded, want the 1 MiB cap to fire")
	}
	var tooBig *http.MaxBytesError
	if !errors.As(err, &tooBig) {
		t.Fatalf("error %v is not an *http.MaxBytesError, so a handler cannot answer 413", err)
	}
}

// TestDecodeJSONHonoursTheConfiguredCap pins that web.maxBodyBytes in config
// actually reaches DecodeJSON, rather than the default silently winning.
func TestDecodeJSONHonoursTheConfiguredCap(t *testing.T) {
	t.Parallel()
	sink := newLogSink()
	srv := web.New(web.Config{Addr: "127.0.0.1:0", MaxBodyBytes: 32}, sink.logr)
	srv.HandleFunc("POST /p", func(w http.ResponseWriter, r *http.Request) {
		var p person
		if err := web.DecodeJSON(r, &p); err != nil {
			web.WriteProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		web.WriteJSON(w, http.StatusOK, p)
	})
	url := startServer(t, srv)

	if code := post(t, url+"/p", `{"name":"ada"}`); code != http.StatusOK {
		t.Fatalf("a 14-byte body under a 32-byte cap gave %d, want 200", code)
	}
	if code := post(t, url+"/p", `{"name":"`+strings.Repeat("a", 64)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("a 74-byte body over a 32-byte cap gave %d, want the cap to fire", code)
	}
}

// startServer runs srv inside an App and returns its base URL.
func startServer(t *testing.T, srv *web.Server) string {
	t.Helper()
	app, err := goboot.New(goboot.Config{Log: goboot.LogConfig{Level: "ERROR"}, Lifecycle: quick})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.Add(srv)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return "http://" + srv.Addr()
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
