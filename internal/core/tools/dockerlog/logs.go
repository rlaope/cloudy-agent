package dockerlog

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/pkg/stdcopy"
)

// errTokens are the case-insensitive markers that classify a log line as an
// error for the summary count. "err" subsumes "error" so both are kept only
// for clarity of intent; matching is substring against the lower-cased line.
var errTokens = []string{"error", "err", "fatal", "panic", "level=error"}

// demux reads a Docker log stream and returns the combined stdout+stderr text.
// Docker frames stdout/stderr with an 8-byte header per chunk unless the
// container has a TTY (then the stream is raw). stdcopy.StdCopy handles the
// multiplexed framing; both destinations are written to the same builder so
// the result is a single interleaved-by-stream transcript. A malformed-frame
// error from StdCopy is tolerated: whatever was decoded before the error is
// returned with the error so the caller can still render a partial transcript.
func demux(r io.Reader) (string, error) {
	var combined strings.Builder
	// StdCopy writes stdout and stderr to the two writers; pointing both at the
	// same builder yields the merged transcript callers want.
	_, err := stdcopy.StdCopy(&combined, &combined, r)
	return combined.String(), err
}

// countErrorLines reports how many lines in text look like errors, matched
// case-insensitively against errTokens. Blank lines never count.
func countErrorLines(text string) int {
	n := 0
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if isErrorLine(sc.Text()) {
			n++
		}
	}
	return n
}

// isErrorLine reports whether line contains any error marker (case-insensitive).
func isErrorLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	lower := strings.ToLower(line)
	for _, tok := range errTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// tailLines returns the last n non-empty-trimmed transcript lines of text,
// preserving order. n <= 0 returns every line. Trailing blank lines produced
// by the stream's final newline are dropped so the count is meaningful.
func tailLines(text string, n int) []string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// containerLog is one matched container's decoded transcript plus its derived
// error count, ready for rendering.
type containerLog struct {
	Name       string
	Lines      []string
	ErrorCount int
}

// renderLogs formats the per-container transcripts. A header reports the match
// count; each container block names the container, its line count, and error
// count, followed by the tail-limited lines. Per-container decode failures are
// appended as a short note so a partial result is still actionable.
func renderLogs(workload string, matched int, logs []containerLog, failures []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d container(s) for %q\n", matched, workload)
	if matched == 0 {
		return strings.TrimRight(b.String(), "\n")
	}
	for _, l := range logs {
		fmt.Fprintf(&b, "--- %s (%d line(s), %d error line(s)) ---\n", l.Name, len(l.Lines), l.ErrorCount)
		for _, line := range l.Lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "note: %d container(s) failed: %s\n", len(failures), strings.Join(failures, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}
