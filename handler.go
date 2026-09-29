package endpoint

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// A Collector adds its families to a scrape. It is called on every scrape,
// from the request goroutine; two scrapes running at once call it
// concurrently, so it must be safe for concurrent use.
type Collector func(*Writer)

// Options configures a Handler.
type Options struct {
	// Ready reports whether the process should receive traffic; nil means always ready.
	// /readyz answers 200 "ok\n" or 503 with the error text.
	Ready func() error
	// Collectors are called in order on every scrape of /metrics. Nil
	// entries are ignored.
	Collectors []Collector
}

// MetricsContentType is the Content-Type of a /metrics response.
const MetricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves GET and HEAD on three paths:
//
//   - /healthz always answers 200 "ok\n": liveness, the process can answer.
//   - /readyz answers 200 "ok\n" when Options.Ready returns nil (or is nil),
//     and 503 with the error text otherwise.
//   - /metrics runs every collector into a fresh Writer and answers 200 with
//     Content-Type MetricsContentType.
//
// Any other method on those paths is 405 with an Allow header; any other
// path is 404. Paths are matched exactly: mount the handler under a prefix
// with http.StripPrefix.
//
// A Collector, or Ready, that panics does not take the process down or
// drop the connection: the panic is recovered and that one request answers
// 500 with the panic value in the body. A scrape is all or nothing — a
// partial /metrics would make every series of the failed collector look
// absent rather than unknown.
//
// Every response carries Cache-Control: no-store, so no proxy answers a
// probe with a stale verdict.
func Handler(o Options) http.Handler {
	h := &handler{ready: o.Ready}
	for _, c := range o.Collectors {
		if c != nil {
			h.collectors = append(h.collectors, c)
		}
	}
	return h
}

type handler struct {
	ready      func() error
	collectors []Collector
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var serve func(http.ResponseWriter)
	switch r.URL.Path {
	case "/healthz":
		serve = func(w http.ResponseWriter) { reply(w, http.StatusOK, "", "ok\n") }
	case "/readyz":
		serve = h.readyz
	case "/metrics":
		serve = h.metrics
	default:
		reply(w, http.StatusNotFound, "", "not found\n")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		reply(w, http.StatusMethodNotAllowed, "", "method not allowed\n")
		return
	}
	serve(w)
}

func (h *handler) readyz(w http.ResponseWriter) {
	if h.ready == nil {
		reply(w, http.StatusOK, "", "ok\n")
		return
	}
	var err error
	if p := recovered(func() { err = h.ready() }); p != nil {
		reply(w, http.StatusInternalServerError, "", fmt.Sprintf("readyz: Ready panicked: %v\n", p))
		return
	}
	if err != nil {
		reply(w, http.StatusServiceUnavailable, "", strings.TrimSuffix(err.Error(), "\n")+"\n")
		return
	}
	reply(w, http.StatusOK, "", "ok\n")
}

func (h *handler) metrics(w http.ResponseWriter) {
	var mw Writer
	for i, c := range h.collectors {
		if p := recovered(func() { c(&mw) }); p != nil {
			reply(w, http.StatusInternalServerError, "",
				fmt.Sprintf("metrics: collector %d of %d panicked, no scrape served: %v\n", i+1, len(h.collectors), p))
			return
		}
	}
	var b strings.Builder
	mw.WriteTo(&b) // a strings.Builder never fails
	reply(w, http.StatusOK, MetricsContentType, b.String())
}

// recovered runs f and returns the value it panicked with, or nil.
func recovered(f func()) (p any) {
	defer func() { p = recover() }()
	f()
	return nil
}

func reply(w http.ResponseWriter, code int, contentType, body string) {
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	hd := w.Header()
	hd.Set("Content-Type", contentType)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body)) // a failed write means the client is gone
}
