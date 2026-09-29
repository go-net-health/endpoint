package endpoint

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	res := rec.Result()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestRoutes(t *testing.T) {
	h := Handler(Options{Collectors: []Collector{func(w *Writer) { w.Gauge("up", "", S(1)) }}})
	for _, c := range []struct {
		method, path string
		code         int
		body, ctype  string
	}{
		{"GET", "/healthz", 200, "ok\n", "text/plain; charset=utf-8"},
		{"HEAD", "/healthz", 200, "ok\n", "text/plain; charset=utf-8"},
		{"GET", "/readyz", 200, "ok\n", "text/plain; charset=utf-8"},
		{"GET", "/metrics", 200, "# TYPE up gauge\nup 1\n", MetricsContentType},
		{"HEAD", "/metrics", 200, "# TYPE up gauge\nup 1\n", MetricsContentType},
		{"POST", "/healthz", 405, "method not allowed\n", "text/plain; charset=utf-8"},
		{"PUT", "/readyz", 405, "method not allowed\n", "text/plain; charset=utf-8"},
		{"DELETE", "/metrics", 405, "method not allowed\n", "text/plain; charset=utf-8"},
		{"GET", "/", 404, "not found\n", "text/plain; charset=utf-8"},
		{"GET", "/healthz/", 404, "not found\n", "text/plain; charset=utf-8"},
		{"POST", "/elsewhere", 404, "not found\n", "text/plain; charset=utf-8"},
	} {
		res, body := do(t, h, c.method, c.path)
		if res.StatusCode != c.code || body != c.body || res.Header.Get("Content-Type") != c.ctype {
			t.Errorf("%s %s = %d %q (%s), want %d %q (%s)", c.method, c.path,
				res.StatusCode, body, res.Header.Get("Content-Type"), c.code, c.body, c.ctype)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: Cache-Control %q", c.method, c.path, res.Header.Get("Cache-Control"))
		}
		if c.code == 405 && res.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s: Allow %q", c.method, c.path, res.Header.Get("Allow"))
		}
	}
	if MetricsContentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("MetricsContentType = %q", MetricsContentType)
	}
}

func TestReadyz(t *testing.T) {
	var mu sync.Mutex
	var state error
	h := Handler(Options{Ready: func() error { mu.Lock(); defer mu.Unlock(); return state }})
	check := func(code int, body string) {
		t.Helper()
		if res, got := do(t, h, "GET", "/readyz"); res.StatusCode != code || got != body {
			t.Errorf("/readyz = %d %q, want %d %q", res.StatusCode, got, code, body)
		}
		// Liveness never depends on readiness.
		if res, _ := do(t, h, "GET", "/healthz"); res.StatusCode != 200 {
			t.Errorf("/healthz = %d while ready is %v", res.StatusCode, state)
		}
	}
	check(200, "ok\n")
	mu.Lock()
	state = errors.New("database unreachable")
	mu.Unlock()
	check(503, "database unreachable\n")
	mu.Lock()
	state = errors.New("already terminated\n")
	mu.Unlock()
	check(503, "already terminated\n")
	mu.Lock()
	state = nil
	mu.Unlock()
	check(200, "ok\n")
}

func TestReadyPanics(t *testing.T) {
	h := Handler(Options{Ready: func() error { panic("oops") }})
	res, body := do(t, h, "GET", "/readyz")
	if res.StatusCode != 500 || body != "readyz: Ready panicked: oops\n" {
		t.Errorf("/readyz = %d %q", res.StatusCode, body)
	}
}

func TestCollectorPanics(t *testing.T) {
	ok := func(w *Writer) { w.Gauge("up", "", S(1)) }
	bad := func(w *Writer) {
		w.Gauge("half", "", S(1))
		var m map[string]int
		m["x"] = 1 // a real runtime panic, not a string
	}
	h := Handler(Options{Collectors: []Collector{ok, bad}})
	res, body := do(t, h, "GET", "/metrics")
	if res.StatusCode != 500 ||
		!strings.HasPrefix(body, "metrics: collector 2 of 2 panicked, no scrape served: assignment to entry in nil map") ||
		strings.Contains(body, "up 1") || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("/metrics = %d %q", res.StatusCode, body)
	}
	// The handler survives and the other routes still answer.
	if res, _ := do(t, h, "GET", "/healthz"); res.StatusCode != 200 {
		t.Errorf("/healthz after a panic = %d", res.StatusCode)
	}
	// Positive control: the same handler without the bad collector serves.
	h = Handler(Options{Collectors: []Collector{ok}})
	if res, body := do(t, h, "GET", "/metrics"); res.StatusCode != 200 || body != "# TYPE up gauge\nup 1\n" {
		t.Errorf("control /metrics = %d %q", res.StatusCode, body)
	}
}

func TestCollectorPanicsNil(t *testing.T) {
	h := Handler(Options{Collectors: []Collector{func(*Writer) { panic(nil) }}})
	if res, body := do(t, h, "GET", "/metrics"); res.StatusCode != 500 || !strings.Contains(body, "panic called with nil") {
		t.Errorf("/metrics = %d %q", res.StatusCode, body)
	}
}

func TestPanicOverRealServer(t *testing.T) {
	// Through net/http itself: the connection is answered, not dropped.
	srv := httptest.NewServer(Handler(Options{Collectors: []Collector{func(*Writer) { panic("x") }}}))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 500 {
		t.Errorf("status %d", res.StatusCode)
	}
}

func TestNilCollectorsIgnoredAndOptionsCopied(t *testing.T) {
	cs := []Collector{nil, func(w *Writer) { w.Gauge("a", "", S(1)) }, nil}
	h := Handler(Options{Collectors: cs})
	cs[1] = func(w *Writer) { w.Gauge("changed", "", S(1)) }
	if res, body := do(t, h, "GET", "/metrics"); res.StatusCode != 200 || body != "# TYPE a gauge\na 1\n" {
		t.Errorf("/metrics = %d %q", res.StatusCode, body)
	}
}

func TestInvalidNameStillServes(t *testing.T) {
	h := Handler(Options{Collectors: []Collector{func(w *Writer) {
		w.Gauge("bad name", "", S(1))
		w.Gauge("good", "", S(1))
	}}})
	res, body := do(t, h, "GET", "/metrics")
	want := "# TYPE good gauge\ngood 1\n# endpoint: skipped gauge \"bad name\": invalid metric name\n"
	if res.StatusCode != 200 || body != want {
		t.Errorf("/metrics = %d %q", res.StatusCode, body)
	}
}

func TestConcurrentScrapes(t *testing.T) {
	h := Handler(Options{Collectors: []Collector{GoRuntime, BuildInfo("t")}})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
				if rec.Code != 200 || strictCheck(rec.Body.String()) != nil {
					t.Errorf("scrape %d: %v", rec.Code, strictCheck(rec.Body.String()))
					return
				}
			}
		})
	}
	wg.Wait()
}
