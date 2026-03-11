package build

import "testing"

func TestBuildVarsExist(t *testing.T) {
	if Version == "" {
		t.Fatalf("Version should be set")
	}
	if Commit == "" {
		t.Fatalf("Commit should be set")
	}
	if Date == "" {
		t.Fatalf("Date should be set")
	}
}
