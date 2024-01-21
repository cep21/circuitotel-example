module github.com/cep21/circuitotel-example

go 1.21.6

require (
	github.com/cep21/circuit/v4 v4.0.0
	github.com/cep21/circuitotel v0.0.0-20240121062959-d8c70ac5354c
	go.opentelemetry.io/otel v1.22.0
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v0.45.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.22.0
	go.opentelemetry.io/otel/sdk v1.22.0
	go.opentelemetry.io/otel/sdk/metric v1.22.0
	go.opentelemetry.io/otel/trace v1.22.0
)

require (
	github.com/go-logr/logr v1.4.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/otel/metric v1.22.0 // indirect
	golang.org/x/sys v0.16.0 // indirect
)
