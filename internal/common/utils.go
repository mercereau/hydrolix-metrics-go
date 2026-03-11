package common

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func GetEnvValue(name string) (string, error) {
	val := os.Getenv(name)
	if val == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	return val, nil
}

func SetEnv(vars map[string]string) error {
	for key, value := range vars {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("failed to set %s: %w", key, err)
		}
	}
	return nil
}

func GetStartEnd(offset int64) (start, end int64) {
	now := time.Now().UTC()
	end = int64(now.Truncate(time.Minute).Unix()) - offset
	start = end - 60
	return start, end
}

func MakeEnvMap(envvars, keys, defaults []string) map[string]string {
	envMap := make(map[string]string)
	for i, v := range envvars {
		val, err := GetEnvValue(v)
		if err != nil {
			envMap[keys[i]] = val
		} else {
			envMap[keys[i]] = defaults[i]
		}
	}
	return envMap
}

func NormalizeCacheStatus(v any) string {
	if isOne(v) {
		return "hit"
	}
	return "miss"
}

func isOne(v any) bool {
	switch t := v.(type) {
	case int:
		return t == 1
	case int64:
		return t == 1
	case float64:
		return t == 1
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i == 1
		}
		if f, err := t.Float64(); err == nil {
			return f == 1
		}
		return t.String() == "1"
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "1" || s == "hit"
	default:
		return false
	}
}
