package goboot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-viper/mapstructure/v2"
	yaml "go.yaml.in/yaml/v3"
)

// Load fills out from embedded defaults, then a file on disk, then
// PREFIX-scoped environment variables. Later sources win.
//
// defaults may be nil. path names the file in both layers: the embedded
// lookup uses its base name, the disk lookup uses it whole.
//
//	//go:embed app.yaml
//	var defaults embed.FS
//
//	goboot.Load(defaults, "app.yaml", "ORDERS_", &cfg)
//
// Whatever out already holds is the bottom layer, so a Starter's own defaults
// are written in Go and survive every key the files leave out.
//
// The prefix belongs to the service, not to go-boot, and must not be empty:
// the environment layer claims every variable under it, and an unknown key is
// a startup error, so an empty prefix turns PATH and HOME into config.
//
// Six things are deliberately absent, so nobody files them as gaps. There is
// no flag layer: flag needs every key declared up front, so a working one
// means the service hand-writing a flag.String line per key. There is no
// Validate hook: the decode already reports unknown keys and type mismatches,
// and anything semantic belongs in a Starter's Start, which returns an error
// already. There is no effective-config endpoint and no effective-config
// logging, because both print the database password. There is no secret-store
// hook: secrets come from environment variables. There is no hot reload:
// config is immutable once loaded. And there is no JSON or TOML, because a
// second format is a second thing to document for no user who asked.
//
// <PREFIX>PROFILE names one Profile. It layers app-<profile>.<ext> over
// app.<ext> in both the embedded and the disk layer, and a missing profile
// file is fine.
func Load(defaults fs.FS, path, prefix string, out any) error {
	if prefix == "" {
		return errors.New("config: the environment prefix must not be empty")
	}
	parse, err := parserFor(path)
	if err != nil {
		return err
	}
	profile := profileOf(prefix)

	merged := map[string]any{}
	for _, l := range fileLayers(defaults, path, profile) {
		b, err := l.read()
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist) && !l.required:
			continue
		default:
			return fmt.Errorf("config %s: %w", l.label, err)
		}
		m, err := parse(b)
		if err != nil {
			return fmt.Errorf("config %s: %w", l.label, err)
		}
		merge(merged, m)
	}
	merge(merged, envLayer(prefix))
	return decode(merged, out)
}

// profileOf reads the reserved <PREFIX>PROFILE variable. The lookup is
// relaxed like every other key, so ORDERS_profile selects one too.
func profileOf(prefix string) string {
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, prefix) && canon(strings.TrimPrefix(k, prefix)) == "profile" {
			return v
		}
	}
	return ""
}

// layer is one source of keys, in the order it is applied.
type layer struct {
	label    string
	read     func() ([]byte, error)
	required bool
}

// fileLayers lists the four file sources: embedded base and profile, then the
// same two on disk. The base file is required in whichever layer is the only
// one that has it; a profile file never is.
func fileLayers(defaults fs.FS, path, profile string) []layer {
	var ls []layer
	if defaults != nil {
		base := filepath.Base(path)
		ls = append(ls, layer{
			label:    "embedded " + base,
			read:     func() ([]byte, error) { return fs.ReadFile(defaults, base) },
			required: true,
		})
		if profile != "" {
			name := profileName(base, profile)
			ls = append(ls, layer{
				label: "embedded " + name,
				read:  func() ([]byte, error) { return fs.ReadFile(defaults, name) },
			})
		}
	}
	ls = append(ls, layer{
		label:    path,
		read:     func() ([]byte, error) { return os.ReadFile(path) },
		required: defaults == nil,
	})
	if profile != "" {
		name := profileName(path, profile)
		ls = append(ls, layer{
			label: name,
			read:  func() ([]byte, error) { return os.ReadFile(name) },
		})
	}
	return ls
}

// profileName turns app.yaml into app-local.yaml. The profile file keeps the
// base file's extension.
func profileName(path, profile string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + profile + ext
}

// parserFor picks the parser from the file extension. One file, one format:
// there is no searching and no precedence between formats.
func parserFor(path string) (func([]byte) (map[string]any, error), error) {
	switch ext := filepath.Ext(path); ext {
	case ".yaml", ".yml":
		return parseYAML, nil
	case ".properties":
		return parseProperties, nil
	default:
		return nil, fmt.Errorf("config %s: unsupported file extension %q", path, ext)
	}
}

func parseYAML(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return canonMap(m)
}

// parseProperties reads the subset in docs/spec.md 3. Everything outside that
// subset is an error naming the line, because a properties file that silently
// mis-parses is worse than one that refuses to load.
func parseProperties(b []byte) (map[string]any, error) {
	m := map[string]any{}
	for i, line := range strings.Split(string(b), "\n") {
		n := i + 1
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "!") {
			continue
		}
		if strings.Contains(s, `\u`) {
			return nil, fmt.Errorf(`line %d: \uXXXX escapes are not supported`, n)
		}
		if strings.HasSuffix(s, `\`) {
			return nil, fmt.Errorf("line %d: line continuations are not supported", n)
		}
		cut := firstSeparator(s)
		if cut < 0 {
			return nil, fmt.Errorf("line %d: no = or : separator", n)
		}
		key := strings.TrimSpace(s[:cut])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", n)
		}
		if strings.Contains(key, `\`) {
			return nil, fmt.Errorf("line %d: an escape inside a key is not supported", n)
		}
		if strings.ContainsAny(key, "[]") {
			return nil, fmt.Errorf("line %d: indexed keys are not supported; use YAML, or bare commas", n)
		}
		set(m, strings.Split(key, "."), parseValue(strings.TrimSpace(s[cut+1:])))
	}
	return m, nil
}

// firstSeparator reports whichever of = and : comes first, so the other one
// may appear in the value.
func firstSeparator(s string) int {
	eq, colon := strings.Index(s, "="), strings.Index(s, ":")
	switch {
	case eq < 0:
		return colon
	case colon < 0:
		return eq
	default:
		return min(eq, colon)
	}
}

// envLayer reads every variable under the prefix. A double underscore splits
// sections; a single one is part of a name, which canon then drops anyway.
func envLayer(prefix string) map[string]any {
	m := map[string]any{}
	env := os.Environ()
	slices.Sort(env) // so two spellings of one key settle the same way twice
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := strings.TrimPrefix(k, prefix)
		if canon(name) == "profile" { // reserved: it picks the Profile
			continue
		}
		set(m, strings.Split(name, "__"), parseValue(v))
	}
	return m
}

// parseValue types a value from the environment or a properties file, so
// 8080 is a number and true a boolean. Everything else stays the string it
// was: "10s" reaches the duration hook, and "a # b" keeps its hash, which a
// full YAML parse would have read as a comment and thrown away.
func parseValue(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	}
	// Both numbers must survive a round trip, so a leading zero or a stray
	// underscore stays a string instead of turning into a different number.
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && strconv.FormatInt(n, 10) == v {
		return int(n)
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && strconv.FormatFloat(f, 'g', -1, 64) == v {
		return f
	}
	if strings.HasPrefix(v, "[") { // a YAML flow list
		var list []any
		if yaml.Unmarshal([]byte(v), &list) == nil {
			return list
		}
	}
	return v
}

// set puts v at path in m, creating sections on the way and making every key
// canonical.
func set(m map[string]any, path []string, v any) {
	cur := m
	for _, p := range path[:len(path)-1] {
		next, ok := cur[canon(p)].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[canon(p)] = next
		}
		cur = next
	}
	cur[canon(path[len(path)-1])] = v
}

// canon is the relaxed key of ADR 0002: lowercase, with - and _ dropped, so
// readHeaderTimeout, read-header-timeout and READ_HEADER_TIMEOUT are one key.
// Every layer is canonical before the merge, so two spellings of one key
// cannot both survive into the decode and race to win.
func canon(k string) string {
	var b strings.Builder
	b.Grow(len(k))
	for _, r := range k {
		if r == '-' || r == '_' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// canonMap makes every key in a parsed document canonical. Two spellings of
// one key in one document are an error: which of them won would otherwise
// depend on Go's map order, and that is no way to settle a config value.
func canonMap(m map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(m))
	seen := make(map[string]string, len(m))
	for k, v := range m {
		c := canon(k)
		if first, clash := seen[c]; clash {
			return nil, fmt.Errorf("%q and %q are the same key", first, k)
		}
		seen[c] = k
		if sub, ok := v.(map[string]any); ok {
			s, err := canonMap(sub)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			v = s
		}
		out[c] = v
	}
	return out, nil
}

// merge lays src over dst. Sections merge key by key; everything else, a list
// included, is replaced wholesale, because there is no sane rule for merging
// a list element by element.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if cur, ok := dst[k].(map[string]any); ok {
				merge(cur, sub)
				continue
			}
		}
		dst[k] = v
	}
}

// decode binds the merged map onto out in one pass, under the yaml tags the
// struct already carries.
func decode(m map[string]any, out any) error {
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      out,
		TagName:     "yaml",
		ErrorUnused: true, // a mistyped key fails at startup, naming its path
		// Whatever a key names is written whole, so an override replaces a
		// list rather than decoding into the elements already there. A key
		// nobody names is left alone, which is what keeps the pre-fill layer.
		ZeroFields: true,
		// A Preset user embeds goboot.Config with `yaml:",inline"`, which is
		// the yaml spelling of what mapstructure calls squash.
		SquashTagOption: "inline",
		MatchName:       func(key, field string) bool { return canon(key) == canon(field) },
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			// Fires only when the field is a list, so a string keeps its commas.
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return err
	}
	if err := dec.Decode(m); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}
