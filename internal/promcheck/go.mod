module github.com/go-net-health/endpoint/internal/promcheck

go 1.27.1

require (
	github.com/go-net-health/endpoint v0.1.0
	github.com/prometheus/client_model v0.6.3
	github.com/prometheus/common v0.72.0
	github.com/prometheus/prometheus v0.315.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/grafana/regexp v0.0.0-20250905093917-f7b3be9d1853 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/go-net-health/endpoint => ../..
