package goboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// catalogConfig stands in for a section the service owns, sitting beside
// go-boot's own sections in one struct.
type catalogConfig struct {
	Addr              string        `yaml:"addr"`
	Hosts             []string      `yaml:"hosts"`
	Greeting          string        `yaml:"greeting"`
	MaxConns          int           `yaml:"maxConns"`
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"`
	Servers           []server      `yaml:"servers"`
}

type server struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// testConfig is what a service passes to Load: go-boot's sections plus its own.
type testConfig struct {
	Log       LogConfig       `yaml:"log"`
	Lifecycle LifecycleConfig `yaml:"lifecycle"`
	Catalog   catalogConfig   `yaml:"catalog"`
}

// write puts body in dir under name and returns the whole path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// embed builds an embedded-defaults filesystem from name/body pairs.
func embed(pairs ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 0; i < len(pairs); i += 2 {
		fsys[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return fsys
}

// layers has one field per source, so a single Load says which layer won.
type layers struct {
	A string `yaml:"a"` // struct pre-fill, set by nothing else
	B string `yaml:"b"` // embedded base
	C string `yaml:"c"` // embedded profile
	D string `yaml:"d"` // disk base
	E string `yaml:"e"` // disk profile
	F string `yaml:"f"` // environment
}

// TestLoadLayerOrder pins the precedence table: outside beats inside, profile
// beats base, environment beats everything.
func TestLoadLayerOrder(t *testing.T) {
	dir := t.TempDir()
	defaults := embed(
		"app.yaml", "b: embedded-base\nc: embedded-base\nd: embedded-base\ne: embedded-base\nf: embedded-base\n",
		"app-local.yaml", "c: embedded-profile\nd: embedded-profile\ne: embedded-profile\nf: embedded-profile\n",
	)
	path := write(t, dir, "app.yaml", "d: disk-base\ne: disk-base\nf: disk-base\n")
	write(t, dir, "app-local.yaml", "e: disk-profile\nf: disk-profile\n")
	t.Setenv("ORDERS_PROFILE", "local")
	t.Setenv("ORDERS_F", "env")

	cfg := layers{A: "prefill"}
	if err := Load(defaults, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := layers{
		A: "prefill",
		B: "embedded-base",
		C: "embedded-profile",
		D: "disk-base",
		E: "disk-profile",
		F: "env",
	}
	if cfg != want {
		t.Fatalf("layers = %+v, want %+v", cfg, want)
	}
}

// TestLayersMergeSectionByKey pins that a later layer overrides the keys it
// names and leaves the rest of the section alone.
func TestLayersMergeSectionByKey(t *testing.T) {
	dir := t.TempDir()
	defaults := embed("app.yaml", "catalog:\n  addr: embedded\n  greeting: kept\n")
	path := write(t, dir, "app.yaml", "catalog:\n  addr: disk\n  maxConns: 3\n")
	write(t, dir, "app-local.yaml", "catalog:\n  addr: profile\n")
	t.Setenv("ORDERS_PROFILE", "local")

	var cfg testConfig
	if err := Load(defaults, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Addr != "profile" {
		t.Fatalf("addr = %q, want profile", cfg.Catalog.Addr)
	}
	if cfg.Catalog.Greeting != "kept" {
		t.Fatalf("greeting = %q; the profile layer wiped the embedded section", cfg.Catalog.Greeting)
	}
	if cfg.Catalog.MaxConns != 3 {
		t.Fatalf("maxConns = %d; the profile layer wiped the disk section", cfg.Catalog.MaxConns)
	}
}

// TestRelaxedKeyMatching pins ADR 0002's public promise: camelCase,
// kebab-case and screaming snake case are one key.
func TestRelaxedKeyMatching(t *testing.T) {
	for _, spelling := range []string{"readHeaderTimeout", "read-header-timeout", "READ_HEADER_TIMEOUT"} {
		t.Run(spelling, func(t *testing.T) {
			dir := t.TempDir()
			path := write(t, dir, "app.yaml", "catalog:\n  "+spelling+": 12s\n")
			var cfg testConfig
			if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Catalog.ReadHeaderTimeout != 12*time.Second {
				t.Fatalf("%s bound to %v, want 12s", spelling, cfg.Catalog.ReadHeaderTimeout)
			}
		})
	}
}

// TestCommaSplitsOnlyForSlices pins the other half of ADR 0002: a comma makes
// a list only when the field is a list, so a string field keeps its commas.
func TestCommaSplitsOnlyForSlices(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  hosts: a,b,c\n  greeting: hello, world\n")
	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strings.Join(cfg.Catalog.Hosts, "|"); got != "a|b|c" {
		t.Fatalf("hosts = %q, want three entries", got)
	}
	if cfg.Catalog.Greeting != "hello, world" {
		t.Fatalf("greeting = %q, want its comma kept", cfg.Catalog.Greeting)
	}
}

// TestSliceIsReplacedWholesale pins that a later layer replaces a list rather
// than merging it element by element.
func TestSliceIsReplacedWholesale(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  hosts: [a, b, c]\n")
	write(t, dir, "app-local.yaml", "catalog:\n  hosts: [d]\n")
	t.Setenv("ORDERS_PROFILE", "local")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strings.Join(cfg.Catalog.Hosts, "|"); got != "d" {
		t.Fatalf("hosts = %q, want the whole list replaced", got)
	}
}

// TestEnvNesting pins that a double underscore splits sections while a single
// one is part of a name.
func TestEnvNesting(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  addr: from-file\n")
	t.Setenv("ORDERS_CATALOG__MAX_CONNS", "7")
	t.Setenv("ORDERS_CATALOG__ADDR", ":8080")
	t.Setenv("ORDERS_LOG__LEVEL", "WARN")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.MaxConns != 7 {
		t.Fatalf("maxConns = %d, want 7 from a single-underscore name", cfg.Catalog.MaxConns)
	}
	if cfg.Catalog.Addr != ":8080" {
		t.Fatalf("addr = %q, want :8080", cfg.Catalog.Addr)
	}
	if cfg.Log.Level != "WARN" {
		t.Fatalf("log.level = %q, want WARN", cfg.Log.Level)
	}
}

// TestUnknownKeyIsAnError pins that a typo fails at startup and the error
// names the path to it.
func TestUnknownKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "lifecycle:\n  stoptimeuot: 1s\n")
	var cfg testConfig
	err := Load(nil, path, "ORDERS_", &cfg)
	if err == nil {
		t.Fatal("Load accepted a mistyped key")
	}
	for _, want := range []string{"lifecycle", "stoptimeuot"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}

// TestProfileIsOptional pins that a missing profile file is fine.
func TestProfileIsOptional(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  addr: base\n")
	t.Setenv("ORDERS_PROFILE", "nosuchprofile")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Addr != "base" {
		t.Fatalf("addr = %q, want base", cfg.Catalog.Addr)
	}
}

// TestProfileIsReserved pins that PROFILE selects the overlay and never
// reaches the config struct as a key of its own.
func TestProfileIsReserved(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  addr: base\n")
	write(t, dir, "app-local.yaml", "catalog:\n  addr: local\n")
	t.Setenv("ORDERS_PROFILE", "local")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v (profile leaked into the struct as an unknown key)", err)
	}
	if cfg.Catalog.Addr != "local" {
		t.Fatalf("addr = %q, want local", cfg.Catalog.Addr)
	}
}

// TestEmptyPrefixIsAnError pins that go-boot never claims the whole
// environment: with no prefix, PATH and HOME become unknown config keys.
func TestEmptyPrefixIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  addr: base\n")
	var cfg testConfig
	if err := Load(nil, path, "", &cfg); err == nil {
		t.Fatal("Load accepted an empty prefix")
	}
}

// TestMissingRequiredFile pins which file each layer insists on: the embedded
// one when defaults are given, the disk one when they are not.
func TestMissingRequiredFile(t *testing.T) {
	dir := t.TempDir()

	var cfg testConfig
	err := Load(nil, filepath.Join(dir, "app.yaml"), "ORDERS_", &cfg)
	if err == nil {
		t.Fatal("Load accepted a missing disk file with no embedded defaults")
	}

	path := write(t, dir, "app.yaml", "catalog:\n  addr: disk\n")
	err = Load(embed("other.yaml", "catalog:\n  addr: x\n"), path, "ORDERS_", &cfg)
	if err == nil {
		t.Fatal("Load accepted embedded defaults with no app.yaml in them")
	}
}

// TestDiskFileOptionalWithDefaults pins the other half: once defaults are
// embedded, the image runs with no file on disk at all.
func TestDiskFileOptionalWithDefaults(t *testing.T) {
	dir := t.TempDir()
	defaults := embed("app.yaml", "catalog:\n  addr: embedded\n")
	var cfg testConfig
	if err := Load(defaults, filepath.Join(dir, "app.yaml"), "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Addr != "embedded" {
		t.Fatalf("addr = %q, want embedded", cfg.Catalog.Addr)
	}
}

// TestExtensionPicksTheParser pins that the extension decides, with no
// searching and no precedence between formats.
func TestExtensionPicksTheParser(t *testing.T) {
	dir := t.TempDir()
	yml := write(t, dir, "app.yml", "catalog:\n  addr: from-yml\n")
	var ymlCfg testConfig
	if err := Load(nil, yml, "ORDERS_", &ymlCfg); err != nil {
		t.Fatalf("Load .yml: %v", err)
	}
	if ymlCfg.Catalog.Addr != "from-yml" {
		t.Fatalf(".yml addr = %q", ymlCfg.Catalog.Addr)
	}

	path := write(t, dir, "app.toml", "addr = 'x'\n")
	var cfg testConfig
	err := Load(nil, path, "ORDERS_", &cfg)
	if err == nil {
		t.Fatal("Load accepted a format it cannot parse")
	}
	if !strings.Contains(err.Error(), ".toml") {
		t.Fatalf("error %q does not name the extension", err)
	}
}

// TestPropertiesSubset pins every row of the supported half of the table in
// docs/spec.md 3: comments, both separators, trimming, dotted nesting and
// typed values.
func TestPropertiesSubset(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.properties", strings.Join([]string{
		"# a hash comment",
		"! a bang comment",
		"catalog.addr = :8080",
		"catalog.maxConns: 12",
		"   catalog.readHeaderTimeout   =   10s   ",
		"catalog.greeting = hello, world",
		"lifecycle.drainDelay=2s",
		"",
	}, "\n"))

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Addr != ":8080" {
		t.Fatalf("addr = %q", cfg.Catalog.Addr)
	}
	if cfg.Catalog.MaxConns != 12 {
		t.Fatalf("maxConns = %d, want 12 as a number", cfg.Catalog.MaxConns)
	}
	if cfg.Catalog.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("readHeaderTimeout = %v, want 10s", cfg.Catalog.ReadHeaderTimeout)
	}
	if cfg.Catalog.Greeting != "hello, world" {
		t.Fatalf("greeting = %q", cfg.Catalog.Greeting)
	}
	if cfg.Lifecycle.DrainDelay != 2*time.Second {
		t.Fatalf("drainDelay = %v, want 2s", cfg.Lifecycle.DrainDelay)
	}
}

// TestPropertiesFirstSeparatorWins pins that whichever of = and : comes first
// is the separator, so a value may hold the other one.
func TestPropertiesFirstSeparatorWins(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.properties", "catalog.addr: host=1\ncatalog.greeting = a:b\n")
	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Addr != "host=1" {
		t.Fatalf("addr = %q, want host=1", cfg.Catalog.Addr)
	}
	if cfg.Catalog.Greeting != "a:b" {
		t.Fatalf("greeting = %q, want a:b", cfg.Catalog.Greeting)
	}
}

// TestPropertiesUnsupportedErrors pins that the parts go-boot does not
// support fail loudly instead of silently mis-parsing. Each case checks the
// wording, because an unknown-key error would otherwise hide a missing guard.
func TestPropertiesUnsupportedErrors(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"unicode escape", "catalog.greeting = caf\\u00e9\n", "escapes"},
		{"line continuation", "catalog.greeting = one \\\n  two\n", "continuation"},
		{"escaped separator", "catalog.gree\\=ting = x\n", "escape inside a key"},
		{"indexed key", "catalog.hosts[0] = a\n", "indexed"},
		{"no separator", "catalog.addr\n", "separator"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := write(t, dir, "app.properties", c.body)
			var cfg testConfig
			err := Load(nil, path, "ORDERS_", &cfg)
			if err == nil {
				t.Fatalf("Load accepted %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// TestPropertiesKeepsABackslashInAValue pins the other side of the table: a
// backslash in a value is not on the unsupported list, so it survives.
func TestPropertiesKeepsABackslashInAValue(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.properties", `catalog.greeting = C:\logs`+"\n")
	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Greeting != `C:\logs` {
		t.Fatalf("greeting = %q, want the backslash kept", cfg.Catalog.Greeting)
	}
}

// inlineConfig is the shape a Preset user writes: go-boot's own Config
// embedded inline, with the service's own section beside it.
type inlineConfig struct {
	Config  `yaml:",inline"`
	Catalog catalogConfig `yaml:"catalog"`
}

// TestInlineEmbeddingBinds pins that a struct embedded with yaml:",inline"
// binds its keys at the top level, which is how every Preset user writes it.
func TestInlineEmbeddingBinds(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "log:\n  level: WARN\ncatalog:\n  addr: x\n")
	var cfg inlineConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "WARN" {
		t.Fatalf("log.level = %q; the inline section became one of its own", cfg.Log.Level)
	}
	if cfg.Catalog.Addr != "x" {
		t.Fatalf("catalog.addr = %q", cfg.Catalog.Addr)
	}
}

// TestOverrideReplacesAPreFilledList pins that an override replaces the whole
// list. Decoding into the elements already there would leave half of an old
// entry behind.
func TestOverrideReplacesAPreFilledList(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  servers:\n    - host: b\n")
	cfg := testConfig{Catalog: catalogConfig{Servers: []server{{Host: "a", Port: 9000}}}}
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []server{{Host: "b"}}
	if len(cfg.Catalog.Servers) != 1 || cfg.Catalog.Servers[0] != want[0] {
		t.Fatalf("servers = %+v, want %+v with no port left over", cfg.Catalog.Servers, want)
	}
}

// TestPreFillSurvivesAPartialSection pins the bottom layer: a key nobody
// names keeps whatever the struct came with.
func TestPreFillSurvivesAPartialSection(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "log:\n  level: WARN\n")
	cfg := testConfig{Log: LogConfig{Level: "INFO", Format: "json"}}
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "WARN" {
		t.Fatalf("log.level = %q, want the file to win", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Fatalf("log.format = %q; writing one key wiped its neighbour", cfg.Log.Format)
	}
}

// TestEnvValueIsNotYAML pins that a value is typed, not parsed as a YAML
// document: a hash is part of the string, not the start of a comment.
func TestEnvValueIsNotYAML(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  addr: base\n")
	t.Setenv("ORDERS_CATALOG__GREETING", "hello # world")
	t.Setenv("ORDERS_CATALOG__ADDR", "0644")
	t.Setenv("ORDERS_CATALOG__MAX_CONNS", "12")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Greeting != "hello # world" {
		t.Fatalf("greeting = %q; the hash was read as a comment", cfg.Catalog.Greeting)
	}
	if cfg.Catalog.Addr != "0644" {
		t.Fatalf("addr = %q; a leading zero turned it into another number", cfg.Catalog.Addr)
	}
	if cfg.Catalog.MaxConns != 12 {
		t.Fatalf("maxConns = %d, want 12", cfg.Catalog.MaxConns)
	}
}

// TestTwoSpellingsOfOneKeyIsAnError pins that a document naming one key twice
// fails, rather than letting Go's map order pick the winner.
func TestTwoSpellingsOfOneKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  maxConns: 1\n  max_conns: 2\n")
	var cfg testConfig
	err := Load(nil, path, "ORDERS_", &cfg)
	if err == nil {
		t.Fatal("Load accepted one key spelled two ways")
	}
	if !strings.Contains(err.Error(), "same key") {
		t.Fatalf("error %q does not say the two spellings are one key", err)
	}
}

// TestProfileKeyIsRelaxed pins that the reserved key follows the same relaxed
// rule as every other one, so a lowercase spelling still selects a Profile
// instead of vanishing.
func TestProfileKeyIsRelaxed(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "catalog:\n  addr: base\n")
	write(t, dir, "app-local.yaml", "catalog:\n  addr: local\n")
	t.Setenv("ORDERS_profile", "local")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Catalog.Addr != "local" {
		t.Fatalf("addr = %q; a lowercase PROFILE selected nothing", cfg.Catalog.Addr)
	}
}

// TestServiceOwnsItsKeys pins that a service puts its own section in the same
// struct as go-boot's and nothing breaks.
func TestServiceOwnsItsKeys(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "app.yaml", "log:\n  level: WARN\ncatalog:\n  addr: :9090\n")
	t.Setenv("ORDERS_CATALOG__ADDR", ":9091")

	var cfg testConfig
	if err := Load(nil, path, "ORDERS_", &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "WARN" {
		t.Fatalf("go-boot's own key did not bind: %q", cfg.Log.Level)
	}
	if cfg.Catalog.Addr != ":9091" {
		t.Fatalf("the service's key did not bind: %q", cfg.Catalog.Addr)
	}

	// The loaded struct is what New takes, so the two halves meet here.
	app, err := New(Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !app.Log.Enabled(t.Context(), 8) { // slog.LevelWarn
		t.Fatal("the loaded log level did not reach the App")
	}
}
