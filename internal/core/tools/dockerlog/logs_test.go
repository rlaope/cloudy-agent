package dockerlog

import (
	"bytes"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

// muxFrame builds a Docker-multiplexed log stream: stdout payloads framed with
// stdcopy.Stdout, stderr payloads with stdcopy.Stderr. It is the same 8-byte
// header framing the daemon emits for non-TTY containers.
func muxFrame(t *testing.T, stdout, stderr string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if stdout != "" {
		if _, err := stdcopy.NewStdWriter(&buf, stdcopy.Stdout).Write([]byte(stdout)); err != nil {
			t.Fatalf("write stdout frame: %v", err)
		}
	}
	if stderr != "" {
		if _, err := stdcopy.NewStdWriter(&buf, stdcopy.Stderr).Write([]byte(stderr)); err != nil {
			t.Fatalf("write stderr frame: %v", err)
		}
	}
	return buf.Bytes()
}

func TestDemux_CombinesStdoutAndStderr(t *testing.T) {
	raw := muxFrame(t, "out line 1\nout line 2\n", "err line 1\n")
	text, err := demux(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	for _, want := range []string{"out line 1", "out line 2", "err line 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("demux output missing %q:\n%s", want, text)
		}
	}
}

func TestDemux_MalformedFrameReturnsPartial(t *testing.T) {
	// A valid frame followed by a full header carrying an unrecognized stream
	// type (only 0..3 are valid). StdCopy decodes the first frame, then errors
	// on the bad header — demux returns the decoded prefix AND the error so the
	// caller can still render a partial transcript.
	good := muxFrame(t, "good line\n", "")
	bad := []byte{0x09, 0, 0, 0, 0, 0, 0, 1, 'x'} // stream-type 9 is invalid
	raw := append(good, bad...)
	text, err := demux(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("expected an error on the unrecognized stream-type header")
	}
	if !strings.Contains(text, "good line") {
		t.Errorf("expected partial transcript to retain decoded frame, got:\n%s", text)
	}
}

// TestDemux_TruncatedTrailingFrameIsCleanEOF documents that StdCopy treats a
// truncated final frame as a clean EOF (no error), dropping the partial frame.
// demux therefore returns the fully-decoded prefix with a nil error — the
// expected, non-fatal behaviour for a log stream cut mid-write.
func TestDemux_TruncatedTrailingFrameIsCleanEOF(t *testing.T) {
	good := muxFrame(t, "good line\n", "")
	raw := append(good, 0x01, 0x00) // 2-byte dangling header < 8 bytes
	text, err := demux(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("a truncated trailing frame should be a clean EOF, got: %v", err)
	}
	if !strings.Contains(text, "good line") {
		t.Errorf("expected decoded prefix retained, got:\n%s", text)
	}
}

func TestCountErrorLines(t *testing.T) {
	text := strings.Join([]string{
		"all good here",
		"this is an ERROR message",
		"a normal info line",
		"FATAL: out of memory",
		"goroutine panic recovered",
		"level=error component=db",
		"",           // blank, never counts
		"errno is 5", // contains "err"
	}, "\n")
	if got := countErrorLines(text); got != 5 {
		t.Errorf("countErrorLines = %d, want 5\n%s", got, text)
	}
}

func TestIsErrorLine(t *testing.T) {
	cases := map[string]bool{
		"":                       false,
		"   ":                    false,
		"info: started":          false,
		"Error: bad":             true,
		"fatal crash":            true,
		"PANIC in handler":       true,
		"level=error msg=oops":   true,
		"recoverable err here":   true,
		"superb performance run": false,
	}
	for line, want := range cases {
		if got := isErrorLine(line); got != want {
			t.Errorf("isErrorLine(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestTailLines(t *testing.T) {
	text := "a\nb\nc\nd\ne\n"

	t.Run("limits to last n", func(t *testing.T) {
		got := tailLines(text, 2)
		if len(got) != 2 || got[0] != "d" || got[1] != "e" {
			t.Errorf("tailLines(_, 2) = %v, want [d e]", got)
		}
	})

	t.Run("n<=0 returns all", func(t *testing.T) {
		got := tailLines(text, 0)
		if len(got) != 5 {
			t.Errorf("tailLines(_, 0) = %v, want 5 lines", got)
		}
	})

	t.Run("n larger than line count returns all", func(t *testing.T) {
		got := tailLines(text, 100)
		if len(got) != 5 {
			t.Errorf("tailLines(_, 100) = %v, want 5 lines", got)
		}
	})

	t.Run("empty text yields no lines", func(t *testing.T) {
		if got := tailLines("", 10); got != nil {
			t.Errorf("tailLines(\"\", 10) = %v, want nil", got)
		}
	})
}

func TestRenderLogs(t *testing.T) {
	logs := []containerLog{
		{Name: "web-1", Lines: []string{"line a", "ERROR boom"}, ErrorCount: 1},
	}
	out := renderLogs("web", 1, logs, nil)
	if !strings.Contains(out, "1 container(s) for \"web\"") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "--- web-1 (2 line(s), 1 error line(s)) ---") {
		t.Errorf("missing per-container header:\n%s", out)
	}
	if !strings.Contains(out, "ERROR boom") {
		t.Errorf("missing log line:\n%s", out)
	}

	t.Run("zero matches", func(t *testing.T) {
		got := renderLogs("nope", 0, nil, nil)
		if !strings.Contains(got, "0 container(s)") || strings.Contains(got, "---") {
			t.Errorf("zero-match render unexpected:\n%s", got)
		}
	})

	t.Run("failure note", func(t *testing.T) {
		got := renderLogs("web", 2, logs, []string{"web-2: gone"})
		if !strings.Contains(got, "note: 1 container(s) failed: web-2: gone") {
			t.Errorf("missing failure note:\n%s", got)
		}
	})
}
