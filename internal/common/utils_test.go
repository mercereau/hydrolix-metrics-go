package common

import (
	"os"
	"testing"
)

func TestGetEnvValueAndSetEnv(t *testing.T) {
	key := "TEST_COMMON_FOO"
	os.Unsetenv(key)
	if _, err := GetEnvValue(key); err == nil {
		t.Fatalf("expected error when env not set")
	}
	if err := SetEnv(map[string]string{key: "bar"}); err != nil {
		t.Fatalf("SetEnv failed: %v", err)
	}
	val, err := GetEnvValue(key)
	if err != nil || val != "bar" {
		t.Fatalf("GetEnvValue returned %q, %v", val, err)
	}
}

func TestGetStartEnd(t *testing.T) {
	start, end := GetStartEnd(0)
	if end-start != 60 {
		t.Fatalf("expected end-start==60, got %d", end-start)
	}
}

func TestMakeEnvMapAndNormalizeCacheStatus(t *testing.T) {
	// Set env var referenced
	os.Setenv("FOO_ENV", "val1")
	defer os.Unsetenv("FOO_ENV")
	envvars := []string{"FOO_ENV", "BAR_ENV"}
	keys := []string{"k1", "k2"}
	defaults := []string{"d1", "d2"}
	m := MakeEnvMap(envvars, keys, defaults)
	if _, ok := m["k1"]; !ok {
		t.Fatalf("expected k1 present in map")
	}

	if NormalizeCacheStatus(1) != "hit" {
		t.Fatalf("expected hit for 1")
	}
	if NormalizeCacheStatus(0) != "miss" {
		t.Fatalf("expected miss for 0")
	}
}
