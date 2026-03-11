# hydrolix-collector

Minimal, production-ready CDN Metrics Collector using Cobra.

## Quickstart

```bash
CLI for collecting CDN Metrics from Hydrolix and exporting them to various metric sinks.

Usage:
  hydrolix-collector [flags]
  hydrolix-collector [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  version     Print version info

Flags:
  -C, --concurrency uint16             concurrency level for the exporter, how many simultaneous requests to make (default 1)
  -e, --environment string             environment that the exporter runs in (default "dev")
  -f, --flush int32                    how frequently the metric sinks should export metrics in seconds (default 15)
      --healthz string                 healthcheck endpoint path (default "/healthz")
  -h, --help                           help for hydrolix-collector
  -i, --interval int32                 polling interval in seconds for the exporter, how frequently the exporter polls (default 15)
  -n, --namespace string               override metric namespace, default uses command
  -S, --sink strings                   metrics sinks to enable, options: datadog, prometheus, otel
  -B, --sink-datadog-backoff int       [DataDog Sink] initial backoff time for retries (default 1)
  -b, --sink-datadog-batch-size int    [DataDog Sink] size of batch of custom metrics for a payload (default 200)
  -M, --sink-datadog-max-retries int   [DataDog Sink] max retries before failure to send custom metrics (default 3)
  -q, --sink-datadog-queue-size int    [DataDog Sink] size of payload queue for custom metrics (default 25)
  -o, --sink-otel-endpoint string      [OpenTelemetry Sink] comma-separated list of host:port for endpoint
  -m, --sink-prometheus-path string    [Prometheus Sink] path for endpoint (default "/metrics")
  -p, --sink-prometheus-port string    [Prometheus Sink] port for prometheus endpoint (default ":2112")
  -s, --subsystem string               provide a subsystem for the namespace (default "exporter")
  -v, --verbose                        enable verbose output

Use "hydrolix-collector [command] --help" for more information about a command.
```

## Authentication

The collector reads Hydrolix credentials from environment variables:

| Variable       | Description                                  |
|----------------|----------------------------------------------|
| `HDX_HOST`     | Hydrolix host (required)                     |
| `HDX_TOKEN`    | Bearer token (preferred)                     |
| `HDX_USERNAME` | Username (used if token not set)             |
| `HDX_PASSWORD` | Password (used if token not set)             |

## Build the Go Binary

```bash
make clean
make prepare
make lint
make test
make build
```

## Build and Run the Dockerfile

```bash
make docker-login
make docker-build
make docker-run
```

## Docker Compose

Spins up the collector (prometheus sink) + Prometheus + Grafana:

```bash
# Copy and fill in credentials
cp .env.example .env

docker compose up
```

| Service              | URL                     |
|----------------------|-------------------------|
| Prometheus           | http://localhost:9090   |
| Grafana              | http://localhost:3000   |
| Collector metrics    | http://localhost:2112/metrics |

## Features
- Cobra command structure (`root`, `version`)
- Verbose logging via `log/slog`
- ldflags-based versioning (version, commit, date)
- Makefile with build/test/format/vet
- Tiny test to keep `go test ./...` green

## Releasing
TODO: add Goreleaser, CI, etc.
