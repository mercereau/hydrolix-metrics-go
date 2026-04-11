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
  AND (timestamp_min >= DATE_TRUNC ('minute', NOW() - toIntervalMinute ({{.OffsetStart}})))
  AND (timestamp_min < DATE_TRUNC ('minute', NOW() - toIntervalMinute ({{.OffsetEnd}})))
GROUP BY time, hostname, status_code, status_class, cache_status
ORDER BY time ASC, hostname ASC, status_class ASC, status_code ASC, cache_status ASC
FORMAT JSON;
