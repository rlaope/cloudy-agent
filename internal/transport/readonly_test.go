package transport

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadOnlyRoundTripper_AllowsGetHeadOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.Method)
	}))
	defer srv.Close()

	client := &http.Client{Transport: New(nil)}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req, err := http.NewRequest(method, srv.URL, nil)
		if err != nil {
			t.Fatalf("new request %s: %v", method, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do %s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("method %s: want 200, got %d", method, resp.StatusCode)
		}
	}
}

func TestReadOnlyRoundTripper_BlocksMutatingMethods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("inner transport must not be invoked for blocked methods")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: New(nil)}

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodTrace,
	} {
		req, err := http.NewRequest(method, srv.URL, strings.NewReader(""))
		if err != nil {
			t.Fatalf("new request %s: %v", method, err)
		}
		_, err = client.Do(req)
		if err == nil {
			t.Errorf("method %s: expected error, got nil", method)
			continue
		}
		if !errors.Is(err, ErrReadOnlyViolation) {
			t.Errorf("method %s: want ErrReadOnlyViolation, got %v", method, err)
		}
	}
}

func TestReadOnlyRoundTripper_RejectsNilRequest(t *testing.T) {
	rt := New(nil)
	if _, err := rt.RoundTrip(nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestWrap_ReturnsReadOnlyTransport(t *testing.T) {
	got := Wrap(http.DefaultTransport)
	if _, ok := got.(*ReadOnlyRoundTripper); !ok {
		t.Fatalf("Wrap should return *ReadOnlyRoundTripper, got %T", got)
	}
}
