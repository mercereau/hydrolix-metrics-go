package hydrolix

type MetricExtractor struct {
	Namespace string
	Subsystem string

	Query string
	Tags  []string
	KV    map[string]string
}

// FieldMeta describes the schema items listed in "meta".
type FieldMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Statistics mirrors "statistics".
type Statistics struct {
	Elapsed   float64 `json:"elapsed"`
	RowsRead  int64   `json:"rows_read"`
	BytesRead int64   `json:"bytes_read"`
}
