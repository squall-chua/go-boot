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
	"testing"
	"time"
)

// TestTimeoutDefaults pins docs/spec.md 4.3: the header and idle timeouts are
// on, and the write timeout is OFF because gRPC streams share this server.
// It reads the http.Server directly, which is why it is an internal test:
// there is no way to observe a write deadline that is absent.
func TestTimeoutDefaults(t *testing.T) {
	t.Parallel()
	s := New(Config{}, slog.Default())
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
	s := New(cfg, slog.New(slog.DiscardHandler))
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
// Start, not by the goroutine a moment later. A misspelt key path must not
// leave the service quietly serving plain HTTP, and must not surface as a
// bare "open : no such file" on the death channel.
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
			s := New(cfg, slog.New(slog.DiscardHandler))
			deathc, err := s.Start(t.Context())
			if err == nil {
				_ = s.Stop(context.Background())
				t.Fatal("Start accepted a half-finished TLS config")
			}
			if deathc != nil {
				t.Fatal("Start returned a death channel alongside its error")
			}
		})
	}
}
