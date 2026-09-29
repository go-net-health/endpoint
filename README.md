# endpoint — go-net-health

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-0D9488)](https://go-net-health.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)](#tests--validation)

**The served side of health checks** — one `http.Handler` answering `/healthz`,
`/readyz` and `/metrics`, the last in the Prometheus text exposition format
0.0.4, with **zero dependencies beyond the standard library**.

[`go-net-health/health`](https://github.com/go-net-health/health) is the
client side: it probes an endpoint. This package is the endpoint.

There is no `prometheus/client_golang` here, no registry and no metric objects.
A `Collector` is a function called on every scrape; it writes the current value
of what it knows to a `Writer`. A binary that already keeps its own counters
exposes them without importing a metrics framework.

## Install

```sh
go get github.com/go-net-health/endpoint
```

## Usage

```go
package main

import (
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-net-health/endpoint"
)

var (
	started     = time.Now()
	served      atomic.Int64
	draining    atomic.Bool
	errDraining = errors.New("draining")
)

func main() {
	ops := endpoint.Handler(endpoint.Options{
		Ready: func() error {
			if draining.Load() {
				return errDraining // /readyz answers 503 "draining\n"
			}
			return nil
		},
		Collectors: []endpoint.Collector{
			endpoint.GoRuntime,
			endpoint.BuildInfo("myapp"),
			func(w *endpoint.Writer) {
				w.Gauge("process_start_time_seconds", "Start time of the process since the Unix epoch.",
					endpoint.S(float64(started.Unix())))
				w.Counter("myapp_requests_total", "Requests served.",
					endpoint.S(float64(served.Load())))
			},
		},
	})
	go http.ListenAndServe("127.0.0.1:9090", ops)
	// ... the application's own server ...
}
```

Mounted under a prefix: `mux.Handle("/-/", http.StripPrefix("/-", ops))`.

## Behaviour

| Path | GET / HEAD | Other methods |
| --- | --- | --- |
| `/healthz` | `200 ok` — always: liveness means the process can answer | `405`, `Allow: GET, HEAD` |
| `/readyz` | `200 ok` if `Ready` is nil or returns nil, else `503` + the error text | `405` |
| `/metrics` | `200`, `Content-Type: text/plain; version=0.0.4; charset=utf-8` | `405` |
| anything else | `404` | `404` |

Every response carries `Cache-Control: no-store`. A `Collector` (or `Ready`)
that **panics** does not kill the server or drop the connection: the panic is
recovered and that request answers `500`, naming the collector and the panic
value. A scrape is all or nothing — half a scrape would make the failed
collector's series look *absent* rather than *unknown*.

### What the Writer guarantees

Whatever a collector writes, the scrape stays parseable:

- **Names** are checked against the Prometheus data model: metric names
  `[a-zA-Z_:][a-zA-Z0-9_:]*`, label names `[a-zA-Z_][a-zA-Z0-9_]*` and not
  starting with `__` (reserved). Label values must be valid UTF-8.
- **Each `Counter` / `Gauge` call is atomic.** An invalid name, label, value or
  a duplicated label set drops the *whole call* — never part of it — and is
  reported twice: `Writer.Err()` returns the first problem, and the scrape
  ends with one `# endpoint: skipped …` comment line per problem (Prometheus
  ignores comments; a person reading the scrape sees them).
- **The same family written twice** (two collectors) is merged under one
  `# HELP` / `# TYPE` when the types agree; the first non-empty help wins. A
  different type is a problem as above and the first type stands.
- **Duplicate series are refused**, within a call and across merged calls.
  This matters because Prometheus' own parsers do *not* refuse them: the
  server parser accepts `x 1\nx 2`, and even a second `# TYPE` line, and the
  duplicate sample is then dropped at ingestion.
- **Escaping** follows 0.0.4 exactly: label values escape `\`, `"` and line
  feed; HELP escapes `\` and line feed. Invalid UTF-8 in HELP is replaced by
  U+FFFD.
- **Numbers**: `+Inf`, `-Inf`, `NaN`; an integer of magnitude ≤ 2^53 in plain
  decimal (`1234567`, not `1.234567e+06` — byte counts stay greppable);
  anything else in Go's shortest round-tripping form (`'g', -1`). Both forms are
  what `strconv.ParseFloat`, and so every Prometheus parser, reads back to the
  identical `float64`.
- **Nothing is renamed.** Counter names conventionally end in `_total`; that is
  the author's choice to make, since a rename would break every query written
  against the name they chose. Values are not checked either.

### Built-in collectors

- `GoRuntime` — `go_goroutines`, `go_memstats_heap_alloc_bytes`,
  `go_memstats_sys_bytes` (gauges) and `go_gc_cycles_total` (counter), under
  the names `client_golang` uses so existing dashboards keep working. Read from
  `runtime/metrics`, **not** `runtime.ReadMemStats`, which stops the world on
  every call — once per scrape, per scraper, for the life of the process.
  `process_start_time_seconds` is left to the program, which alone knows when
  it considers itself started.
- `BuildInfo(prefix)` — `<prefix>_build_info{version,goversion,revision} 1`
  from `debug.ReadBuildInfo`, read once.

## Tests & validation

```sh
COVERPKG=$(go list ./... | paste -sd, -)
go test -race -coverpkg="$COVERPKG" -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1   # 100.0%

cd internal/promcheck && go test -race ./...
```

The library's own tests compare exact golden output and run every scrape
through a strict line-level checker (`strictCheck` in `writer_test.go`) that
also refuses a second `# TYPE` and interleaved families — each with a positive
control proving the checker can fail.

[`internal/promcheck`](internal/promcheck) is a **separate, test-only module**
that parses real scrapes with Prometheus' own parsers: the server's
(`prometheus/prometheus` `model/textparse`, chosen from the response's
`Content-Type` with no fallback, exactly as a scrape does) and the client
library's (`prometheus/common` `expfmt`, legacy 0.0.4 name validation). Every
tricky label value, help text and number — `+Inf`, `NaN`, `-0`, 2^53,
`MaxFloat64` — must read back bit-identical. Being its own module, its
dependencies never reach this module's `go.mod`; CI runs it in its own job, and
checks the library's dependency list is empty.

CI runs on Linux, macOS and Windows with the race detector and a 100% coverage
gate, plus the six 64-bit Go targets (amd64, arm64, riscv64, loong64, ppc64le,
s390x).

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-net-health/endpoint authors.
