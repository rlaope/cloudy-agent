package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTokenTripper_SetsCustomScheme verifies the TokenEnv/TokenScheme path
// injects a verbatim Authorization header — PagerDuty's "Token token=<key>"
// scheme, which is neither Bearer nor Basic.
func TestTokenTripper_SetsCustomScheme(t *testing.T) {
	t.Setenv("CLOUDY_TEST_PD_TOKEN", "SECRET123")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := NewClient("pd", srv.URL, Auth{
		TokenEnv:    "CLOUDY_TEST_PD_TOKEN",
		TokenScheme: "Token token=",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.RawGet(context.Background(), "/incidents", nil); err != nil {
		t.Fatalf("RawGet: %v", err)
	}
	if gotAuth != "Token token=SECRET123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Token token=SECRET123")
	}
}

// TestTokenTripper_DefaultsToBearer verifies an empty TokenScheme falls back to
// the Bearer prefix.
func TestTokenTripper_DefaultsToBearer(t *testing.T) {
	t.Setenv("CLOUDY_TEST_TOKEN", "abc")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	c, _ := NewClient("x", srv.URL, Auth{TokenEnv: "CLOUDY_TEST_TOKEN"})
	_, _ = c.RawGet(context.Background(), "/", nil)
	if gotAuth != "Bearer abc" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer abc")
	}
}

// TestWrapAuth_EmptyTokenEnv_NoHeader verifies that an unset token env produces
// no Authorization header rather than a malformed one.
func TestWrapAuth_EmptyTokenEnv_NoHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	c, _ := NewClient("x", srv.URL, Auth{TokenEnv: "CLOUDY_TEST_UNSET_TOKEN", TokenScheme: "Token token="})
	_, _ = c.RawGet(context.Background(), "/", nil)
	if gotAuth != "" {
		t.Errorf("expected no Authorization header for unset token, got %q", gotAuth)
	}
}
