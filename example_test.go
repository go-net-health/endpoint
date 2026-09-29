package endpoint_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"

	"github.com/go-net-health/endpoint"
)

func ExampleHandler() {
	var draining atomic.Bool
	var served atomic.Int64

	h := endpoint.Handler(endpoint.Options{
		Ready: func() error {
			if draining.Load() {
				return errors.New("draining")
			}
			return nil
		},
		Collectors: []endpoint.Collector{
			// endpoint.GoRuntime and endpoint.BuildInfo("myapp") go here too.
			func(w *endpoint.Writer) {
				w.Counter("myapp_requests_total", "Requests served.", endpoint.S(float64(served.Load())))
			},
		},
	})
	srv := httptest.NewServer(h) // in a program: http.ListenAndServe(":9090", h)
	defer srv.Close()

	get := func(path string) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			panic(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		fmt.Printf("%s %d %s", path, res.StatusCode, b)
	}
	served.Add(42)
	get("/healthz")
	get("/readyz")
	draining.Store(true)
	get("/readyz")
	get("/metrics")
	// Output:
	// /healthz 200 ok
	// /readyz 200 ok
	// /readyz 503 draining
	// /metrics 200 # HELP myapp_requests_total Requests served.
	// # TYPE myapp_requests_total counter
	// myapp_requests_total 42
}

func ExampleWriter() {
	var w endpoint.Writer
	w.Gauge("queue_length", "Jobs waiting, by queue.",
		endpoint.S(3, endpoint.L("queue", "thumbnails")),
		endpoint.S(0, endpoint.L("queue", `say "hi"`)))
	w.Gauge("queue_length", "", endpoint.S(1, endpoint.L("queue", "mail"))) // merged
	w.Counter("queue_length", "", endpoint.S(1))                            // wrong type: skipped
	w.WriteTo(os.Stdout)
	fmt.Println(w.Err())
	// Output:
	// # HELP queue_length Jobs waiting, by queue.
	// # TYPE queue_length gauge
	// queue_length{queue="thumbnails"} 3
	// queue_length{queue="say \"hi\""} 0
	// queue_length{queue="mail"} 1
	// # endpoint: skipped counter "queue_length": already written as a gauge
	// endpoint: skipped counter "queue_length": already written as a gauge
}
