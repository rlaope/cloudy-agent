package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/tools"
)

func TestLogSummaryHook_BelowThreshold_Untouched(t *testing.T) {
	h := NewLogSummaryHook(1024)
	obs := tools.Observation{Text: "small line\nanother line"}
	out, err := h.AfterToolCall(context.Background(),
		llm.ToolCall{Name: "log.loki_query_range", Arguments: json.RawMessage(`{}`)},
		obs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Text != obs.Text {
		t.Fatalf("text rewritten under threshold: %q", out.Text)
	}
}

func TestLogSummaryHook_NonLogTool_Untouched(t *testing.T) {
	h := NewLogSummaryHook(8)
	big := strings.Repeat("xxxxxxxxxxxxxxxx\n", 100)
	obs := tools.Observation{Text: big}
	out, _ := h.AfterToolCall(context.Background(),
		llm.ToolCall{Name: "k8s.list_pods", Arguments: json.RawMessage(`{}`)},
		obs, nil)
	if out.Text != big {
		t.Fatalf("non-log tool output rewritten")
	}
}

func TestLogSummaryHook_DisabledWhenBudgetZero(t *testing.T) {
	h := NewLogSummaryHook(0)
	big := strings.Repeat("a\n", 4096)
	obs := tools.Observation{Text: big}
	out, _ := h.AfterToolCall(context.Background(),
		llm.ToolCall{Name: "log.elasticsearch_query", Arguments: json.RawMessage(`{}`)},
		obs, nil)
	if out.Text != big {
		t.Fatalf("zero-budget hook rewrote output")
	}
}

func TestLogSummaryHook_PreservesExceptions(t *testing.T) {
	h := NewLogSummaryHook(512)
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("INFO routine ping\n")
	}
	sb.WriteString("ERROR java.lang.NullPointerException: payload was null\n")
	sb.WriteString("\tat com.example.Handler.process(Handler.java:42)\n")
	sb.WriteString("\tat com.example.Server.dispatch(Server.java:88)\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("INFO routine ping\n")
	}
	obs := tools.Observation{Text: sb.String()}
	out, _ := h.AfterToolCall(context.Background(),
		llm.ToolCall{Name: "log.loki_query_range", Arguments: json.RawMessage(`{}`)},
		obs, nil)
	if !strings.Contains(out.Text, "NullPointerException") {
		t.Fatalf("exception line dropped from summary:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "Handler.java:42") {
		t.Fatalf("stack-trace frame dropped from summary:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "log summary") {
		t.Fatalf("summary header missing")
	}
	if len(out.Text) >= len(obs.Text) {
		t.Fatalf("summary not smaller than original: %d vs %d", len(out.Text), len(obs.Text))
	}
}

func TestLogSummaryHook_ErrorPassesThrough(t *testing.T) {
	h := NewLogSummaryHook(8)
	big := strings.Repeat("Exception\n", 1000)
	obs := tools.Observation{Text: big}
	out, gotErr := h.AfterToolCall(context.Background(),
		llm.ToolCall{Name: "log.loki_query_range", Arguments: json.RawMessage(`{}`)},
		obs, errExample)
	if gotErr != nil {
		t.Fatalf("hook altered err: %v", gotErr)
	}
	if out.Text != big {
		t.Fatalf("hook rewrote text on errored call")
	}
}

func TestSummarizeLog_ClipsOnNewlineBoundary(t *testing.T) {
	text := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc\nException: boom\n at frame.A\n at frame.B\n"
	out := SummarizeLog(text, 80)
	if strings.Contains(out, "aaaaaaaaaaa") {
		t.Fatalf("head section spans line boundary: %s", out)
	}
}

// errExample is a sentinel used by TestLogSummaryHook_ErrorPassesThrough.
var errExample = errExampleType("synthetic upstream failure")

type errExampleType string

func (e errExampleType) Error() string { return string(e) }
