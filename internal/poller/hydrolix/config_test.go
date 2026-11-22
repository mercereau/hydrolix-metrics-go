package hydrolix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sql_file is resolved relative to the config file's directory, so a failure to
// read it must name the path that was actually searched. Reporting the value as
// written sends the reader looking in the wrong place.
func TestLoadConfigMissingSQLFileNamesSearchedPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "queries.yaml")
	config := "queries:\n  - name: demo\n    sql_file: sub/missing.sql\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(configPath, nil)
	if err == nil {
		t.Fatal("expected an error for an unreadable sql_file, got nil")
	}

	searched := filepath.Join(dir, "sub", "missing.sql")
	if !strings.Contains(err.Error(), searched) {
		t.Errorf("error should name the path searched (%s), got: %v", searched, err)
	}
}
