package endpoint

import (
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

func TestGoRuntime(t *testing.T) {
	var w Writer
	GoRuntime(&w)
	out := render(t, &w)
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	for _, want := range []string{
		"# TYPE go_goroutines gauge\n",
		"# TYPE go_memstats_heap_alloc_bytes gauge\n",
		"# TYPE go_memstats_sys_bytes gauge\n",
		"# TYPE go_gc_cycles_total counter\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	m := regexp.MustCompile(`(?m)^go_goroutines (\d+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no go_goroutines sample in\n%s", out)
	}
	if n, _ := strconv.Atoi(m[1]); n < 1 {
		t.Errorf("go_goroutines = %d", n)
	}
	if strings.Contains(out, "process_start_time_seconds") {
		t.Error("process_start_time_seconds is the consumer's to write")
	}
	// The GC counter must move when the GC runs: it is read live, not cached.
	before := gcCycles(t, out)
	runtime.GC()
	var w2 Writer
	GoRuntime(&w2)
	if after := gcCycles(t, render(t, &w2)); after <= before {
		t.Errorf("go_gc_cycles_total %d -> %d across runtime.GC()", before, after)
	}
}

func gcCycles(t *testing.T, out string) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^go_gc_cycles_total (\d+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no go_gc_cycles_total in\n%s", out)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestGoRuntimeUnknownKey(t *testing.T) {
	saved := runtimeFamilies
	t.Cleanup(func() { runtimeFamilies = saved })
	runtimeFamilies = append(runtimeFamilies[:1:1], runtimeFamilies[0])
	runtimeFamilies[1].key = "/no/such/metric:things"
	runtimeFamilies[1].name = "go_unknown"
	var w Writer
	GoRuntime(&w)
	out := render(t, &w)
	if strings.Contains(out, "go_unknown") || !strings.Contains(out, "go_goroutines ") {
		t.Errorf("unknown key not skipped:\n%s", out)
	}
}

func TestBuildInfo(t *testing.T) {
	var w Writer
	BuildInfo("endpoint_test")(&w)
	out := render(t, &w)
	if w.Err() != nil {
		t.Fatal(w.Err())
	}
	re := regexp.MustCompile(`(?m)^endpoint_test_build_info\{version="[^"]*",goversion="go[^"]+",revision="[^"]*"\} 1$`)
	if !re.MatchString(out) || !strings.Contains(out, "# TYPE endpoint_test_build_info gauge\n") {
		t.Errorf("unexpected build info:\n%s", out)
	}
}

func fakeBuildInfo(t *testing.T, bi *debug.BuildInfo, ok bool) {
	saved := readBuildInfo
	t.Cleanup(func() { readBuildInfo = saved })
	readBuildInfo = func() (*debug.BuildInfo, bool) { return bi, ok }
}

func TestBuildInfoStamped(t *testing.T) {
	fakeBuildInfo(t, &debug.BuildInfo{
		GoVersion: "go1.99.1",
		Main:      debug.Module{Version: "v1.2.3"},
		Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "0123abc"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)
	var w Writer
	BuildInfo("app")(&w)
	want := `app_build_info{version="v1.2.3",goversion="go1.99.1",revision="0123abc"} 1`
	if out := render(t, &w); !strings.Contains(out, want+"\n") {
		t.Errorf("got\n%s\nwant %s", out, want)
	}
}

func TestBuildInfoMissing(t *testing.T) {
	fakeBuildInfo(t, nil, false)
	var w Writer
	BuildInfo("app")(&w)
	want := `app_build_info{version="",goversion="` + runtime.Version() + `",revision=""} 1`
	if out := render(t, &w); !strings.Contains(out, want+"\n") {
		t.Errorf("got\n%s\nwant %s", out, want)
	}
}

func TestBuildInfoNoGoVersion(t *testing.T) {
	fakeBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true)
	var w Writer
	BuildInfo("app")(&w)
	want := `app_build_info{version="(devel)",goversion="` + runtime.Version() + `",revision=""} 1`
	if out := render(t, &w); !strings.Contains(out, want+"\n") {
		t.Errorf("got\n%s\nwant %s", out, want)
	}
}

func TestBuildInfoInvalidPrefix(t *testing.T) {
	var w Writer
	BuildInfo("my-app")(&w)
	if out := render(t, &w); strings.Contains(out, "TYPE") || w.Err() == nil {
		t.Errorf("invalid prefix not refused: %v\n%s", w.Err(), out)
	}
}
