package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifySHA256_Match pins the headline contract: a matching digest
// produces no error. Without this the L-4 fix (v0.5 security review)
// could regress silently.
func TestVerifySHA256_Match(t *testing.T) {
	// "abc" — SHA-256("abc") canonical value, see RFC 6234 §8.5.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	dir := t.TempDir()
	path := filepath.Join(dir, "asset")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifySHA256(path, want); err != nil {
		t.Errorf("matching digest must verify; got %v", err)
	}
	// Uppercase expected hex must also verify (we lowercase internally).
	if err := verifySHA256(path, strings.ToUpper(want)); err != nil {
		t.Errorf("uppercase expected digest must verify; got %v", err)
	}
}

// TestVerifySHA256_Mismatch confirms a corrupted/swapped asset is
// rejected before the rename — the whole point of the verification step.
func TestVerifySHA256_Mismatch(t *testing.T) {
	const fake = "0000000000000000000000000000000000000000000000000000000000000000"
	dir := t.TempDir()
	path := filepath.Join(dir, "asset")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifySHA256(path, fake)
	if err == nil {
		t.Fatal("mismatched digest must error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should mention 'mismatch'; got: %v", err)
	}
}

// TestFetchSHA256_ShasumFormat exercises the canonical `shasum -a 256`
// output shape: "<hex>  <filename>". This is exactly what
// .github/workflows/release.yml ships per asset.
func TestFetchSHA256_ShasumFormat(t *testing.T) {
	const wantDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(wantDigest + "  cloudy-darwin-arm64\n"))
	}))
	defer srv.Close()

	got, err := fetchSHA256(context.Background(), srv.URL+"/x.sha256")
	if err != nil {
		t.Fatalf("fetchSHA256: %v", err)
	}
	if got != wantDigest {
		t.Errorf("digest mismatch: got %q want %q", got, wantDigest)
	}
}

// TestFetchSHA256_BareDigest accepts a stripped 64-char hex line — the
// shape an operator might publish if they edited the manifest by hand.
func TestFetchSHA256_BareDigest(t *testing.T) {
	const wantDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(wantDigest + "\n"))
	}))
	defer srv.Close()

	got, err := fetchSHA256(context.Background(), srv.URL+"/x.sha256")
	if err != nil {
		t.Fatalf("fetchSHA256: %v", err)
	}
	if got != wantDigest {
		t.Errorf("digest mismatch: got %q want %q", got, wantDigest)
	}
}

// TestFetchSHA256_RejectsTruncated guards against a corrupted manifest
// containing only the first half of the digest — silently accepting it
// would defeat the whole verification step.
func TestFetchSHA256_RejectsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ba7816bf8f01cfea\n"))
	}))
	defer srv.Close()

	_, err := fetchSHA256(context.Background(), srv.URL+"/x.sha256")
	if err == nil {
		t.Fatal("truncated digest must error")
	}
}

// TestFetchSHA256_Rejects404 confirms a missing companion file is a
// hard failure: the binary should not be installed if the integrity
// witness was not published alongside it.
func TestFetchSHA256_Rejects404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchSHA256(context.Background(), srv.URL+"/missing.sha256")
	if err == nil {
		t.Fatal("404 on .sha256 must error")
	}
}
