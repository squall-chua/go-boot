package web

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTimeoutDefaults pins docs/spec.md 4.3: the header and idle timeouts are
// on, and the write timeout is OFF because gRPC streams share this server.
// It reads the http.Server directly, which is why it is an internal test:
// there is no way to observe a write deadline that is absent.
func TestTimeoutDefaults(t *testing.T) {
	t.Parallel()
	s, err := New(Config{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.srv.ReadHeaderTimeout; got != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", got)
	}
	if got := s.srv.IdleTimeout; got != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", got)
	}
	if got := s.srv.WriteTimeout; got != 0 {
		t.Errorf("WriteTimeout = %v, want it off", got)
	}
	if got := s.srv.ReadTimeout; got != 0 {
		t.Errorf("ReadTimeout = %v, want it off for the same reason", got)
	}
	if s.maxBody != defaultMaxBodyBytes {
		t.Errorf("MaxBodyBytes default = %d, want %d", s.maxBody, defaultMaxBodyBytes)
	}
}

// TestTLSIsTwoKeys pins that setting certFile and keyFile is all it takes to
// serve HTTPS, and that nothing else is asked for: no autocert, no host list.
func TestTLSIsTwoKeys(t *testing.T) {
	t.Parallel()
	certFile, keyFile := selfSignedCert(t)

	cfg := Config{Addr: "127.0.0.1:0"}
	cfg.TLS.CertFile, cfg.TLS.KeyFile = certFile, keyFile
	s, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	s.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "secure")
	})

	if _, err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	client := &http.Client{Transport: &http.Transport{
		//nolint:gosec // the certificate is generated in this test
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+s.Addr()+"/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "secure" {
		t.Fatalf("got %d %q, want 200 %q", resp.StatusCode, body, "secure")
	}
	if resp.TLS == nil {
		t.Fatal("the connection was not TLS")
	}
}

// TestHTTP2OverTLSStaysOn is the other half of the Protocols set. Assigning a
// Protocols value replaces Go's default outright — the zero value is an EMPTY
// set — so SetHTTP2(true) is the line that keeps HTTP/2 alive over TLS. Drop
// it while turning cleartext HTTP/2 on and every TLS user silently falls back
// to HTTP/1 with nothing logged anywhere.
func TestHTTP2OverTLSStaysOn(t *testing.T) {
	t.Parallel()
	certFile, keyFile := selfSignedCert(t)

	cfg := Config{Addr: "127.0.0.1:0"}
	cfg.TLS.CertFile, cfg.TLS.KeyFile = certFile, keyFile
	s, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	s.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	})

	if _, err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	// The client has to offer h2 as well: a Transport with its own
	// TLSClientConfig does not attempt HTTP/2 unless it is asked to. Without
	// these two lines the test measures the client, not the server.
	tr := &http.Transport{
		//nolint:gosec // the certificate is generated in this test
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Protocols:       &http.Protocols{},
	}
	tr.Protocols.SetHTTP1(true)
	tr.Protocols.SetHTTP2(true)

	client := &http.Client{Transport: tr}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+s.Addr()+"/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 2 || string(body) != "HTTP/2.0" {
		t.Errorf("served %s and the handler saw %q, want HTTP/2 both ways", resp.Proto, body)
	}
}

// selfSignedCert writes a throwaway certificate and key to t.TempDir and
// returns the two paths.
func selfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "go-boot test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	write(t, certFile, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	write(t, keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certFile, keyFile
}

func write(t *testing.T, path string, block *pem.Block) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestTLSRejectsAHalfConfig pins that one key without the other is refused by
// New, before anything is built and long before the goroutine that would
// surface it as a bare "open : no such file" on the death channel. A misspelt
// key path must not leave the service quietly serving plain HTTP.
//
// New and not Start is the convention of spec 4.0 and ADR 0011: a constructor
// validates its own config, Start reports only what needs the world. The
// message opens with the config key path, so an operator knows which YAML
// line to edit.
func TestTLSRejectsAHalfConfig(t *testing.T) {
	t.Parallel()
	certFile, keyFile := selfSignedCert(t)

	for name, tls := range map[string]struct{ cert, key string }{
		"cert without key": {certFile, ""},
		"key without cert": {"", keyFile},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{Addr: "127.0.0.1:0"}
			cfg.TLS.CertFile, cfg.TLS.KeyFile = tls.cert, tls.key
			s, err := New(cfg, slog.New(slog.DiscardHandler))
			if err == nil {
				_ = s.Stop(context.Background())
				t.Fatal("New accepted a half-finished TLS config")
			}
			if s != nil {
				t.Fatal("New returned a Server alongside its error")
			}
			if !strings.HasPrefix(err.Error(), "web.tls: ") {
				t.Errorf("err = %q, want it to open with the config key path", err)
			}
		})
	}
}
