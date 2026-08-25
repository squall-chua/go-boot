package traced_test

import (
	"testing"
	"testing/fstest"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/preset/traced"
)

// serviceConfig is the tracing twin's shape, and it is two inline embeddings
// deep: the service embeds traced.Config, which embeds preset.Config. Both
// have to flatten to the top level or every go-boot key would need a prefix
// nobody wrote.
type serviceConfig struct {
	traced.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

// TestAServiceKeyLoadsBesideTheTracedConfig is the preset test one embedding
// deeper, plus the trace section the twin adds.
func TestAServiceKeyLoadsBesideTheTracedConfig(t *testing.T) {
	defaults := fstest.MapFS{"app.yaml": {Data: []byte(
		"log:\n  level: WARN\nweb:\n  addr: \":9999\"\ntrace:\n  serviceName: orders\ngreeting: hi\n",
	)}}
	t.Setenv("ORDERS_TRACE__SAMPLERATIO", "0.25")

	var cfg serviceConfig
	if err := goboot.Load(defaults, "app.yaml", "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Greeting != "hi" {
		t.Errorf("greeting = %q; the service's own key did not bind", cfg.Greeting)
	}
	if cfg.Log.Level != "WARN" {
		t.Errorf("log.level = %q; the twice-inlined section became one of its own", cfg.Log.Level)
	}
	if cfg.Web.Addr != ":9999" {
		t.Errorf("web.addr = %q", cfg.Web.Addr)
	}
	if cfg.Trace.ServiceName != "orders" {
		t.Errorf("trace.serviceName = %q", cfg.Trace.ServiceName)
	}
	if cfg.Trace.SampleRatio != 0.25 {
		t.Errorf("trace.sampleRatio = %v; the environment layer did not reach the trace section", cfg.Trace.SampleRatio)
	}
}
