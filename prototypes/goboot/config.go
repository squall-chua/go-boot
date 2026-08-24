package goboot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Load fills out from <path> then from PREFIX_-scoped env vars.
//
// STUB of the 78-line loader in docs/research/config-library.md: no profile
// overlay, no flag layer. Precedence here is struct defaults < file < env.
// Env nesting separator is "__" (GB_HTTP__ADDR -> http.addr), and yaml keys
// must be lowercase for the env layer to bind.
func Load(path, prefix string, out any) error {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := decode(b, out); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist): // absent file is fine
	default:
		return err
	}
	return loadEnv(prefix, out)
}

func decode(b []byte, out any) error {
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true) // a mistyped key fails at startup
	if err := d.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func loadEnv(prefix string, out any) error {
	m := map[string]any{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		path := strings.Split(strings.ToLower(strings.TrimPrefix(k, prefix)), "__")
		var val any
		if yaml.Unmarshal([]byte(v), &val) != nil {
			val = v
		}
		cur := m
		for _, p := range path[:len(path)-1] {
			next, _ := cur[p].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cur[p] = next
			}
			cur = next
		}
		cur[path[len(path)-1]] = val
	}
	if len(m) == 0 {
		return nil
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if err := decode(b, out); err != nil {
		return fmt.Errorf("config env %s*: %w", prefix, err)
	}
	return nil
}
