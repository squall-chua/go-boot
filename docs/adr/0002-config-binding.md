# Config keys bind loosely, and lists split by target type

go-boot reads config from an embedded file, a file on disk, a profile overlay of each, and
environment variables. Rather than make each of those spell keys the same way, go-boot matches keys
**loosely**: it lowercases both sides and drops `-` and `_`, so `readHeaderTimeout`,
`read-header-timeout`, `read_header_timeout` and `READ_HEADER_TIMEOUT` are all the same key. A value
with commas becomes a list only when the target field is a list, so `hosts=a,b,c` is three hosts but
`greeting=hello, world` stays one string. Both rules come from Spring Boot, whose users are go-boot's
audience.

This is a public promise. Once people's config files rely on it, it cannot be tightened.

## Considered options

- **Exact tag matching**, which is what a plain `yaml.Decode` gives. Then every tag has to be
  all-lowercase — `readheadertimeout`, `maxopenconns` — because the environment layer lowercases each
  segment. Readable in Go, unreadable in the file a human edits.
- **Kebab-case tags with a `_`-to-`-` mapping.** One line, and it fixes the file but not the
  general problem: every source still has to spell keys exactly one way.
- **Hand-written reflection**, about 50 lines, to walk the target struct for both the name matching
  and the list splitting. Rejected because it turns the loader into something a reader can no longer
  skim, which was the main argument in [#4](https://github.com/squall-chua/go-boot/issues/4) for
  writing it ourselves.

## Consequences

- **A second dependency**, `github.com/go-viper/mapstructure/v2`, alongside `go.yaml.in/yaml/v3`.
  #4 concluded "one dependency", so its record is amended rather than quietly contradicted.
  `mapstructure` v2.5.0 has no transitive dependencies: 1 `go.sum` module, 1 linked module. koanf,
  which sits on the same library, costs 16.
- **The loader gets shorter.** Every layer is parsed into `map[string]any`, merged as maps, then
  decoded once at the end. That replaces a marshal-and-re-decode step, and error messages become
  keyed by config path instead of a line number pointing at a merged document the user never wrote.
- **A list cannot be built up across layers.** An override replaces a slice wholesale. There is no
  sane rule for merging element-wise, and koanf and viper both behave the same way.
- **Unknown keys stay a hard error**, now via `ErrorUnused` rather than `KnownFields`. Combined with
  the environment prefix, this means go-boot claims every variable under that prefix, so the prefix
  must belong to the service — not to go-boot, and never empty.
