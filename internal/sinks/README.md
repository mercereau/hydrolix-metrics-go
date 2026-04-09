# sinks

This module provides **sink** implementations for exporting metrics to different backends.

A **sink** is the "last mile" output for your telemetry pipeline: it receives already-aggregated / already-labeled metrics and ships them to a destination.

Currently supported sinks:

- **Datadog** (push): batch + send series to Datadog Metrics API
- **Prometheus** (pull): expose `/metrics` for Prometheus to scrape
- **OpenTelemetry (OTel)** (push): export OTLP metrics to an OpenTelemetry Collector / backend

---

## Concepts

### What a sink does
A sink typically:
1. Accepts measurements (counters/gauges/histograms or “series”)
2. Buffers and/or aggregates (optional)
3. Flushes/export on an interval or on demand
4. Shuts down gracefully (flush in-flight data)

### Common knobs (recommended)
Regardless of sink type, you usually want:
- `enabled`: turn sink on/off
- `namespace` / `subsystem` (or metric prefixing rules)
- `base_tags` / `labels` / `resource_attributes`
- `flush_interval`
- `queue_size` (buffering)
- `timeout` + retries (push sinks)

---

## Datadog sink

### What it’s for
Use **Datadog** when you want to **push metrics** directly into Datadog and build dashboards/monitors there.

Typical characteristics:
- Buffers metric series on a channel/queue
- Periodically flushes/batches
- Sends via Datadog Metrics API (often v2 series payloads)
- Supports consistent tags across all metrics (env, service, cdn, hostname, etc.)

### Configuration (example)
> Rename fields to match your `DatadogOpts`.

### Env vars (example)
- `DD_API_KEY` (required)
- `DD_SITE` (optional, if supported by your code)
- Any tags/metadata you choose to source from env (e.g. `ENV`, `SERVICE`, `REGION`)

### Operational notes
- Prefer **batching** (size + time) to reduce API overhead.
- Decide how you handle backpressure:
  - block producers (strong delivery)
  - drop when full (protect the process)
- If you include timestamps, be consistent about “now” vs event-time.

---

## Prometheus sink

### What it’s for
Use **Prometheus** when you want a **scrape endpoint** that Prometheus (or Agent/Alloy) pulls from.

Typical characteristics:
- Registers counters/gauges/histograms in a Prometheus registry
- Serves an HTTP endpoint (commonly `/metrics`)
- Relies on Prometheus scrape + retention instead of push

### Prometheus scrape example
```yaml
scrape_configs:
  - job_name: "cdn-metrics"
    static_configs:
      - targets: ["your-host:9102"]
```

### Operational notes
- Prometheus is pull-based: if your service is down, scraping stops.
- Prefer stable label cardinality (avoid unbounded labels like raw URLs).
- Histograms can be expensive; use thoughtfully.

---

## OpenTelemetry (OTel) sink

### What it’s for
Use **OTel** when you want vendor-neutral export via **OTLP** to an OpenTelemetry Collector (or directly to a compatible backend).

Typical characteristics:
- Uses the OpenTelemetry SDK MeterProvider
- Exports periodically via OTLP (gRPC or HTTP/protobuf)
- Lets you attach resource attributes (service.name, deployment.environment, etc.)

### Collector example (high level)
- Your service exports OTLP → Collector
- Collector routes to your backend(s): Prometheus remote-write, Datadog, Splunk, etc.

### Operational notes
- Decide temporality (delta vs cumulative) if your implementation exposes the choice.
- If exporting to a collector, you get retries/backpressure handling in one place.
- Keep resource attributes stable and meaningful.

---

## Usage pattern

Your app typically selects one or more sinks from config:

1. Load config
2. Build enabled sinks
3. Start exporters/servers
4. Emit metrics
5. On shutdown: stop → flush → close

Pseudo-flow:
- `NewDatadogSink(opts)` / `NewPrometheusSink(opts)` / `NewOtelSink(opts)`
- `Start(ctx)` (if applicable)
- `Close(ctx)` or `Shutdown(ctx)` (flushes + stops)

---

## When to pick which sink?

- Pick **Datadog** if Datadog is your source of truth and you want “just push it there.”
- Pick **Prometheus** if you want a simple scrape endpoint and Prometheus-native workflows.
- Pick **OTel** if you want portability, a Collector layer, and/or multi-backend routing.

---

## Contributing

- Add a new sink under a clear package (e.g. `sinks/datadog`, `sinks/prometheus`, `sinks/otel`)
- Keep shutdown semantics consistent across sinks
- Provide:
  - unit tests (queueing, flush cadence, error handling)
  - an example config snippet
  - a small runnable example under `examples/`
