# Config library for the base Starter: koanf vs viper vs stdlib

Research for [#4](https://github.com/squall-chua/go-boot/issues/4). Date: 2026-08-24.
Toolchain used for all measurements: `go1.26.3 linux/amd64`.

**Recommendation up front: stdlib plus a ~80-line loader, with `go.yaml.in/yaml/v3` as the
single third-party dependency.** Reasoning in [Recommendation](#recommendation).

---

## 1. Dependency weight (measured, not guessed)

Four scratch modules were built under a temp dir, each doing the same job the base Starter
needs — read a YAML file, overlay env vars, overlay flags, unmarshal into a typed struct.
Every number below comes from `go mod tidy` + `go list` + `go build` on those modules.

| | go.sum modules | `go mod graph` edges | `go list -m all` | linked non-std **packages** | linked non-std **modules** | binary bytes |
|---|---|---|---|---|---|---|
| **stdlib + hand loader** (`go.yaml.in/yaml/v3`) | **1** | **3** | **2** | **1** | **1** | **3,757,305** |
| yaml-only baseline (no loader logic) | 2 | 4 | 3 | 1 | 1 | 3,491,409 |
| **koanf, lean** (v2 + yaml + rawbytes + env/v2) | **16** | **42** | **17** | **11** | **9** | **4,147,238** |
| **koanf, with file+flag providers** | **19** | **53** | **20** | **15** | **12** | **4,310,573** |
| **viper** v1.21.0 | **23** | **65** | **27** | **53** | **13** | **7,965,091** |

Notes on the numbers:

- The "yaml-only baseline" row used `gopkg.in/yaml.v3` (2 go.sum modules because of its
  `gopkg.in/check.v1` test require); the hand-loader row uses the maintained
  `go.yaml.in/yaml/v3`, which has a cleaner graph — hence 1 module, not 2.
- **viper links 53 non-stdlib packages vs koanf's 11–15** — a 3.5–5x difference in code
  actually compiled into the binary, and the binary is ~1.9x the size of the hand loader's.
- koanf's transitive modules (measured): `github.com/go-viper/mapstructure/v2`,
  `github.com/knadh/koanf/maps`, `github.com/mitchellh/copystructure`,
  `github.com/mitchellh/reflectwalk`, `go.yaml.in/yaml/v3`. Adding
  `providers/file` also pulls `github.com/fsnotify/fsnotify` and `golang.org/x/sys` — those
  two are avoidable by using `os.ReadFile` + `providers/rawbytes` if hot-reload is not wanted
  (issue #1 puts hot-reload out of scope).
- viper's transitive modules (measured): `fsnotify`, `go-viper/mapstructure/v2`,
  `pelletier/go-toml/v2`, `sagikazarmark/locafero`, `sourcegraph/conc`, `spf13/afero`,
  `spf13/cast`, `spf13/pflag`, `subosito/gotenv`, `go.yaml.in/yaml/v3`, `golang.org/x/sys`,
  `golang.org/x/text`. All of them are unconditional — you get the TOML parser, the dotenv
  parser, an in-memory filesystem abstraction and a concurrency helper library whether or not
  you use them.
- koanf's own wiki claims a 2.9 MB vs 12 MB binary comparison
  ([wiki](https://github.com/knadh/koanf/wiki/Comparison-with-spf13-viper)). My measurement
  (4.3 MB vs 8.0 MB) is smaller but directionally identical; their figure is probably from an
  older viper. Treat their number as stale, mine as current.

Sources for the library versions resolved: [`koanf/v2` v2.3.6](https://pkg.go.dev/github.com/knadh/koanf/v2),
[`viper` v1.21.0](https://pkg.go.dev/github.com/spf13/viper),
[`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml).

### 1a. Go version floor — this one is a blocker

Issue #1 fixes **Go 1.22 minimum**. Measured `go` directives in the dependencies' own
`go.mod` files (via `go mod download -json`):

| module | `go` directive |
|---|---|
| `github.com/knadh/koanf/v2` v2.1.2 | `go 1.18` |
| `github.com/knadh/koanf/v2` v2.2.0 … v2.3.6 | **`go 1.23.0`** |
| `github.com/knadh/koanf/parsers/yaml` v1.1.1 | `go 1.23.0` |
| `github.com/knadh/koanf/providers/env/v2` v2.0.1 | `go 1.23.0` |
| `github.com/spf13/viper` v1.19.0 | `go 1.20` |
| `github.com/spf13/viper` v1.20.0/v1.20.1 | `go 1.21.0` |
| `github.com/spf13/viper` v1.21.0 | **`go 1.23.0`** |
| `go.yaml.in/yaml/v3` v3.0.3+ | `go 1.22` |

viper's README states the requirement plainly: *"go version >=1.23"*
([README](https://github.com/spf13/viper/blob/master/README.md)).

Verified empirically: a module declaring `go 1.22` that requires `viper v1.21.0` fails a
default (`-mod=readonly`) build with `go: updates to go.mod needed; to update it: go mod tidy`,
and `-mod=mod` **silently rewrites the `go` directive to `go 1.23.0`**. Same for koanf v2.3.6.
This is the documented main-module-floor rule
([Go modules reference](https://go.dev/ref/mod#go-mod-file-go)).

**So: adopting either koanf or viper forces go-boot's stated Go 1.22 minimum up to 1.23.**
Pinning `koanf/v2` at v2.1.2 (`go 1.18`) would preserve 1.22 but freezes go-boot on a version
from before v2.2.0. `go.yaml.in/yaml/v3` at `go 1.22` is the only option that leaves the floor
where issue #1 put it.

---

## 2. Typed binding and error reporting

All three routes were run against the same deliberately-broken YAML
(`server: {port: notanint, prot: 3}`) to capture real error text.

**koanf** — `k.Unmarshal("", &out)` and
`k.UnmarshalWithConf("", &out, koanf.UnmarshalConf{Tag: "koanf", DecoderConfig: …})`. Default
struct tag is `koanf`. Under the hood it uses `github.com/go-viper/mapstructure/v2`
([README](https://github.com/knadh/koanf/blob/master/README.md)). Measured output:

```
koanf Unmarshal err: decoding failed due to the following error(s):

'server.port' cannot parse value as 'int': strconv.ParseInt: invalid syntax
```

With `DecoderConfig.ErrorUnused: true`, typos are caught too, and errors aggregate:

```
'server.port' expected type 'int', got unconvertible type 'string'
'server' has invalid keys: prot
```

**viper** — `viper.Unmarshal(&out)` / `viper.UnmarshalExact(&out)`, struct tag `mapstructure`.
Measured output is *byte-for-byte the same shape* as koanf's, because both delegate to
`go-viper/mapstructure/v2`:

```
viper UnmarshalExact err: decoding failed due to the following error(s):

'server.port' cannot parse value as 'int': strconv.ParseInt: invalid syntax
'server' has invalid keys: prot
```

**Hand loader** — merge into `map[string]any`, re-marshal, decode with
`yaml.Decoder.KnownFields(true)`. Measured output:

```
config: yaml: unmarshal errors:
  line 2: field prot not found in type main.Server
config: yaml: unmarshal errors:
  line 2: cannot unmarshal !!str `notanint` into int
```

Verdict on errors: koanf and viper are **identical** and are keyed by dotted config path,
which is the more useful identifier for a user staring at a YAML file. The hand loader gives
a line number, but **the line number refers to the merged intermediate document, not the
user's source file** — a real, if minor, downside. Both mapstructure-based routes aggregate
all field errors in one message; `yaml.v3` also aggregates.

One behavioural note worth recording: koanf's *default* `Unmarshal` is weakly typed (it tries
`strconv` on strings), which is what makes env-var strings bind to `int` fields. The hand
loader gets the same effect by YAML-parsing each env value before inserting it (a `1` becomes
an int, `true` a bool, everything else a string).

---

## 3. Source precedence

**koanf** — fully user-controlled, and this is the headline design decision:

> "koanf does not impose any ordering on loading config from various providers. Every
> successive `Load()` or `Merge()` merges new config into the existing config."
> — [README](https://github.com/knadh/koanf/blob/master/README.md)

Scalars from a later `Load()` win; nested maps merge recursively. `koanf.Conf{StrictMerge: true}`
errors on type conflicts, and `WithMergeFunc` allows a custom strategy. This is exactly the
shape a Spring-Boot-like Starter wants.

**viper** — fixed, not configurable:

> "Viper uses the following precedence order. Each item takes precedence over the item below
> it: explicit call to `Set`, flag, env, config, key/value store, default"
> — [README](https://github.com/spf13/viper/blob/master/README.md)

The order happens to be sensible, but you cannot change it, and viper only reads **one**
config file (plus `MergeInConfig` as an add-on), which is the mechanism a profile overlay
needs.

**Hand loader** — precedence is literally the order of statements in the function. Nothing to
learn, nothing to configure, and it is readable in the source, which matches CONTEXT.md's
"written in plain Go so a reader can copy its body and edit it".

---

## 4. Profiles (dev/prod)

**Neither library has profiles.** Neither README mentions the concept. The nearest equivalent
is loading a base file then an environment-specific overlay:

- **koanf**: natural — repeated `Load()` calls merge, so `app.yaml` then `app-dev.yaml` works
  out of the box ([README](https://github.com/knadh/koanf/blob/master/README.md)).
- **viper**: awkward — the primary API reads a single config file; overlays need
  `MergeInConfig`, and viper lowercases all keys along the way (see §5).
- **Hand loader**: three lines (`files := []string{name+".yaml"}`, append the profile file,
  loop). Profile file absent is treated as fine.

Since profiles are a go-boot invention either way, no library gives it to you free. This
criterion does not favour a dependency.

---

## 5. Formats, and what YAML costs

**koanf** — every parser is a **separate Go module**, installed only if used:
`json`, `yaml`, `toml`, `toml/v2`, `dotenv`, `hcl`, `hjson`, `huml`, `nestedtext`
([README](https://github.com/knadh/koanf/blob/master/README.md)). So YAML *does* cost an
extra module (`github.com/knadh/koanf/parsers/yaml`), but that is the point of the design —
you pay only for YAML. Measured: it pulls `go.yaml.in/yaml/v3`, the maintained fork.

**viper** — JSON, TOML, YAML, INI, envfile, Java properties, all **unconditional**
([README](https://github.com/spf13/viper/blob/master/README.md)). YAML costs nothing extra
because you already paid for TOML and dotenv whether you wanted them or not. Only the remote
K/V store support is opt-in (`import _ "github.com/spf13/viper/remote"`).

**Hand loader** — YAML costs exactly one module. JSON would cost zero (`encoding/json`, and
the same map-merge works unchanged). TOML would cost a module; go-boot does not need TOML.

**Important YAML finding, applies to all three routes:** `gopkg.in/yaml.v3`
(`github.com/go-yaml/yaml`) is **archived and explicitly unmaintained** — its README opens
with "THIS PROJECT IS UNMAINTAINED" ([go-yaml/yaml](https://github.com/go-yaml/yaml)). The
maintained successor is the YAML-org fork at [yaml/go-yaml](https://github.com/yaml/go-yaml),
module path `go.yaml.in/yaml/v3`. koanf and viper both already resolve to it (measured:
`go.yaml.in/yaml/v3 v3.0.3`/`v3.0.4`). **go-boot should import `go.yaml.in/yaml/v3`, not
`gopkg.in/yaml.v3`**, regardless of which option is chosen. Verified: the hand loader builds
and passes its self-check against `go.yaml.in/yaml/v3 v3.0.5` with `go 1.22`.

### Key-case behaviour (measured)

Fed `Server:\n  MaxConns: 5\n  apiKey: secret`:

```
koanf keys: [Server.MaxConns Server.apiKey]        # case preserved
viper keys: [server.maxconns server.apikey]        # forcibly lowercased
```

viper mangling key case is koanf's stated founding grievance, and it reproduces today. It is
transparent for `Get` (which lowercases lookups too) and for struct-tag matching, but it bites
anything that round-trips keys or uses `map[string]T` config values.

---

## 6. Maintenance and API stability (checked 2026-08-24 via GitHub API)

| | knadh/koanf | spf13/viper |
|---|---|---|
| Stars | 4,173 | 30,446 |
| Latest release | **v2.3.6, 2026-08-04** | **v1.21.0, 2025-09-08** |
| Last commit on default branch | **2026-08-09** | **2025-10-15** |
| Open issues (non-PR) | 0 | 9 |
| Open PRs | 4 | 124 |
| License | MIT | MIT |
| Archived | no | no |
| `go.mod` files on GitHub referencing it | ~6,700 | ~86,000 |

- koanf release cadence in the last 12 months: v2.2.2 (2025-07), v2.3.0 (2025-09), v2.3.1/.2
  (2026-01), v2.3.3/.4 (2026-03), v2.3.5 (2026-05), v2.3.6 (2026-08)
  ([releases](https://github.com/knadh/koanf/releases)). Steady, all patch/minor within v2.
  No v3 in sight.
- viper: **~10 months since the last commit to `master`** and ~11 months since the last
  release. **124 open PRs against 9 open issues** is the signal to read here — the low issue
  count is not health. viper added a `stale.yaml` workflow and a commit
  *"chore: turn up the number of stale issue ops from 30 to 500"* (2025-10-15), i.e. the open
  issue list was mass-closed by a bot, not resolved. 927 issues are closed.
- viper's own README states the project's posture:
  > "The Viper project is currently prioritizing backwards compatibility and stability over
  > features. Features may be deferred until Viper 2 forms."

  A "Viper 2" is referenced but has no branch, no timeline and no release. Meanwhile v1.21.0
  did bump the Go floor from 1.21 to 1.23 without a major version, so "stability" here means
  API stability, not dependency stability.
- **v2/v3 churn**: koanf's v1→v2 happened in 2023 and is settled; note that its *providers*
  and *parsers* version independently and some have their own v2 (`env/v2`, `vault/v2`,
  `etcd/v2`, `toml/v2`), which is a small ongoing import-path tax. viper is still v1 after a
  decade.
- **Notable adopter, primary source**: the OpenTelemetry Collector's `confmap` module depends
  on `github.com/knadh/koanf/v2 v2.3.6` and `github.com/knadh/koanf/maps`
  ([confmap/go.mod](https://github.com/open-telemetry/opentelemetry-collector/blob/main/confmap/go.mod)).
  That is a serious, actively-maintained production user of koanf's core.

---

## 7. Has anything newer become the community default?

Checked (stars / open issues / last push, GitHub API, 2026-08-24):

| repo | stars | open | last push |
|---|---|---|---|
| spf13/viper | 30,446 | 133 | 2026-01-12 |
| caarlos0/env | 6,299 | 31 | 2026-08-03 |
| kelseyhightower/envconfig | 5,465 | 58 | 2025-06-28 |
| knadh/koanf | 4,173 | 4 | 2026-08-09 |
| alecthomas/kong | 3,160 | 40 | 2026-08-24 |
| ilyakaznacheev/cleanenv | 2,161 | 55 | 2025-09-15 |
| sethvargo/go-envconfig | 1,234 | 0 | 2026-07-19 |
| cristalhq/aconfig | 639 | 19 | 2025-11-28 |

Findings:

- **No new layered-config library has displaced koanf or viper.** The genuinely active
  newcomers (`caarlos0/env`, `sethvargo/go-envconfig`) are **env-vars-only struct binders** —
  no file loading, no layering, no precedence. They solve a strict subset of what the base
  Starter needs, so they are not candidates on their own. They are, however, evidence of where
  the community's taste has moved: small, single-purpose, struct-tag-driven, near-zero deps.
- `alecthomas/kong` is a CLI/flag parser (very active), not a config loader; it would compete
  with `flag`, not with koanf.
- Awesome Go's configuration page lists ~60 libraries alphabetically with no popularity data
  ([awesome-go.com/configuration](https://awesome-go.com/configuration/)) — no signal there.
- **Nothing arrived in the stdlib.** Go 1.25 added an experimental `encoding/json/v2` and
  Go 1.26 added `crypto/hpke`, `crypto/mlkem/mlkemtest` and `testing/cryptotest` — no config
  or YAML package in either ([Go 1.25](https://go.dev/doc/go1.25),
  [Go 1.26](https://go.dev/doc/go1.26)). YAML remains outside the stdlib, so *some* third-party
  dependency is unavoidable if go-boot's config format is YAML.

*Unverified:* no official Go Developer Survey question covers config-library choice, so there
is no authoritative popularity source; the table above is GitHub metrics only, and stars are a
lagging indicator that flatters viper's decade-long head start.

---

## 8. The stdlib-plus-small-loader option, measured

I did not estimate this — I wrote it and ran it. Full working loader:
**85 lines total, 78 non-blank non-comment lines**, `gofmt`-clean, one import beyond stdlib.

Signature:

```go
func Load(dir, name, profile, prefix string, fs *flag.FlagSet, out any) error
```

Mechanism, in order: read `<name>.yaml` → merge `<name>-<profile>.yaml` if present (absent is
not an error) → merge `PREFIX_`-scoped env vars, splitting on `__` for nesting and
YAML-parsing each value so types come out right → merge flags that were *actually set*
(`fs.Visit`, so unset flags never shadow a file value) → `yaml.Marshal` the merged map →
decode into `out` with `KnownFields(true)`.

Feature coverage, each one exercised by a runnable self-check that asserts and panics on
failure:

| requirement | covered | how |
|---|---|---|
| YAML file loading | yes | `os.ReadFile` + `yaml.Unmarshal` into `map[string]any` |
| Profile overlay (`app.yaml` + `app-dev.yaml`) | yes | recursive `mergeMap`, 8 lines |
| Env override, nested | yes | `os.Environ()` + prefix filter + `__` path split |
| Flag override, highest precedence | yes | `fs.Visit` — only explicitly-set flags |
| Defaults | yes | **zero code** — pre-fill the struct before calling `Load`; `yaml.v3` leaves keys absent from the document untouched |
| Typed binding into a user struct | yes | `yaml` struct tags, no reflection written by hand |
| Unknown-key / typo detection | yes | `Decoder.KnownFields(true)` |
| Type-mismatch errors | yes | `cannot unmarshal !!str \`notanint\` into int` |
| Configurable precedence | yes | it is the statement order in one readable function |

Self-check result (env beats file, profile beats base, flag beats both, defaults survive,
unknown key named, type error reported):

```
{Server:{Port:9999 Host:devhost} Level:warn Extra:default-kept}
unknown-key error: config: yaml: unmarshal errors:
  line 2: field prot not found in type main.Server
type error: config: yaml: unmarshal errors:
  line 2: cannot unmarshal !!str `notanint` into int
OK
```

**Known limitations of the 78-line version** (all real, all recorded honestly):

1. Error line numbers refer to the **merged intermediate document**, not the user's source
   file. Fixing this properly means tracking provenance per key — roughly doubles the code.
   The cheaper mitigation is to name the source file in the wrapped error and accept that the
   line number is approximate.
2. `KnownFields(true)` makes unknown keys a **hard error**. That is the right default for a
   framework (it catches typos that would otherwise silently do nothing) but it is a design
   decision to write down, not an accident.
3. Slices are replaced wholesale by an override, never merged element-wise. koanf and viper
   behave the same way; noting it so nobody is surprised.
4. Env nesting uses `__` as the separator by convention (`GB_SERVER__PORT` → `server.port`).
   Needs documenting. koanf's `env` provider makes you supply the same mapping function
   yourself, so this is not extra work relative to koanf.
5. No file watching / hot reload. Issue #1 already puts hot-reload dev tooling out of scope.
6. Not benchmarked. Irrelevant — this runs once at startup on a file of tens of keys.

**Is 78 lines less total complexity than a dependency?** Yes, on the evidence:

- It is 78 lines *that a reader can read*, versus 11–53 non-stdlib packages that a reader
  cannot. CONTEXT.md's Preset definition — "written in plain Go so a reader can copy its body
  and edit it" — describes this loader exactly.
- It costs 1 go.sum module against koanf's 16–19 and viper's 23, and the one module
  (`go.yaml.in/yaml/v3`) is a dependency of the koanf and viper routes too, so it is not a
  saving either of them can offer.
- It is the **only** option that keeps the Go 1.22 floor from issue #1.
- The features go-boot actually needs are the cheap half of what koanf does. koanf's real
  value is the *other* half — Vault, S3, etcd, Consul, AWS Parameter Store, nine parsers,
  `StrictMerge`, `WithMergeFunc`, `Watch`. go-boot v1 needs none of it.

---

## Recommendation

**Write the loader. Take `go.yaml.in/yaml/v3` as the one third-party dependency, keep
`go 1.22`, and do not add koanf or viper to the base Starter.**

This is the answer the map's own rule produces. The rule is "stdlib first; one well-known
third-party library only where stdlib clearly falls short." The stdlib falls short in exactly
one place — Go has no YAML parser — so exactly one dependency is justified, and every option
here pays it. Beyond YAML, stdlib does not fall short at all: `os.Environ`, `flag`, a
20-line map merge and `yaml.Decoder` cover layered files, profile overlays, env override,
flag override, defaults, typed binding and typo detection in 78 lines that were written,
run and asserted.

Concretely:

1. Base Starter imports **`go.yaml.in/yaml/v3` only**. Not `gopkg.in/yaml.v3` — it is
   archived and its README says "THIS PROJECT IS UNMAINTAINED".
2. Precedence, fixed and documented: **defaults (struct pre-fill) < base file < profile file
   < env < explicitly-set flags**. This matches viper's ordering, which is the one Go
   developers already expect.
3. Profiles are go-boot's own: `app.yaml` plus optional `app-<profile>.yaml`, profile
   selected by env var or flag. Neither library would have given this away free.
4. Strict decoding on (`KnownFields(true)`), so a mistyped key fails at startup instead of
   silently doing nothing.
5. Env nesting separator `__`, documented.

**Runner-up, and the escape hatch: koanf.** If go-boot ever needs Vault/S3/etcd/Consul
providers, TOML/HCL/dotenv parsers, or config hot-reload, koanf is the library to adopt — it
is actively maintained (release 3 weeks ago, 0 open issues), its precedence model is
user-controlled rather than baked in, it does not mangle key case, its parsers and providers
are separate modules so you pay only for what you use, and the OpenTelemetry Collector runs
its core in production. The cost is real but bounded: +15 go.sum modules and a Go floor of
1.23. Because the loader is 78 lines, swapping to koanf later is a small, contained change —
which is itself an argument for not paying for it now.

**Viper: no.** Ten months without a commit; 124 open PRs against a bot-emptied issue list;
53 non-stdlib packages linked and a binary about twice the size for the same job; a fixed,
non-configurable precedence order; forcible lowercasing of every config key; a Go floor bumped
inside a patch-level release; and a "Viper 2" that is referenced in the README but has no
branch and no timeline. It is the popular choice by stars and the weakest choice on every axis
this ticket asked about.

---

## Sources

- koanf README — <https://github.com/knadh/koanf/blob/master/README.md>
- koanf releases — <https://github.com/knadh/koanf/releases>
- koanf wiki, comparison with viper — <https://github.com/knadh/koanf/wiki/Comparison-with-spf13-viper>
- koanf godoc — <https://pkg.go.dev/github.com/knadh/koanf/v2>
- viper README — <https://github.com/spf13/viper/blob/master/README.md>
- viper releases — <https://github.com/spf13/viper/releases>
- viper godoc — <https://pkg.go.dev/github.com/spf13/viper>
- go-yaml/yaml, unmaintained notice — <https://github.com/go-yaml/yaml>
- yaml/go-yaml, maintained fork (`go.yaml.in/yaml/v3`) — <https://github.com/yaml/go-yaml>
- go-viper/mapstructure — <https://github.com/go-viper/mapstructure>
- OpenTelemetry Collector `confmap/go.mod` — <https://github.com/open-telemetry/opentelemetry-collector/blob/main/confmap/go.mod>
- Go modules reference, `go` directive — <https://go.dev/ref/mod#go-mod-file-go>
- Go 1.25 release notes — <https://go.dev/doc/go1.25>
- Go 1.26 release notes — <https://go.dev/doc/go1.26>
- Awesome Go, configuration — <https://awesome-go.com/configuration/>
- Repository metrics (stars, issues, PRs, commit dates) — GitHub REST API,
  `repos/{owner}/{repo}`, `repos/{owner}/{repo}/releases`, `repos/{owner}/{repo}/commits`,
  `search/issues`, queried 2026-08-24.
- All dependency-weight and error-message figures — measured locally with `go1.26.3` on
  scratch modules; commands were `go mod tidy`, `go mod graph`, `go list -m all`,
  `go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}'`, `go build`, `go mod download -json`.
