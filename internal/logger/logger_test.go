package logger

import "testing"

func TestNewLogger(t *testing.T) {
	l := New(false)
	if l == nil {
		t.Fatalf("expected non-nil logger")
	}
	ld := New(true)
	if ld == nil {
		t.Fatalf("expected non-nil logger for verbose")
	}
}
