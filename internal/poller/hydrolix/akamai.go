package hydrolix


const SQLDescribe = `
DESCRIBE akamai.edge_origin_performance_1minute;
`

// | Column Name | Description | Type |
// |-------------|-------------|------|
// | timestamp_min | Request timestamp truncated to the minute | Dimension |
// | reqHost | Request hostname/domain | Dimension |
// | country | Geographic country of the request | Dimension |
// | is_cached | Flag indicating if the response was served from cache | Dimension |
// | cacheability | Flag indicating if the content is cacheable | Dimension |
// | statusCode | HTTP response status code | Dimension |
// | statusClass | HTTP status code class (2xx, 3xx, 4xx, 5xx) | Dimension |
// | cnt_all | Total count of all requests | Metric |
// | quantiles_transferTimeMSec | Percentiles (p25, p50, p75, p90, p95, p99) of transfer time in ms | Metric |
// | quantiles_turnAroundTimeMSec | Percentiles of turnaround time in ms | Metric |
// | quantiles_timeToFirstByte | Percentiles of time to first byte (TTFB) | Metric |
// | quantiles_downloadTime | Percentiles of download time | Metric |
// | quantiles_tlsOverheadTimeMSec | Percentiles of TLS handshake overhead in ms | Metric |
// | quantiles_overheadBytes | Percentiles of overhead bytes | Metric |
// | quantiles_compressionRatio | Percentiles of compression ratio (origin size / uncompressed size) | Metric |
// | edge_requests | Total edge requests (cache hits + cache misses) | Metric |
// | origin_requests | Count of requests that went to origin (not cached) | Metric |
// | cnt_edgeMiss | Count of cache misses at the edge | Metric |
// | cnt_edgeHits | Count of cache hits at the edge | Metric |
// | sum_originBytes | Total bytes transferred from origin | Metric |
// | sum_edgeBytes | Total bytes transferred from edge cache | Metric |
// | sum_originTransferTimeMSec | Total transfer time for origin requests in ms | Metric |
// | sum_edgeTransferTimeMSec | Total transfer time for edge requests in ms | Metric |
// | origin_throughput | Calculated origin throughput (bytes/sec) | Metric |
// | edge_throughput | Calculated edge throughput (bytes/sec) | Metric |
// | cnt_originErrors | Count of origin errors (status ≥ 400, cache miss) | Metric |
// | cnt_edgeErrors | Count of edge errors (status ≥ 400, cache hit) | Metric |

// EdgeRequestResponse is the top-level envelope.
type AkamaiEdgeRequestsResponse struct {
	Meta       []FieldMeta            `json:"meta"`
	Data       []AkamaiEdgeRequestRow `json:"data"`
	Rows       int64                  `json:"rows"`
	Statistics Statistics             `json:"statistics"`
}

// AkamaiEdgeRequestRow matches each object in "data".
// Use pointers for Nullable(...) fields so JSON nulls decode cleanly.
type AkamaiEdgeRequestRow struct {
	TimeMinute         *string  `json:"time"`
	Hostname           *string  `json:"hostname"`              // Nullable(String)
	StatusClass        *string  `json:"status_class"`          // Nullable(String)
	CacheStatus        *uint8   `json:"cache_status"`          // Nullable(UInt8)
	Cacheability       *uint8   `json:"cacheability"`          // Nullable(UInt8)
	EdgeRequestTotal   *float64 `json:"edge_requests_total"`   // Float64
	EdgeBytesTotal     *float64 `json:"edge_bytes_total"`      // Float64
	OriginRequestTotal *float64 `json:"origin_requests_total"` // Float64
	OriginBytesTotal   *float64 `json:"origin_bytes_total"`    // Float64
}

var SQLAkamaiEdgeRequests = `
SELECT
  timestamp_min AS time,
  reqHost AS hostname,
  statusClass as status_class,
  is_cached as cache_status,
  cacheability,
  CAST(IFNULL(edge_requests, 0) AS DECIMAL) as edge_requests_total,
  CAST(IFNULL(sum_edgeBytes, 0) AS DECIMAL) as edge_bytes_total,
  CAST(IFNULL(origin_requests, 0) AS DECIMAL) as origin_requests_total,
  CAST(IFNULL(sum_originBytes, 0) AS DECIMAL) as origin_bytes_total
FROM akamai.edge_origin_performance_1minute
WHERE (reqHost NOT IN(''))
  AND (timestamp_min >= DATE_TRUNC ('minute', NOW() - toIntervalMinute (%d)))
  AND (timestamp_min < DATE_TRUNC ('minute', NOW() - toIntervalMinute (%d)))
GROUP BY time, hostname, status_class, cache_status, cacheability
ORDER BY time ASC, hostname ASC, status_class ASC, cache_status ASC, cacheability ASC
FORMAT JSON;
`


// EdgeRequestResponse is the top-level envelope.
type AkamaiEdgeQuantilesResponse struct {
	Meta       []FieldMeta              `json:"meta"`
	Data       []AkamaiEdgeQuantilesRow `json:"data"`
	Rows       int64                    `json:"rows"`
	Statistics Statistics               `json:"statistics"`
}

// EdgeRequestRow matches each object in "data".
// Use pointers for Nullable(...) fields so JSON nulls decode cleanly.
type AkamaiEdgeQuantilesRow struct {
	TimeMinute       *string   `json:"time"`
	Hostname         *string   `json:"hostname"`           // Nullable(String)
	StatusClass      *string   `json:"status_class"`       // Nullable(String)
	CacheStatus      *uint8    `json:"cache_status"`       // Nullable(UInt8)
	CompressionRatio []float64 `json:"compression_ratio"`  // Float64
	OverheadBytes    []float64 `json:"overhead_bytes"`     // Float64
	TransferSecs     []float64 `json:"transfer_seconds"`   // Float64
	FirstByteSecs    []float64 `json:"first_byte_seconds"` // Float64
	DownloadSecs     []float64 `json:"download_seconds"`   // Float64
	TurnaroundSecs   []float64 `json:"turnaround_seconds"` // Float64
	TLSOverheadSecs  []float64 `json:"tls_seconds"`        // Float64
}

var SQLAkamaiQuantiles = `
SELECT 
  timestamp_min AS time,
  reqHost AS hostname,
  statusClass as status_class,
  is_cached as cache_status,

  IFNULL(quantiles_compressionRatio, 0) AS compression_ratio,
  quantiles_overheadBytes as overhead_bytes, 
  quantiles_timeToFirstByte/1000 AS first_byte_seconds,
  quantiles_tlsOverheadTimeMSec/1000 AS tls_seconds,
  quantiles_transferTimeMSec/1000 AS transfer_seconds,
  quantiles_downloadTime/1000 AS download_seconds,
  quantiles_turnAroundTimeMSec/1000 AS turnaround_seconds
FROM akamai.edge_origin_performance_1minute
WHERE (reqHost NOT IN(''))
  AND (timestamp_min >= DATE_TRUNC ('minute', NOW() - toIntervalMinute (%d)))
  AND (timestamp_min < DATE_TRUNC ('minute', NOW() - toIntervalMinute (%d)))
GROUP BY time, hostname, status_class, cache_status
ORDER BY time ASC, hostname ASC, status_class ASC, cache_status ASC
FORMAT JSON;
`

// AkamaiErrorsResponse is the top-level envelope.
type AkamaiErrorsResponse struct {
	Meta       []FieldMeta       `json:"meta"`
	Data       []AkamaiErrorsRow `json:"data"`
	Rows       int64             `json:"rows"`
	Statistics Statistics        `json:"statistics"`
}

// EdgeRequestRow matches each object in "data".
// Use pointers for Nullable(...) fields so JSON nulls decode cleanly.
type AkamaiErrorsRow struct {
	TimeMinute        *string  `json:"time"`
	Hostname          *string  `json:"hostname"`      // Nullable(String)
	StatusClass       *string  `json:"status_class"`  // Nullable(String)
	StatusCode        *uint16  `json:"status_code"`   // Nullable(String)
	CacheStatus       *uint8   `json:"cache_status"`  // Nullable(UInt8)
	EdgeErrorsTotal   *float64 `json:"edge_errors"`   // Float64
	OriginErrorsTotal *float64 `json:"origin_errors"` // Float64
}

var SQLAkamaiErrors = `
SELECT 
  timestamp_min AS time,
  reqHost AS hostname,
  statusClass as status_class,
  statusCode as status_code,
  is_cached as cache_status,
  CAST(IFNULL(cnt_edgeErrors, 0) AS DECIMAL) AS edge_errors,
  CAST(IFNULL(cnt_originErrors, 0) AS DECIMAL) AS origin_errors
FROM akamai.edge_origin_performance_1minute
WHERE (reqHost NOT IN(''))
  AND (timestamp_min >= DATE_TRUNC ('minute', NOW() - toIntervalMinute (%d)))
  AND (timestamp_min < DATE_TRUNC ('minute', NOW() - toIntervalMinute (%d)))
GROUP BY time, hostname, status_code, status_class, cache_status
ORDER BY time ASC, hostname ASC, status_class ASC, status_code ASC, cache_status ASC
FORMAT JSON;
`

