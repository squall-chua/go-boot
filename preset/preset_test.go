package preset_test

import (
	"testing"
	"testing/fstest"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/preset"
)

// serviceConfig is the shape a Preset user writes: the Preset's Config
// embedded inline, with the service's own key beside it. This is why
// goboot.Load stays in main — a Preset that loaded config for you could never
// see this key.
type serviceConfig struct {
	preset.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

// TestAServiceKeyLoadsBesideThePresetConfig proves the Preset costs a service
// nothing in config: every go-boot section binds, and so does a key go-boot
// has never heard of.
func TestAServiceKeyLoadsBesideThePresetConfig(t *testing.T) {
	defaults := fstest.MapFS{"app.yaml": {Data: []byte(
		"log:\n  level: WARN\nweb:\n  addr: \":9999\"\ndb:\n  maxOpenConns: 3\nactuator:\n  expose: [livez]\ngreeting: hi\n",
	)}}
	t.Setenv("ORDERS_DB__DSN", "postgres://from-the-environment")

	var cfg serviceConfig
	if err := goboot.Load(defaults, "app.yaml", "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Greeting != "hi" {
		t.Errorf("greeting = %q; the service's own key did not bind", cfg.Greeting)
	}
	if cfg.Log.Level != "WARN" {
		t.Errorf("log.level = %q; the inline section became one of its own", cfg.Log.Level)
	}
	if cfg.Web.Addr != ":9999" {
		t.Errorf("web.addr = %q", cfg.Web.Addr)
	}
	if cfg.DB.MaxOpenConns != 3 {
		t.Errorf("db.maxOpenConns = %d", cfg.DB.MaxOpenConns)
	}
	if cfg.DB.DSN != "postgres://from-the-environment" {
		t.Errorf("db.dsn = %q; the environment layer did not reach the Preset's section", cfg.DB.DSN)
	}
	if len(cfg.Actuator.Expose) != 1 || cfg.Actuator.Expose[0] != "livez" {
		t.Errorf("actuator.expose = %v", cfg.Actuator.Expose)
	}
}
