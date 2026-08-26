package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestTheDocumentedWiringWorks drives the file README.md quotes. Building it
// proves the lines compile; this proves they do what the prose says, which is
// the part a reader is actually relying on.
//
// The two claims worth a test are the ones a reader cannot check by eye:
// guarding the Actuator does NOT break the Kubernetes probes, and an
// unguarded route still needs no token.
func TestTheDocumentedWiringWorks(t *testing.T) {
	iss := newIssuer(t)
	base := start(t, iss)

	t.Run("an open route needs no token", func(t *testing.T) {
		if code, _ := do(t, http.MethodGet, base+"/hello/world", ""); code != http.StatusOK {
			t.Fatalf("got %d, want 200", code)
		}
	})

	t.Run("the probes stay open", func(t *testing.T) {
		// The whole reason the Actuator wrapper skips these. Kubernetes
		// carries no bearer token, so a guard here never goes ready.
		for _, p := range []string{"/livez", "/readyz", "/actuator/livez", "/actuator/readyz"} {
			if code, _ := do(t, http.MethodGet, base+p, ""); code != http.StatusOK {
				t.Errorf("%s = %d, want 200 with no token", p, code)
			}
		}
	})

	t.Run("the other actuator endpoints are guarded", func(t *testing.T) {
		for _, p := range []string{"/actuator/loglevel", "/actuator/metrics", "/actuator/info"} {
			if code, _ := do(t, http.MethodGet, base+p, ""); code != http.StatusUnauthorized {
				t.Errorf("%s with no token = %d, want 401", p, code)
			}
			tok := iss.sign(t, "actuator")
			if code, _ := do(t, http.MethodGet, base+p, tok); code != http.StatusOK {
				t.Errorf("%s with the actuator scope = %d, want 200", p, code)
			}
		}
	})

	t.Run("a guarded route needs the scope", func(t *testing.T) {
		if code, _ := do(t, http.MethodPost, base+"/orders", ""); code != http.StatusUnauthorized {
			t.Error("POST /orders with no token was not 401")
		}
		if code, _ := do(t, http.MethodPost, base+"/orders", iss.sign(t, "actuator")); code != http.StatusForbidden {
			t.Error("POST /orders with the wrong scope was not 403")
		}
		if code, _ := do(t, http.MethodPost, base+"/orders", iss.sign(t, "orders:write")); code != http.StatusAccepted {
			t.Error("POST /orders with the right scope was not 202")
		}
	})

	t.Run("the security headers are on every response", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, base+"/hello/world", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q", got)
		}
		if got := resp.Header.Get("Strict-Transport-Security"); got != "max-age=15552000" {
			t.Errorf("HSTS = %q, want the 4320h from app.yaml", got)
		}
	})
}

// issuer is a JWKS endpoint on loopback. It is plain http on purpose: that is
// the one carve-out security.Authenticate makes, and this exercises it.
type issuer struct {
	key *rsa.PrivateKey
	url string
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &issuer{key: k}
	enc := base64.RawURLEncoding.EncodeToString
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, fmt.Sprintf(
			`{"keys":[{"kty":"RSA","use":"sig","kid":"k1","n":%q,"e":%q}]}`,
			enc(k.N.Bytes()), enc(big.NewInt(int64(k.E)).Bytes())))
	}))
	t.Cleanup(srv.Close)
	i.url = srv.URL
	return i
}

func (i *issuer) sign(t *testing.T, scope string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": i.url, "aud": "orders-api", "sub": "user-7",
		"exp": time.Now().Add(time.Hour).Unix(), "scope": scope,
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(t *testing.T, method, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// start runs the real main, pointed at the throwaway issuer.
func start(t *testing.T, iss *issuer) string {
	t.Helper()
	addr := freeAddr(t)
	t.Setenv("ORDERS_WEB__ADDR", addr)
	t.Setenv("ORDERS_LOG__LEVEL", "ERROR")
	t.Setenv("ORDERS_LIFECYCLE__DRAINDELAY", "1ms")
	t.Setenv("ORDERS_SECURITY__JWT__ISSUER", iss.url)
	t.Setenv("ORDERS_SECURITY__JWT__JWKSURL", iss.url+"/jwks")

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the service did not shut down")
		}
	})

	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errc:
			t.Fatalf("the service stopped before it was ready: %v", err)
		default:
		}
		if resp, err := http.Get(base + "/livez"); err == nil {
			resp.Body.Close()
			return base
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the service never became ready")
	return ""
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}
