package ebpf

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// maxOutput caps the captured stdout+stderr from a single ebpf subprocess.
const maxOutput = 2 << 20 // 2 MiB

// runner is a function variable so tests can stub it without invoking real
// BCC/bpftrace binaries. The default executes the named binary with the
// given argv vector under ctx and returns stdout / stderr.
var runner = run

func run(ctx context.Context, bin string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout = trimTo(outBuf.Bytes(), maxOutput)
	stderr = trimTo(errBuf.Bytes(), maxOutput)
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			// SIGINT/SIGTERM-on-deadline is the normal end of a timed eBPF
			// session — the tool was supposed to be stopped after the
			// configured duration. Return stdout as success.
			if exitErr.ExitCode() < 0 && stdout != "" {
				return stdout, stderr, nil
			}
			return stdout, stderr, fmt.Errorf("%s exited %d: %s", bin, exitErr.ExitCode(), stderr)
		}
		return stdout, stderr, fmt.Errorf("exec %s: %w", bin, runErr)
	}
	return stdout, stderr, nil
}

func trimTo(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
