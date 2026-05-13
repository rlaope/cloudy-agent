package permission

import (
	"bytes"
	"testing"
)

func TestNewMasker_NilProfile(t *testing.T) {
	m, err := NewMasker(nil)
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if m != nil {
		t.Fatalf("want nil masker, got non-nil")
	}
}

func TestNewMasker_EmptyMasking(t *testing.T) {
	m, err := NewMasker(&Profile{})
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if m != nil {
		t.Fatalf("want nil masker for empty masking, got non-nil")
	}
}

func TestNewMasker_BadKeyRegex(t *testing.T) {
	_, err := NewMasker(&Profile{
		Masking: Masking{KeyRegex: []string{"[invalid"}},
	})
	if err == nil {
		t.Fatal("want error for bad key regex, got nil")
	}
}

func TestNewMasker_BadValueRegex(t *testing.T) {
	_, err := NewMasker(&Profile{
		Masking: Masking{ValueRegex: []string{"[invalid"}},
	})
	if err == nil {
		t.Fatal("want error for bad value regex, got nil")
	}
}

func TestMaskString_AWSAccessKey(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	input := "credentials: AKIAIOSFODNN7EXAMPLE and more text"
	got := m.MaskString(input)
	if got == input {
		t.Fatal("expected AWS access key to be redacted")
	}
	if bytes.Contains([]byte(got), []byte("AKIA")) {
		t.Fatalf("AKIA key still present in: %s", got)
	}
}

func TestMaskString_JWTPrefix(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	// A minimal JWT-like prefix (header.payload format).
	input := "token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.rest"
	got := m.MaskString(input)
	if bytes.Contains([]byte(got), []byte("eyJhbGci")) {
		t.Fatalf("JWT prefix still present in: %s", got)
	}
}

func TestMaskString_GitHubPAT(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	pat := "ghp_" + "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij" // 36 chars
	input := "export GITHUB_TOKEN=" + pat
	got := m.MaskString(input)
	if bytes.Contains([]byte(got), []byte(pat)) {
		t.Fatalf("GitHub PAT still present in: %s", got)
	}
}

func TestMaskString_NilMasker_NoOp(t *testing.T) {
	var m *Masker
	input := "AKIAIOSFODNN7EXAMPLE"
	if got := m.MaskString(input); got != input {
		t.Fatalf("nil masker must be a no-op, got %q", got)
	}
}

func TestMaskBytes_NilMasker_NoOp(t *testing.T) {
	var m *Masker
	b := []byte("AKIAIOSFODNN7EXAMPLE")
	if got := m.MaskBytes(b); !bytes.Equal(got, b) {
		t.Fatalf("nil masker must be a no-op")
	}
}

func TestMaskJSON_KeyMatch_Password(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	input := []byte(`{"username":"alice","password":"s3cr3t"}`)
	out, err := m.MaskJSON(input)
	if err != nil {
		t.Fatalf("MaskJSON: %v", err)
	}
	if bytes.Contains(out, []byte("s3cr3t")) {
		t.Fatalf("password value still present: %s", out)
	}
	if !bytes.Contains(out, []byte("[REDACTED]")) {
		t.Fatalf("expected [REDACTED] in output: %s", out)
	}
	// username must survive
	if !bytes.Contains(out, []byte("alice")) {
		t.Fatalf("non-secret key 'username' was wrongly redacted: %s", out)
	}
}

func TestMaskJSON_KeyMatch_ApiKey(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	input := []byte(`{"api_key":"abc123","endpoint":"https://example.com"}`)
	out, err := m.MaskJSON(input)
	if err != nil {
		t.Fatalf("MaskJSON: %v", err)
	}
	if bytes.Contains(out, []byte("abc123")) {
		t.Fatalf("api_key value still present: %s", out)
	}
	if !bytes.Contains(out, []byte("example.com")) {
		t.Fatalf("endpoint was wrongly redacted: %s", out)
	}
}

func TestMaskJSON_NonJSON_ReturnedUnchanged(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	input := []byte("this is plain text, not JSON")
	out, err := m.MaskJSON(input)
	if err != nil {
		t.Fatalf("MaskJSON on non-JSON must return nil error, got %v", err)
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("non-JSON must be returned unchanged; got %s", out)
	}
}

func TestMaskJSON_NilMasker_NoOp(t *testing.T) {
	var m *Masker
	input := []byte(`{"password":"secret"}`)
	out, err := m.MaskJSON(input)
	if err != nil {
		t.Fatalf("nil masker MaskJSON must not error: %v", err)
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("nil masker must be a no-op")
	}
}

func TestMaskMap_InPlace(t *testing.T) {
	m, err := NewMasker(&Profile{Masking: DefaultMaskingPatterns()})
	if err != nil {
		t.Fatalf("NewMasker: %v", err)
	}
	v := map[string]any{
		"password": "hunter2",
		"user":     "alice",
	}
	m.MaskMap(v)
	if v["password"] != "[REDACTED]" {
		t.Fatalf("password not redacted in map: %v", v["password"])
	}
	if v["user"] != "alice" {
		t.Fatalf("user was wrongly redacted")
	}
}

func TestDefaultMaskingPatterns_CompilesCleanly(t *testing.T) {
	p := DefaultMaskingPatterns()
	_, err := NewMasker(&Profile{Masking: p})
	if err != nil {
		t.Fatalf("DefaultMaskingPatterns must compile cleanly: %v", err)
	}
}
