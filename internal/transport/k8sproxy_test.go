package transport

import (
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestServiceProxy_URL(t *testing.T) {
	p := &ServiceProxy{apiHost: "https://10.0.0.1:6443"}

	tests := []struct {
		name      string
		ns        string
		svc       string
		scheme    string
		port      string
		path      string
		wantSufx  string // expected URL suffix (checked via strings.HasSuffix)
		wantFull  string // exact full URL when set
		wantEmpty bool   // expect empty string return
	}{
		{
			name:     "http scheme numeric port with path",
			ns:       "default",
			svc:      "prometheus",
			scheme:   "http",
			port:     "9090",
			path:     "/api/v1/labels",
			wantSufx: "/api/v1/namespaces/default/services/http:prometheus:9090/proxy/api/v1/labels",
		},
		{
			name:     "https scheme named port",
			ns:       "monitoring",
			svc:      "tempo",
			scheme:   "https",
			port:     "https",
			path:     "/api/traces",
			wantSufx: "/api/v1/namespaces/monitoring/services/https:tempo:https/proxy/api/traces",
		},
		{
			name:     "empty scheme omits prefix",
			ns:       "default",
			svc:      "loki",
			scheme:   "",
			port:     "3100",
			path:     "/loki/api/v1/query",
			wantSufx: "/api/v1/namespaces/default/services/loki:3100/proxy/loki/api/v1/query",
		},
		{
			name:     "path without leading slash gets one prepended",
			ns:       "default",
			svc:      "jaeger",
			scheme:   "http",
			port:     "16686",
			path:     "api/services",
			wantSufx: "/api/v1/namespaces/default/services/http:jaeger:16686/proxy/api/services",
		},
		{
			name:     "empty path produces no trailing slash",
			ns:       "kube-system",
			svc:      "pprof",
			scheme:   "http",
			port:     "6060",
			path:     "",
			wantFull: "https://10.0.0.1:6443/api/v1/namespaces/kube-system/services/http:pprof:6060/proxy",
		},
		{
			// Path-traversal attempt in namespace: '/' in segment → empty string.
			name:      "slash in ns is rejected",
			ns:        "a/b",
			svc:       "prometheus",
			scheme:    "http",
			port:      "9090",
			path:      "/metrics",
			wantEmpty: true,
		},
		{
			// Path-traversal attempt in svc name.
			name:      "slash in svc is rejected",
			ns:        "default",
			svc:       "prom/evil",
			scheme:    "http",
			port:      "9090",
			path:      "/metrics",
			wantEmpty: true,
		},
		{
			// Path-traversal attempt in scheme.
			name:      "slash in scheme is rejected",
			ns:        "default",
			svc:       "svc",
			scheme:    "ht/tp",
			port:      "80",
			path:      "/",
			wantEmpty: true,
		},
		{
			// Path-traversal attempt in port.
			name:      "slash in port is rejected",
			ns:        "default",
			svc:       "svc",
			scheme:    "http",
			port:      "90/90",
			path:      "/metrics",
			wantEmpty: true,
		},
		{
			// Special characters in svc name that are not '/' are path-escaped.
			name:     "special chars in svc are escaped",
			ns:       "default",
			svc:      "my svc",
			scheme:   "http",
			port:     "8080",
			path:     "/health",
			wantSufx: "/api/v1/namespaces/default/services/http:my%20svc:8080/proxy/health",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.URL(tc.ns, tc.svc, tc.scheme, tc.port, tc.path)

			if tc.wantEmpty {
				if got != "" {
					t.Errorf("want empty string, got %q", got)
				}
				return
			}

			if got == "" {
				t.Fatalf("got empty string, want non-empty URL")
			}

			if tc.wantFull != "" {
				if got != tc.wantFull {
					t.Errorf("full URL mismatch:\n got  %q\n want %q", got, tc.wantFull)
				}
				return
			}

			if !strings.HasSuffix(got, tc.wantSufx) {
				t.Errorf("URL suffix mismatch:\n got  %q\n want suffix %q", got, tc.wantSufx)
			}
		})
	}
}

func TestServiceProxy_URL_ContainsAPIHost(t *testing.T) {
	p := &ServiceProxy{apiHost: "https://192.168.1.100:6443"}
	got := p.URL("default", "prometheus", "http", "9090", "/metrics")
	if !strings.HasPrefix(got, "https://192.168.1.100:6443") {
		t.Errorf("URL should start with apiHost, got %q", got)
	}
}

func TestNewServiceProxy_NilConfig(t *testing.T) {
	_, err := NewServiceProxy(nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

func TestNewServiceProxy_ValidConfig(t *testing.T) {
	cfg := &rest.Config{
		Host:        "https://127.0.0.1:6443",
		BearerToken: "test-token",
	}
	sp, err := NewServiceProxy(cfg)
	if err != nil {
		t.Fatalf("NewServiceProxy: %v", err)
	}
	if sp.HTTPClient() == nil {
		t.Fatal("HTTPClient() must not be nil")
	}
	if sp.apiHost != "https://127.0.0.1:6443" {
		t.Errorf("apiHost mismatch: got %q", sp.apiHost)
	}
}

func TestNewServiceProxy_TrimsTrailingSlash(t *testing.T) {
	cfg := &rest.Config{
		Host:        "https://127.0.0.1:6443/",
		BearerToken: "tok",
	}
	sp, err := NewServiceProxy(cfg)
	if err != nil {
		t.Fatalf("NewServiceProxy: %v", err)
	}
	if strings.HasSuffix(sp.apiHost, "/") {
		t.Errorf("apiHost should not have trailing slash, got %q", sp.apiHost)
	}
}

func TestNewServiceProxy_HTTPClientIsReadOnly(t *testing.T) {
	cfg := &rest.Config{
		Host:        "https://127.0.0.1:6443",
		BearerToken: "tok",
	}
	sp, err := NewServiceProxy(cfg)
	if err != nil {
		t.Fatalf("NewServiceProxy: %v", err)
	}
	client := sp.HTTPClient()
	if _, ok := client.Transport.(*ReadOnlyRoundTripper); !ok {
		t.Errorf("HTTPClient transport must be *ReadOnlyRoundTripper, got %T", client.Transport)
	}
}
