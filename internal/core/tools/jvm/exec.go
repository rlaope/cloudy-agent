// Package jvm provides read-only JVM diagnostic tools for the cloudy SRE agent.
// All tools operate on a local PID (LOCAL-ATTACHED mode). In-cluster
// sidecar/ephemeral-container flows are deferred to v0.2.
package jvm

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// maxOutputBytes caps the combined stdout+stderr captured from any subprocess.
const maxOutputBytes = 1 << 20 // 1 MiB

// ExitError is returned when a subprocess exits with a non-zero status.
type ExitError struct {
	Cmd    string
	Stderr string
	Code   int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("jvm: %s exited %d: %s", e.Cmd, e.Code, e.Stderr)
}

// limitWriter drops writes once n bytes have been accepted.
type limitWriter struct {
	buf *bytes.Buffer
	n   int
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.n <= 0 {
		return len(p), nil // silently discard
	}
	if len(p) > lw.n {
		p = p[:lw.n]
	}
	n, err := lw.buf.Write(p)
	lw.n -= n
	return len(p), err // report original len so cmd doesn't error
}

// runner is a function variable so tests can stub it out.
var runner = run

// run executes name with args under ctx, capturing stdout and stderr.
// Combined output is capped at maxOutputBytes. A non-zero exit code returns
// *ExitError so callers can inspect stderr separately.
func run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, name, args...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &limitWriter{buf: &outBuf, n: maxOutputBytes}
	cmd.Stderr = &limitWriter{buf: &errBuf, n: maxOutputBytes}

	runErr := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return stdout, stderr, &ExitError{
				Cmd:    name,
				Stderr: stderr,
				Code:   exitErr.ExitCode(),
			}
		}
		return stdout, stderr, fmt.Errorf("jvm: exec %s: %w", name, runErr)
	}
	return stdout, stderr, nil
}
