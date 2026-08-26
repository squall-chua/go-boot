package security_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/security"
)

type appConfig struct {
	Security security.Config `yaml:"security"`
}

func bind(t *testing.T, yaml string) (appConfig, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg appConfig
	return cfg, goboot.Load(nil, p, "ORDERS_", &cfg)
}

// TestTheConfigKeysBind is here because docs/spec.md 12 counts config keys and
// their defaults as public surface, exactly as much as an exported identifier.
// Nothing else in this package loads a YAML file, so without this the yaml
// tags were only ever read by eye — and the first thing that check caught was
// a README sample written as "180d", which Go durations have no unit for.
func TestTheConfigKeysBind(t *testing.T) {
	cfg, err := bind(t, `
security:
  headers:
    hstsMaxAge: 4320h
  cors:
    allowedOrigins: [https://app.example.com]
  jwt:
    issuer: https://auth.example.com/
    audience: orders-api
    jwksUrl: https://auth.example.com/.well-known/jwks.json
`)
	if err != nil {
		t.Fatalf("the documented config does not bind: %v", err)
	}
	got := cfg.Security
	if got.Headers.HSTSMaxAge != 4320*time.Hour {
		t.Errorf("hstsMaxAge = %s", got.Headers.HSTSMaxAge)
	}
	if len(got.CORS.AllowedOrigins) != 1 || got.CORS.AllowedOrigins[0] != "https://app.example.com" {
		t.Errorf("allowedOrigins = %v", got.CORS.AllowedOrigins)
	}
	if got.JWT.Issuer == "" || len(got.JWT.Audience) != 1 || got.JWT.JWKSURL == "" {
		t.Errorf("jwt = %+v", got.JWT)
	}
	// The whole section must survive as something Authenticate accepts, or
	// the keys bind and the service still will not start.
	if _, err := security.DefaultMiddleware(got); err != nil {
		t.Fatalf("the documented config binds but is rejected: %v", err)
	}
}

// TestTheConfigKeysBindFromTheEnvironment: a JWKS URL and an origin list are
// exactly the values an operator overrides per environment, so the relaxed
// key matching of docs/spec.md 3 is checked on them rather than assumed.
func TestTheConfigKeysBindFromTheEnvironment(t *testing.T) {
	t.Setenv("ORDERS_SECURITY__JWT__JWKSURL", "https://env.example.com/jwks")
	t.Setenv("ORDERS_SECURITY__CORS__ALLOWEDORIGINS", "https://a.test,https://b.test")
	cfg, err := bind(t, `
security:
  jwt:
    issuer: https://auth.example.com/
    audience: orders-api
    jwksUrl: https://auth.example.com/.well-known/jwks.json
`)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got := cfg.Security.JWT.JWKSURL; got != "https://env.example.com/jwks" {
		t.Errorf("jwksUrl = %q; the environment layer did not win", got)
	}
	if got := cfg.Security.CORS.AllowedOrigins; len(got) != 2 {
		t.Errorf("allowedOrigins = %v, want the comma list split into two", got)
	}
}
