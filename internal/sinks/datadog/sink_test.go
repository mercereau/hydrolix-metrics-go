package datadog

import (
	"encoding/json"
	"strings"
	"testing"

	ddv2 "github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

func TestCreateMetricAndFlattenTags(t *testing.T) {
	tags := sinks.Tags{"env": "prod", "zone": "eu"}
	ts := int64(1234567890)
	m := CreateMetric(ddv2.METRICINTAKETYPE_GAUGE, "my.metric", "ms", ts, 3.14, tags)

	if m.Metric != "my.metric" {
		t.Fatalf("unexpected metric name: %q", m.Metric)
	}
	if m.Unit == nil || *m.Unit != "ms" {
		t.Fatalf("unexpected unit: %#v", m.Unit)
	}
	if len(m.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(m.Points))
	}
	if m.Points[0].Timestamp == nil || *m.Points[0].Timestamp != ts {
		t.Fatalf("timestamp mismatch: %#v", m.Points[0].Timestamp)
	}
	if m.Points[0].Value == nil || *m.Points[0].Value != 3.14 {
		t.Fatalf("value mismatch: %#v", m.Points[0].Value)
	}

	flat := flattenTags(tags)
	// order not guaranteed; check both keys present
	joined := strings.Join(flat, ",")
	if !strings.Contains(joined, "env:prod") || !strings.Contains(joined, "zone:eu") {
		t.Fatalf("flattened tags missing entries: %v", flat)
	}
}

func TestSeriesListString_JSON(t *testing.T) {
	tags := sinks.Tags{"k": "v"}
	s := SeriesList{}
	// create a series and append
	m := CreateMetric(ddv2.METRICINTAKETYPE_COUNT, "s.test", "c", 1, 2.0, tags)
	s = append(s, m)

	out := s.String()
	// Should be valid JSON containing the metric name
	var parsed []ddv2.MetricSeries
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v; out=%s", err, out)
	}
	if len(parsed) != 1 || parsed[0].Metric != "s.test" {
		t.Fatalf("unexpected parsed content: %#v", parsed)
	}
}
