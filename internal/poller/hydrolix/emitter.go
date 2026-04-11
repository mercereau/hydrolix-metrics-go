package hydrolix

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/mercereau/hydrolix-metrics-go/internal/sinks"
)

// GenericResponse is the envelope returned by Hydrolix for any JSON-format query.
type GenericResponse struct {
	Meta       []FieldMeta        `json:"meta"`
	Data       []map[string]any   `json:"data"`
	Rows       int64              `json:"rows"`
	Statistics Statistics         `json:"statistics"`
}

// emitMetrics processes the rows from a generic query response according to
// the query config and emits metrics to the provided sinks.
func emitMetrics(q *QueryConfig, resp *GenericResponse, defaultTags map[string]string, ms sinks.MetricSinks) {
	queryTags := q.MergedTags(defaultTags)

	for _, row := range resp.Data {
		// Extract timestamp.
		tsRaw, ok := row[q.TimestampColumn]
		if !ok {
			slog.Warn("Row missing timestamp column", "query", q.Name, "column", q.TimestampColumn)
			continue
		}
		tsStr, ok := tsRaw.(string)
		if !ok {
			slog.Warn("Timestamp column is not a string", "query", q.Name, "value", tsRaw)
			continue
		}
		ts := convertTime(tsStr)
		scopedSinks := ms.WithTimestamp(ts)

		// Build tags from dimensions.
		rowTags := make(map[string]string, len(queryTags)+len(q.Dimensions))
		for k, v := range queryTags {
			rowTags[k] = v
		}
		skipRow := false
		for _, dim := range q.Dimensions {
			val, exists := row[dim.Column]
			if !exists || val == nil {
				if dim.Tag == q.TimestampColumn {
					continue
				}
				slog.Debug("Dimension column nil or missing", "query", q.Name, "column", dim.Column)
				continue
			}
			rowTags[dim.Tag] = formatValue(val, dim.Format)
		}
		if skipRow {
			continue
		}

		// Emit metrics.
		for _, mc := range q.Metrics {
			val, exists := row[mc.Column]
			if !exists || val == nil {
				continue
			}

			if mc.ArrayTag != "" {
				emitArrayMetric(scopedSinks, mc, val, rowTags)
			} else {
				emitScalarMetric(scopedSinks, mc, val, rowTags)
			}
		}
	}
}

// emitScalarMetric emits a single metric from a scalar value.
func emitScalarMetric(s sinks.MetricSink, mc MetricConfig, val any, tags map[string]string) {
	v, ok := toFloat64(val)
	if !ok {
		slog.Debug("Cannot convert metric value to float64", "column", mc.Column, "value", val)
		return
	}
	switch mc.Type {
	case "gauge":
		s.Gauge(mc.Name, mc.Unit, v, tags)
	case "counter":
		s.Inc(mc.Name, mc.Unit, v, tags)
	case "rate":
		s.Rate(mc.Name, mc.Unit, v, tags)
	default:
		s.Gauge(mc.Name, mc.Unit, v, tags)
	}
}

// emitArrayMetric emits one metric per element in an array column,
// adding the array_tag with the corresponding array_values label.
func emitArrayMetric(s sinks.MetricSink, mc MetricConfig, val any, baseTags map[string]string) {
	arr, ok := toFloat64Slice(val)
	if !ok {
		slog.Debug("Cannot convert array metric value", "column", mc.Column, "value", val)
		return
	}
	for i, v := range arr {
		if i >= len(mc.ArrayValues) {
			break
		}
		tags := make(map[string]string, len(baseTags)+1)
		for k, tv := range baseTags {
			tags[k] = tv
		}
		tags[mc.ArrayTag] = mc.ArrayValues[i]

		switch mc.Type {
		case "gauge":
			s.Gauge(mc.Name, mc.Unit, v, tags)
		case "counter":
			s.Inc(mc.Name, mc.Unit, v, tags)
		case "rate":
			s.Rate(mc.Name, mc.Unit, v, tags)
		default:
			s.Gauge(mc.Name, mc.Unit, v, tags)
		}
	}
}

// formatValue converts a value to a string, optionally using a format string.
func formatValue(val any, format string) string {
	if format != "" {
		switch v := val.(type) {
		case float64:
			return fmt.Sprintf(format, int64(v))
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return fmt.Sprintf(format, i)
			}
			return v.String()
		default:
			return fmt.Sprintf(format, v)
		}
	}
	return fmt.Sprintf("%v", val)
}

// toFloat64 converts a JSON-decoded value to float64.
func toFloat64(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// toFloat64Slice converts a JSON array to []float64.
func toFloat64Slice(val any) ([]float64, bool) {
	switch v := val.(type) {
	case []any:
		out := make([]float64, 0, len(v))
		for _, elem := range v {
			f, ok := toFloat64(elem)
			if !ok {
				return nil, false
			}
			out = append(out, f)
		}
		return out, true
	case []float64:
		return v, true
	default:
		return nil, false
	}
}

// convertTime parses a ClickHouse timestamp string to unix epoch seconds.
func convertTime(ts string) int64 {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		slog.Error("Failed to parse timestamp, using current time", "value", ts, "error", err)
		return time.Now().Unix()
	}
	return t.Unix()
}
