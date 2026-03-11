package hydrolix

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestSQLConstantsPresent(t *testing.T) {
	if SQLDescribe == "" {
		t.Fatalf("SQLDescribe should not be empty")
	}
	if SQLAkamaiEdgeRequests == "" {
		t.Fatalf("AkamaiSQLLastMinuteWithoutTime should not be empty")
	}
}

func TestConvertTimeValidAndInvalid(t *testing.T) {
	// Test valid timestamp - convertTime parses in UTC
	got := convertTime("2024-01-15 10:30:00")
	if got == 0 {
		t.Fatalf("expected valid unix time for valid input, got %d", got)
	}
	// Verify it's a reasonable timestamp (somewhere in 2024)
	if got < 1704067200 || got > 1735689600 {
		t.Fatalf("expected timestamp in 2024 range, got %d", got)
	}

	// Test invalid timestamp - should return current time (non-zero)
	invalidGot := convertTime("not-a-time")
	if invalidGot == 0 {
		t.Fatalf("expected non-zero fallback for invalid time, got %d", invalidGot)
	}
}

func TestUpdateTokenAndQueryWithTLSServer(t *testing.T) {
	// create a TLS test server to handle both login and query endpoints
	mux := http.NewServeMux()
	mux.HandleFunc("/config/v1/login/", func(w http.ResponseWriter, r *http.Request) {
		// respond with JSON token
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loginResponse{AccessToken: "test-token"})
	})
	mux.HandleFunc("/query/", func(w http.ResponseWriter, r *http.Request) {
		// echo back a small JSON body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	// Temporarily set the default transport to accept the test server's self-signed cert.
	// Save the original and restore on exit.
	orig := http.DefaultTransport
	if tr, ok := orig.(*http.Transport); ok {
		clone := tr.Clone()
		if clone.TLSClientConfig == nil {
			clone.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} else {
			clone.TLSClientConfig.InsecureSkipVerify = true
		}
		http.DefaultTransport = clone
		defer func() { http.DefaultTransport = orig }()
	}

	// Derive host portion (host:port) from server URL
	// server URL is like "https://127.0.0.1:XXXXX"
	// Use srv.Listener.Addr().String() which yields host:port
	host := srv.Listener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &Client{
		opts:       HydrolixOpts{Host: host, Username: "u", Password: "p"},
		httpClient: &http.Client{},
		ctx:        ctx,
	}

	// UpdateToken should succeed against our test server
	if err := c.UpdateToken(context.Background()); err != nil {
		t.Fatalf("UpdateToken failed: %v", err)
	}
	if c.token != "test-token" {
		t.Fatalf("expected token to be set to test-token, got %q", c.token)
	}

	// Query should call the /query/ endpoint and return the body
	body, err := c.Query("select 1")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if body == "" || body == "null" {
		t.Fatalf("unexpected empty body from Query: %q", body)
	}

	// ensure we can run under `go test -run` in CI by not leaving env changes
	_ = os.Setenv("TZ", "UTC")
}
