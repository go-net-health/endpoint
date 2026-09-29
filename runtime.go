package endpoint

import (
	"runtime"
	"runtime/debug"
	"runtime/metrics"
)

// runtimeFamilies maps runtime/metrics keys to the family names that
// prometheus/client_golang uses for the same quantities, so dashboards
// written against client_golang keep working.
var runtimeFamilies = []struct {
	key, name, help string
	write           func(w *Writer, name, help string, samples ...Sample)
}{
	{"/sched/goroutines:goroutines", "go_goroutines",
		"Number of goroutines that currently exist.", (*Writer).Gauge},
	{"/memory/classes/heap/objects:bytes", "go_memstats_heap_alloc_bytes",
		"Number of heap bytes allocated to objects and not yet freed.", (*Writer).Gauge},
	{"/memory/classes/total:bytes", "go_memstats_sys_bytes",
		"Number of bytes obtained from the system.", (*Writer).Gauge},
	{"/gc/cycles/total:gc-cycles", "go_gc_cycles_total",
		"Number of completed GC cycles.", (*Writer).Counter},
}

// GoRuntime is a Collector for go_goroutines, go_memstats_heap_alloc_bytes,
// go_memstats_sys_bytes and go_gc_cycles_total.
//
// The values come from runtime/metrics rather than runtime.ReadMemStats.
// ReadMemStats stops the world on every call — every goroutine of the
// process pauses so the runtime can take a consistent snapshot — and a
// scrape runs every few seconds from every scraper, for the lifetime of
// the process. runtime/metrics.Read reads these values without stopping
// the world, and is the interface the Go team maintains going forward.
//
// process_start_time_seconds is deliberately not here: only the program
// knows when it considers itself started. Write it from your own Collector.
func GoRuntime(w *Writer) {
	s := make([]metrics.Sample, len(runtimeFamilies))
	for i, f := range runtimeFamilies {
		s[i].Name = f.key
	}
	metrics.Read(s)
	for i, f := range runtimeFamilies {
		// A key this Go release does not know reads as KindBad: leave the
		// family out rather than report a made-up zero.
		if s[i].Value.Kind() != metrics.KindUint64 {
			continue
		}
		f.write(w, f.name, f.help, S(float64(s[i].Value.Uint64())))
	}
}

var readBuildInfo = debug.ReadBuildInfo

// BuildInfo returns a Collector for <prefix>_build_info{version,goversion,revision} 1
// from debug.ReadBuildInfo.
//
// version is the main module's version ("(devel)" for a build that is not
// from a tagged module), goversion the Go release that built the binary,
// and revision the vcs.revision stamped by the go command — empty when the
// build was not made from a VCS checkout, which Prometheus reads as the
// label being absent. The build information is read once, when BuildInfo
// is called. An invalid prefix makes every scrape skip the family, as for
// any invalid name.
func BuildInfo(prefix string) Collector {
	version, goversion, revision := "", runtime.Version(), ""
	if bi, ok := readBuildInfo(); ok {
		version = bi.Main.Version
		if bi.GoVersion != "" {
			goversion = bi.GoVersion
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				revision = s.Value
			}
		}
	}
	name := prefix + "_build_info"
	help := "A metric with a constant 1 value labeled by the version, Go version and revision " + prefix + " was built from."
	sample := S(1, L("version", version), L("goversion", goversion), L("revision", revision))
	return func(w *Writer) { w.Gauge(name, help, sample) }
}
