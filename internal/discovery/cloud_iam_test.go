package discovery

import (
	"context"
	"errors"
	"testing"
)

// stubRunner returns a cloudProbeRunner replacement that maps argv[0] (the
// binary name) to a fixture function.
func stubRunner(t *testing.T, fixtures map[string]func() ([]byte, error)) func() {
	t.Helper()
	orig := cloudProbeRunner
	cloudProbeRunner = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		fn, ok := fixtures[name]
		if !ok {
			return nil, errors.New("not found")
		}
		return fn()
	}
	return func() { cloudProbeRunner = orig }
}

func TestProbeAWS_Success(t *testing.T) {
	restore := stubRunner(t, map[string]func() ([]byte, error){
		"aws": func() ([]byte, error) {
			return []byte(`{"UserId":"AID...","Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/alice"}`), nil
		},
	})
	defer restore()

	id := probeAWS(context.Background())
	if !id.Available {
		t.Fatalf("expected Available=true, got %+v", id)
	}
	if id.Account != "123456789012" {
		t.Errorf("account = %q, want 123456789012", id.Account)
	}
	if id.Principal != "arn:aws:iam::123456789012:user/alice" {
		t.Errorf("principal = %q", id.Principal)
	}
}

func TestProbeAWS_BinaryMissing(t *testing.T) {
	restore := stubRunner(t, map[string]func() ([]byte, error){
		"aws": func() ([]byte, error) { return nil, errors.New("exec: \"aws\": not found in $PATH") },
	})
	defer restore()

	id := probeAWS(context.Background())
	if id.Available {
		t.Error("missing binary should yield Available=false")
	}
	if id.Reason == "" {
		t.Error("expected Reason populated when binary missing")
	}
}

func TestProbeGCP_Success(t *testing.T) {
	restore := stubRunner(t, map[string]func() ([]byte, error){
		"gcloud": func() ([]byte, error) {
			return []byte(`{"core":{"account":"alice@example.com","project":"my-prj"}}`), nil
		},
	})
	defer restore()

	id := probeGCP(context.Background())
	if !id.Available {
		t.Fatalf("expected Available=true, got %+v", id)
	}
	if id.Principal != "alice@example.com" || id.Account != "my-prj" {
		t.Errorf("unexpected identity: %+v", id)
	}
}

func TestProbeGCP_NoActiveAccount(t *testing.T) {
	restore := stubRunner(t, map[string]func() ([]byte, error){
		"gcloud": func() ([]byte, error) { return []byte(`{"core":{}}`), nil },
	})
	defer restore()
	id := probeGCP(context.Background())
	if id.Available {
		t.Error("no active account should yield Available=false")
	}
}

func TestProbeAzure_Success(t *testing.T) {
	restore := stubRunner(t, map[string]func() ([]byte, error){
		"az": func() ([]byte, error) {
			return []byte(`{"id":"sub-uuid","user":{"name":"alice@example.com"}}`), nil
		},
	})
	defer restore()
	id := probeAzure(context.Background())
	if !id.Available {
		t.Fatalf("expected Available=true, got %+v", id)
	}
	if id.Account != "sub-uuid" {
		t.Errorf("account = %q", id.Account)
	}
}

func TestProbeCloudIdentities_StableOrder(t *testing.T) {
	restore := stubRunner(t, map[string]func() ([]byte, error){
		"aws":    func() ([]byte, error) { return nil, errors.New("no creds") },
		"gcloud": func() ([]byte, error) { return nil, errors.New("not installed") },
		"az":     func() ([]byte, error) { return nil, errors.New("no login") },
	})
	defer restore()

	got := ProbeCloudIdentities(context.Background())
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	wantOrder := []CloudProvider{CloudAWS, CloudGCP, CloudAzure}
	for i, w := range wantOrder {
		if got[i].Provider != w {
			t.Errorf("position %d: got %s, want %s", i, got[i].Provider, w)
		}
	}
}

func TestAvailableIdentities_FiltersUnavailable(t *testing.T) {
	in := []CloudIdentity{
		{Provider: CloudAWS, Available: true, Principal: "a"},
		{Provider: CloudGCP, Available: false, Reason: "x"},
		{Provider: CloudAzure, Available: true, Principal: "b"},
	}
	got := AvailableIdentities(in)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Provider != CloudAWS || got[1].Provider != CloudAzure {
		t.Errorf("filter dropped wrong rows: %+v", got)
	}
}
