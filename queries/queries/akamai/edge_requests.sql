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
  AND (timestamp_min >= DATE_TRUNC ('minute', NOW() - toIntervalMinute ({{.OffsetStart}})))
  AND (timestamp_min < DATE_TRUNC ('minute', NOW() - toIntervalMinute ({{.OffsetEnd}})))
GROUP BY time, hostname, status_class, cache_status, cacheability
ORDER BY time ASC, hostname ASC, status_class ASC, cache_status ASC, cacheability ASC
FORMAT JSON;
