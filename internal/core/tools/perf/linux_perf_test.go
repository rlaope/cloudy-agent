package perf

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestLinuxPerfSupported_NonLinuxFails(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("only meaningful on non-linux hosts")
	}
	_, err := linuxPerfSupported()
	if err == nil {
		t.Errorf("expected error on %s, got nil", runtime.GOOS)
	}
	if !strings.Contains(err.Error(), "linux") {
		t.Errorf("expected linux in error, got %q", err.Error())
	}
}

func TestLinuxPerfRecordTool_StubbedRunnerPassesArgs(t *testing.T) {
	prev := perfRunner
	captured := struct {
		bin       string
		pid, dur  int
		freq      int
		ctxClosed bool
	}{}
	perfRunner = func(_ context.Context, bin string, pid, dur, freq int) (string, error) {
		captured.bin = bin
		captured.pid = pid
		captured.dur = dur
		captured.freq = freq
		return "    99.9%  /bin/foo  foo.go:42\n", nil
	}
	defer func() { perfRunner = prev }()

	tool := newLinuxPerfRecordTool("/usr/bin/perf")
	obs, err := tool.Run(context.Background(), []byte(`{"pid":42,"duration_seconds":7,"frequency_hz":250}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if captured.pid != 42 || captured.dur != 7 || captured.freq != 250 {
		t.Errorf("argv mismatch: %+v", captured)
	}
	if !strings.Contains(obs.Text, "99.9%") {
		t.Errorf("Text missing stubbed output: %q", obs.Text)
	}
}

func TestLinuxPerfRecordTool_RejectsInvalidPID(t *testing.T) {
	tool := newLinuxPerfRecordTool("/usr/bin/perf")
	if _, err := tool.Run(context.Background(), []byte(`{"pid":0}`)); err == nil {
		t.Error("expected error for pid=0")
	}
}
