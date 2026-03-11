package cmd

import "testing"

func TestRootCmdExists(t *testing.T) {
	if rootCmd == nil {
		t.Fatal("rootCmd is nil")
	}
}
