package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rlaope/cloudy/internal/tools"
)

type echoArgs struct {
	Msg string `json:"msg"`
}

func TestSpec_BuildAndRun(t *testing.T) {
	t.Parallel()
	tool := tools.Spec[echoArgs]{
		Name:        "test.echo",
		Description: "echo back",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Run: func(_ context.Context, a echoArgs) (tools.Observation, error) {
			return tools.Observation{Text: a.Msg}, nil
		},
	}.Build()

	if tool.Name() != "test.echo" {
		t.Fatalf("Name: %q", tool.Name())
	}
	if tool.Description() != "echo back" {
		t.Fatalf("Description: %q", tool.Description())
	}

	obs, err := tool.Run(context.Background(), json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if obs.Text != "hi" {
		t.Fatalf("expected hi, got %q", obs.Text)
	}
}

func TestSpec_RunWithEmptyArgs(t *testing.T) {
	t.Parallel()
	tool := tools.Spec[echoArgs]{
		Name: "test.echo",
		Run: func(_ context.Context, a echoArgs) (tools.Observation, error) {
			return tools.Observation{Text: "got:" + a.Msg}, nil
		},
	}.Build()

	obs, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if obs.Text != "got:" {
		t.Fatalf("expected got:, got %q", obs.Text)
	}
}

func TestSpec_BuildPanicsWithoutName(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	tools.Spec[echoArgs]{
		Run: func(_ context.Context, _ echoArgs) (tools.Observation, error) {
			return tools.Observation{}, nil
		},
	}.Build()
}

func TestSpec_BuildPanicsWithoutRun(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	tools.Spec[echoArgs]{Name: "x"}.Build()
}
