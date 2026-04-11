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
  AND (timestamp_min >= DATE_TRUNC ('minute', NOW() - toIntervalMinute ({{.OffsetStart}})))
  AND (timestamp_min < DATE_TRUNC ('minute', NOW() - toIntervalMinute ({{.OffsetEnd}})))
GROUP BY time, hostname, status_class, cache_status
ORDER BY time ASC, hostname ASC, status_class ASC, cache_status ASC
FORMAT JSON;
