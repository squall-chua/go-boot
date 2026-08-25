package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// defaultMaxBodyBytes is the cap DecodeJSON applies when the request did not
// come through a go-boot Server — a unit test, or a handler mounted on a
// plain net/http mux.
const defaultMaxBodyBytes = 1 << 20 // 1 MiB

// maxBodyKey carries web.maxBodyBytes from the Server's config down to
// DecodeJSON, which is a package-level function and so cannot read the
// Server's fields.
type maxBodyKey struct{}

// withMaxBody records the cap. It does NOT apply it to r.Body: only
// DecodeJSON reads a whole body, and a blanket cap here would cut off a gRPC
// stream sharing this listener (ADR 0006).
func withMaxBody(ctx context.Context, n int64) context.Context {
	return context.WithValue(ctx, maxBodyKey{}, n)
}

func maxBodyFrom(ctx context.Context) int64 {
	if n, ok := ctx.Value(maxBodyKey{}).(int64); ok && n > 0 {
		return n
	}
	return defaultMaxBodyBytes
}

// problem is an RFC 7807 document. A struct and a content type, no
// dependency. type is always about:blank, which RFC 7807 defines as "this is
// an ordinary HTTP status and nothing more"; instance is left out until a
// caller has something useful to put in it.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// WriteProblem writes an error as an RFC 7807 document. Recovery uses it too,
// so a panic and a hand-written 400 come out in the same shape.
//
// It is a function a handler calls, not a handler signature: handlers stay
// http.HandlerFunc (ADR 0004).
//
// detail is text YOU wrote for this caller. WriteProblem(w, 400, err.Error())
// is the HTTP half of the leak docs/spec.md 4.0 names: it hands the caller
// whatever text came up from below, which on a bad day is a driver saying
// which host and user the password failed for.
func WriteProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}

// WriteJSON writes v as the response body.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads the request body into out. It is not a wrapper around
// json.NewDecoder(r.Body).Decode: that one line is not the correct one.
//
// This one caps the body at web.maxBodyBytes, so a 4 GB POST cannot exhaust
// memory; rejects unknown fields, so a typo in a field name is an error
// rather than a silently ignored value; refuses a second JSON value after the
// first; and turns the decoder's error into a message a handler can hand
// straight to WriteProblem.
//
// A body over the cap comes back as an *http.MaxBytesError, so a handler that
// wants to answer 413 rather than 400 can tell the difference with errors.As.
func DecodeJSON(r *http.Request, out any) error {
	// Assigned back to r.Body, not kept in a local: net/http type-switches on
	// r.Body when the response is written, and closes the connection instead
	// of draining the rest of an oversized upload only if it finds the
	// limited reader there.
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyFrom(r.Context()))
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(out); err != nil {
		return decodeError(err)
	}
	// A second value would otherwise be dropped without a word, so a client
	// sending two objects would think both were accepted.
	if dec.More() {
		return errors.New("body must hold a single JSON value")
	}
	return nil
}

// decodeError renames the decoder's errors into something a handler can show
// a client. encoding/json reports an unknown field only as a string, so that
// one is matched on its prefix.
func decodeError(err error) error {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var tooBig *http.MaxBytesError

	switch {
	case errors.As(err, &tooBig):
		// Wrapped, not replaced, so errors.As still finds it.
		return fmt.Errorf("body is larger than the %d byte limit: %w", tooBig.Limit, err)
	case errors.As(err, &syntax):
		return fmt.Errorf("body is malformed JSON at byte %d", syntax.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("body is malformed JSON: it ends part way through a value")
	case errors.Is(err, io.EOF):
		return errors.New("body is empty")
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return fmt.Errorf("field %q must be a %s", typeErr.Field, typeErr.Type)
		}
		return fmt.Errorf("body must be a %s", typeErr.Type)
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return fmt.Errorf("unknown field %s", name)
	}
	return err
}
