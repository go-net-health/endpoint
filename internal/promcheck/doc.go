// Package promcheck checks the scrapes of github.com/go-net-health/endpoint
// with Prometheus' own parsers: the scraper's (prometheus/prometheus
// model/textparse, what a Prometheus server runs on every scrape) and the
// client library's (prometheus/common expfmt).
//
// It is a separate module so that those dependencies never reach the
// endpoint module's go.mod: endpoint stays standard library only. It has
// no code, only tests; CI runs them in their own job.
package promcheck
