package cmd

import "testing"

// The --namespace flag is documented as an override, so a value supplied by the
// user must survive into the metric name. Only an empty value falls back to the
// default.
func TestResolveNamespace(t *testing.T) {
	tests := []struct {
		name string
		flag string
		want string
	}{
		{name: "flag value overrides the default", flag: "testns", want: "testns"},
		{name: "empty falls back to the default", flag: "", want: defaultNamespace},
		{name: "whitespace only falls back to the default", flag: "   ", want: defaultNamespace},
		{name: "surrounding whitespace is trimmed", flag: "  testns  ", want: "testns"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveNamespace(tt.flag); got != tt.want {
				t.Errorf("resolveNamespace(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}
