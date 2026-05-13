package py

import (
	"context"
	"encoding/json"
	"testing"
)

// withStubRunner replaces the package-level runner for tests.
func withStubRunner(t *testing.T, stub func(ctx context.Context, name string, args ...string) (string, string, error)) func() {
	t.Helper()
	orig := runner
	runner = stub
	return func() { runner = orig }
}

// ---- fixture strings ----

const spyDumpFixture = `
Process 1234: python app.py

Thread 140234567890 (active): "MainThread"
    compute (app.py:42)
    run (app.py:10)
    <module> (app.py:1)

Thread 140234567891 (idle): "worker-1"
    sleep (threading.py:300)
`

const spyTopTextFixture = `
  OwnTime  TotalTime  Function                                 [file:line]
  5.00%    10.00%     compute                                  app.py:42
  3.00%     7.00%     run                                      app.py:10
  1.00%     1.00%     <module>                                 app.py:1
`

const spyTopJSONFixture = `[
  {"function_name":"compute","filename":"app.py","line":42,"own_percent":5.0,"total_percent":10.0,"own_time":0.05,"total_time":0.10},
  {"function_name":"run","filename":"app.py","line":10,"own_percent":3.0,"total_percent":7.0,"own_time":0.03,"total_time":0.07}
]`

// ---- spy_dump tests ----

func TestSpyDump_ParsesThreads(t *testing.T) {
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		return spyDumpFixture, "", nil
	})
	defer restore()

	tool := NewSpyDumpTool()

	if !tool.ReadOnly() {
		t.Fatal("ReadOnly must return true")
	}
	if tool.Name() != "py.spy_dump" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"pid": 1234})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Text == "" {
		t.Fatal("expected non-empty Text")
	}

	// Fixture has 2 Thread lines.
	count := countThreads(spyDumpFixture)
	if count != 2 {
		t.Errorf("expected 2 threads, got %d", count)
	}
}

func TestSpyDump_InvalidPID(t *testing.T) {
	tool := NewSpyDumpTool()
	args, _ := json.Marshal(map[string]any{"pid": -1})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for pid=-1")
	}
}

// ---- spy_top tests ----

func TestSpyTop_JSONFormat(t *testing.T) {
	callCount := 0
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		callCount++
		// First call is JSON format — return JSON fixture.
		return spyTopJSONFixture, "", nil
	})
	defer restore()

	tool := NewSpyTopTool()

	if !tool.ReadOnly() {
		t.Fatal("ReadOnly must return true")
	}
	if tool.Name() != "py.spy_top_snapshot" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"pid": 42, "duration_seconds": 5})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	if len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(obs.Table.Rows))
	}
	if obs.Table.Rows[0][2] != "compute" {
		t.Errorf("expected function=compute, got %s", obs.Table.Rows[0][2])
	}
}

func TestSpyTop_TextFallback(t *testing.T) {
	callCount := 0
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		callCount++
		if callCount == 1 {
			// First call (JSON format) fails.
			return "", "unknown flag: --format", &ExitError{Cmd: "py-spy", Code: 1, Stderr: "unknown flag: --format"}
		}
		// Second call (text fallback) succeeds.
		return spyTopTextFixture, "", nil
	})
	defer restore()

	tool := NewSpyTopTool()
	args, _ := json.Marshal(map[string]any{"pid": 42})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	// Text fixture has 3 data rows.
	if len(obs.Table.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %v", len(obs.Table.Rows), obs.Table.Rows)
	}
}

func TestSpyTop_DurationCap(t *testing.T) {
	var capturedArgs []string
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		capturedArgs = args
		return spyTopJSONFixture, "", nil
	})
	defer restore()

	tool := NewSpyTopTool()
	// Request duration > 30 — should be capped at 30.
	args, _ := json.Marshal(map[string]any{"pid": 42, "duration_seconds": 999})
	_, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Find --duration value in capturedArgs.
	for i, a := range capturedArgs {
		if a == "--duration" && i+1 < len(capturedArgs) {
			if capturedArgs[i+1] != "30" {
				t.Errorf("expected duration capped at 30, got %s", capturedArgs[i+1])
			}
		}
	}
}
