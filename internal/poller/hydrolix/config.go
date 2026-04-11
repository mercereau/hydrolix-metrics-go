package hydrolix

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"

	"go.yaml.in/yaml/v2"
)

// QueriesConfig is the top-level YAML configuration.
type QueriesConfig struct {
	Defaults QueryDefaults `yaml:"defaults"`
	Queries  []QueryConfig `yaml:"queries"`
}

// QueryDefaults are inherited by all queries unless overridden.
type QueryDefaults struct {
	OffsetStartMinutes int               `yaml:"offset_start_minutes"`
	OffsetEndMinutes   int               `yaml:"offset_end_minutes"`
	Tags               map[string]string `yaml:"tags"`
}

// QueryConfig defines a single SQL query and how to map its results to metrics.
type QueryConfig struct {
	Name            string            `yaml:"name"`
	SQL             string            `yaml:"sql"`      // inline SQL
	SQLFile         string            `yaml:"sql_file"` // path to .sql file (relative to config file)
	TimestampColumn string            `yaml:"timestamp_column"`
	Tags            map[string]string `yaml:"tags"`
	Dimensions      []DimensionConfig `yaml:"dimensions"`
	Metrics         []MetricConfig    `yaml:"metrics"`

	// Per-query offset overrides (nil = inherit from defaults).
	OffsetStartMinutes *int `yaml:"offset_start_minutes"`
	OffsetEndMinutes   *int `yaml:"offset_end_minutes"`

	// Resolved at load time.
	resolvedSQL         string
	resolvedOffsetStart int
	resolvedOffsetEnd   int
	renderedSQL         string
}

// DimensionConfig maps a SQL column to a metric tag.
type DimensionConfig struct {
	Column string `yaml:"column"`
	Tag    string `yaml:"tag"`
	Format string `yaml:"format"` // optional, e.g. "%d" for numeric columns
}

// MetricConfig maps a SQL column to a metric emission.
type MetricConfig struct {
	Column      string   `yaml:"column"`
	Name        string   `yaml:"name"`
	Unit        string   `yaml:"unit"`
	Type        string   `yaml:"type"` // gauge, counter, rate
	ArrayTag    string   `yaml:"array_tag"`
	ArrayValues []string `yaml:"array_values"`
}

// templateContext is passed to SQL templates.
type templateContext struct {
	OffsetStart int
	OffsetEnd   int
}

// LoadConfig loads query configuration from an external path, or falls back
// to the embedded defaults if configPath is empty. The embeddedFS should be
// the configs/* embed from the project root.
func LoadConfig(configPath string, embeddedFS fs.FS) (*QueriesConfig, error) {
	if configPath != "" {
		return loadFromDisk(configPath)
	}
	return loadFromEmbed(embeddedFS)
}

func loadFromEmbed(embeddedFS fs.FS) (*QueriesConfig, error) {
	data, err := fs.ReadFile(embeddedFS, "queries/queries.yaml")
	if err != nil {
		return nil, fmt.Errorf("read embedded config: %w", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, err
	}
	return resolveConfig(cfg, embeddedFS, "queries")
}

func loadFromDisk(configPath string) (*QueriesConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", configPath, err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(configPath)
	return resolveConfig(cfg, os.DirFS(baseDir), ".")
}

func parseConfig(data []byte) (*QueriesConfig, error) {
	var cfg QueriesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config YAML: %w", err)
	}
	// Apply hardcoded fallbacks if defaults are zero.
	if cfg.Defaults.OffsetStartMinutes == 0 {
		cfg.Defaults.OffsetStartMinutes = 6
	}
	if cfg.Defaults.OffsetEndMinutes == 0 {
		cfg.Defaults.OffsetEndMinutes = 1
	}
	return &cfg, nil
}

// resolveConfig resolves SQL file references and renders templates for each query.
func resolveConfig(cfg *QueriesConfig, fsys fs.FS, baseDir string) (*QueriesConfig, error) {
	for i := range cfg.Queries {
		q := &cfg.Queries[i]

		// Resolve offsets.
		q.resolvedOffsetStart = cfg.Defaults.OffsetStartMinutes
		if q.OffsetStartMinutes != nil {
			q.resolvedOffsetStart = *q.OffsetStartMinutes
		}
		q.resolvedOffsetEnd = cfg.Defaults.OffsetEndMinutes
		if q.OffsetEndMinutes != nil {
			q.resolvedOffsetEnd = *q.OffsetEndMinutes
		}

		// Default timestamp column.
		if q.TimestampColumn == "" {
			q.TimestampColumn = "time"
		}

		// Resolve SQL: inline or from file.
		if q.SQL != "" {
			q.resolvedSQL = q.SQL
		} else if q.SQLFile != "" {
			path := filepath.Join(baseDir, q.SQLFile)
			data, err := fs.ReadFile(fsys, path)
			if err != nil {
				return nil, fmt.Errorf("query %q: read sql_file %s: %w", q.Name, q.SQLFile, err)
			}
			q.resolvedSQL = string(data)
		} else {
			return nil, fmt.Errorf("query %q: must specify either 'sql' or 'sql_file'", q.Name)
		}

		// Render Go template placeholders.
		tmpl, err := template.New(q.Name).Parse(q.resolvedSQL)
		if err != nil {
			return nil, fmt.Errorf("query %q: parse SQL template: %w", q.Name, err)
		}
		var buf []byte
		w := &byteWriter{buf: &buf}
		err = tmpl.Execute(w, templateContext{
			OffsetStart: q.resolvedOffsetStart,
			OffsetEnd:   q.resolvedOffsetEnd,
		})
		if err != nil {
			return nil, fmt.Errorf("query %q: render SQL template: %w", q.Name, err)
		}
		q.renderedSQL = string(buf)
	}
	return cfg, nil
}

// renderQuerySQL re-renders the SQL template for a query using its resolved offsets.
func renderQuerySQL(q *QueryConfig) error {
	tmpl, err := template.New(q.Name).Parse(q.resolvedSQL)
	if err != nil {
		return fmt.Errorf("query %q: parse SQL template: %w", q.Name, err)
	}
	var buf []byte
	w := &byteWriter{buf: &buf}
	err = tmpl.Execute(w, templateContext{
		OffsetStart: q.resolvedOffsetStart,
		OffsetEnd:   q.resolvedOffsetEnd,
	})
	if err != nil {
		return fmt.Errorf("query %q: render SQL template: %w", q.Name, err)
	}
	q.renderedSQL = string(buf)
	return nil
}

// RenderedSQL returns the fully resolved SQL for a query.
func (q *QueryConfig) RenderedSQL() string {
	return q.renderedSQL
}

// ResolvedOffsetStart returns the effective offset start for this query.
func (q *QueryConfig) ResolvedOffsetStart() int {
	return q.resolvedOffsetStart
}

// ResolvedOffsetEnd returns the effective offset end for this query.
func (q *QueryConfig) ResolvedOffsetEnd() int {
	return q.resolvedOffsetEnd
}

// MergedTags returns the query's tags merged on top of the default tags.
func (q *QueryConfig) MergedTags(defaults map[string]string) map[string]string {
	out := make(map[string]string, len(defaults)+len(q.Tags))
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range q.Tags {
		out[k] = v
	}
	return out
}

// byteWriter is a minimal io.Writer that appends to a byte slice.
type byteWriter struct {
	buf *[]byte
}

func (w *byteWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}
