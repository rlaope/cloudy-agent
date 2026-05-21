// Package selfupdate replaces the running cloudy binary with the latest
// GitHub release, on operator demand. Both `cloudy update` (CLI) and
// the `/update` slash command (TUI) call into Run.
//
// The strategy mirrors what install.sh does, but in-process:
//
//  1. Resolve the "latest" release tag through the GitHub REST API.
//     If it matches buildinfo.Version, return early — nothing to do.
//  2. Locate the running binary via os.Executable().
//  3. Download the matching cloudy-<goos>-<goarch> asset into a temp
//     file in the same directory as the current binary, so the final
//     rename stays on the same filesystem (cross-device os.Rename
//     fails with EXDEV).
//  4. Reject obvious HTML error pages (the failure mode when an
//     asset is missing) and ELF/Mach-O sanity-check the download.
//  5. chmod +x and os.Rename atomically over the current binary.
//
// On Unix this is safe even with the binary running: the kernel keeps
// the executing file's inode mapped until the process exits; new
// invocations get the replacement. Windows refuses to rename over an
// open .exe, so we detect runtime.GOOS == "windows" and return an
// error explaining the manual path.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/buildinfo"
)

const (
	// owner/repo where releases live. Promoted to a const so tests
	// can swap it without parsing the full URL string.
	releaseRepo = "rlaope/cloudy"

	// requestTimeout caps a single HTTP fetch (release lookup or
	// binary download). Long enough for a 50 MB binary on a slow
	// connection, short enough that a hung GitHub API will not
	// leave the user staring at a spinner forever.
	requestTimeout = 90 * time.Second
)

// Result is what Run returns to its caller.
type Result struct {
	// PreviousVersion is buildinfo.Version at the moment Run started.
	PreviousVersion string
	// LatestVersion is the tag fetched from GitHub.
	LatestVersion string
	// Replaced is true when the binary was actually swapped on disk;
	// false when the running version already matched latest.
	Replaced bool
	// InstalledPath is the os.Executable() target that was (or would
	// have been) replaced. Surfaced so callers can show it in the
	// success line.
	InstalledPath string
}

// Run downloads the latest release binary for the current OS+arch
// and replaces the executable that called it. Progress messages are
// written to w (caller passes os.Stdout for CLI, an in-memory writer
// or stream sink for TUI). Returns the resulting Result plus any
// error; on error the binary on disk is left untouched.
func Run(ctx context.Context, w io.Writer) (Result, error) {
	res := Result{PreviousVersion: buildinfo.Version}

	if runtime.GOOS == "windows" {
		return res, errors.New("self-update is not supported on Windows; download the release manually from " +
			"https://github.com/" + releaseRepo + "/releases/latest")
	}

	asset := fmt.Sprintf("cloudy-%s-%s", runtime.GOOS, runtime.GOARCH)

	fmt.Fprintln(w, "→ checking latest release on GitHub…")
	latest, err := fetchLatestTag(ctx)
	if err != nil {
		return res, fmt.Errorf("resolve latest tag: %w", err)
	}
	res.LatestVersion = latest

	if matches(buildinfo.Version, latest) {
		fmt.Fprintf(w, "✓ already on latest (%s); nothing to do.\n", latest)
		return res, nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return res, fmt.Errorf("locate running binary: %w", err)
	}
	// os.Executable returns a path that may include symlinks. Follow
	// them so we replace the real file rather than a symlink pointing
	// at the old binary.
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	res.InstalledPath = exePath

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		releaseRepo, latest, asset)
	fmt.Fprintf(w, "→ downloading %s\n", url)

	tmpPath, err := downloadAsset(ctx, url, exePath)
	if err != nil {
		return res, fmt.Errorf("download: %w", err)
	}
	// Best-effort cleanup if any step after this fails.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if err := validateBinary(tmpPath); err != nil {
		cleanup()
		return res, fmt.Errorf("validate %s: %w", asset, err)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return res, fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		cleanup()
		return res, fmt.Errorf("install over %s: %w", exePath, err)
	}

	res.Replaced = true
	fmt.Fprintf(w, "✓ replaced %s\n", exePath)
	fmt.Fprintf(w, "✓ %s → %s\n", res.PreviousVersion, latest)
	return res, nil
}

// fetchLatestTag asks the GitHub REST API for the latest release tag
// of the configured repo. Returns just the tag string ("v0.4.1").
func fetchLatestTag(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", releaseRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("github api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if payload.TagName == "" {
		return "", errors.New("github returned no tag_name (the repo may have zero releases yet)")
	}
	return payload.TagName, nil
}

// downloadAsset streams url into a fresh temp file next to dstExe.
// Keeping the temp file in the same directory as the eventual
// install target is what makes the final os.Rename atomic — across
// filesystems os.Rename fails with EXDEV. Returns the temp file's
// absolute path; caller is responsible for removing it on any
// failure path before the rename.
func downloadAsset(ctx context.Context, url, dstExe string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d (asset may be missing for this os/arch)", resp.StatusCode)
	}

	dir := filepath.Dir(dstExe)
	tmp, err := os.CreateTemp(dir, ".cloudy-update-*")
	if err != nil {
		return "", fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

// validateBinary refuses to swap in anything that is not at least
// plausibly an executable. Without this, GitHub's "asset not found"
// HTML error page would happily get chmod +x'd and renamed over the
// running binary, bricking the install.
func validateBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	// ELF magic (0x7f 'E' 'L' 'F') covers linux. Mach-O 64-bit magic
	// covers darwin amd64/arm64 (0xfeedfacf little-endian or
	// 0xcffaedfe big-endian). Any other byte sequence is rejected.
	switch {
	case head[0] == 0x7f && head[1] == 'E' && head[2] == 'L' && head[3] == 'F':
		return nil
	case head[0] == 0xcf && head[1] == 0xfa && head[2] == 0xed && head[3] == 0xfe:
		return nil
	case head[0] == 0xfe && head[1] == 0xed && head[2] == 0xfa && head[3] == 0xcf:
		return nil
	}
	return fmt.Errorf("downloaded file does not look like an ELF or Mach-O binary "+
		"(header: %x); the release may not have published a %s/%s asset",
		head, runtime.GOOS, runtime.GOARCH)
}

// matches treats "v0.4.1" and "0.4.1" as the same version so an
// unprefixed buildinfo.Version (set by `make build` for unreleased
// snapshots like "0.4.0-48-gfa752bc") still compares cleanly with
// the GitHub-style "v" prefix on tags.
func matches(local, remote string) bool {
	return strings.TrimPrefix(local, "v") == strings.TrimPrefix(remote, "v")
}
