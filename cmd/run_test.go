package cmd

import (
	"path/filepath"
	"testing"
)

// A startup failure must surface as an error so cobra exits non-zero. Logging
// it and returning normally makes a dead process look like a clean shutdown to
// Docker, Kubernetes and systemd, none of which will restart it.
func TestRunHydrolixCollectorErrorsWhenConfigCannotBeLoaded(t *testing.T) {
	original := configPath
	t.Cleanup(func() { configPath = original })
	configPath = filepath.Join(t.TempDir(), "does-not-exist.yaml")

	if err := RunHydrolixCollector(); err == nil {
		t.Fatal("expected an error when the query config cannot be loaded, got nil")
	}
}
