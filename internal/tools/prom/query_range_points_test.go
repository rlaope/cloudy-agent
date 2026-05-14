package prom

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestQueryRange_RejectsExcessivePoints(t *testing.T) {
	tool := NewQueryRangeTool(map[string]*Client{"default": {}})

	// 30d / 1s = 2_592_000 points — far above the 11k cap.
	args, _ := json.Marshal(map[string]any{
		"endpoint": "default",
		"query":    "up",
		"start":    "2025-01-01T00:00:00Z",
		"end":      "2025-01-31T00:00:00Z",
		"step":     "1s",
	})

	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for excessive points, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds cap") {
		t.Errorf("expected cap error, got: %v", err)
	}
}

func TestQueryRange_RejectsReversedWindow(t *testing.T) {
	tool := NewQueryRangeTool(map[string]*Client{"default": {}})

	args, _ := json.Marshal(map[string]any{
		"endpoint": "default",
		"query":    "up",
		"start":    "2025-01-31T00:00:00Z",
		"end":      "2025-01-01T00:00:00Z",
		"step":     "1m",
	})

	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for reversed window, got nil")
	}
	if !strings.Contains(err.Error(), "must be after start") {
		t.Errorf("expected reversed-window error, got: %v", err)
	}
}
