package jvm

import (
	"context"
	"encoding/json"
	"testing"
)

// stubRunner replaces the package-level runner for tests.
func withStubRunner(t *testing.T, stub func(ctx context.Context, name string, args ...string) (string, string, error)) func() {
	t.Helper()
	orig := runner
	runner = stub
	return func() { runner = orig }
}

// ---- fixture strings ----

const heapInfoFixture = `
 garbage-first heap   total 262144K, used 51200K [0x00000006c0000000, 0x00000007c0000000)
  region size 1024K, 50 young (51200K), 1 survivors (1024K)
 Metaspace       used 28672K, committed 29184K, reserved 1114112K
  class space    used 3584K, committed 3840K, reserved 1048576K
`

const histogramFixture = `
 num     #instances         #bytes  class name (module)
-------------------------------------------------------
   1:         12345       9876543  [B (java.base@17.0.3)
   2:          5678       3456789  java.lang.String (java.base@17.0.3)
   3:          1234        567890  java.util.HashMap$Node (java.base@17.0.3)
Total        123456      98765432
`

const threadDumpFixture = `
2023-01-01 00:00:00
Full thread dump OpenJDK 64-Bit Server VM (17.0.3 mixed mode):

"main" #1 prio=5 os_prio=0 tid=0x... nid=0x1 waiting on condition [0x...]
   java.lang.Thread.State: TIMED_WAITING (sleeping)

"GC Thread#0" os_prio=0 tid=0x... nid=0x2 runnable
   java.lang.Thread.State: RUNNABLE

"Reference Handler" #2 daemon prio=10 os_prio=0 tid=0x...
   java.lang.Thread.State: WAITING (on object monitor)

"Finalizer" #3 daemon prio=8 os_prio=0 tid=0x...
   java.lang.Thread.State: WAITING (on object monitor)

"Signal Dispatcher" #4 daemon prio=9 os_prio=0 tid=0x...
   java.lang.Thread.State: RUNNABLE

"worker-1" #10 prio=5 os_prio=0 tid=0x...
   java.lang.Thread.State: BLOCKED (on object monitor)

Found one Java-level deadlock:
=============================
"thread-A":
  waiting to lock monitor 0x... (object 0x..., a java.lang.Object),
  which is held by "thread-B"
`

const jstatFixture = `S0C    S1C    S0U    S1U      EC       EU        OC         OU       MC     MU    CCSC   CCSU   YGC     YGCT    FGC    FGCT     GCT
0.0   1024.0  0.0   1024.0 52224.0  8192.0  131072.0   51200.0  29184.0 28672.0 3840.0 3584.0    100    2.345     2    0.567    2.912
0.0   1024.0  0.0   1024.0 52224.0  9216.0  131072.0   51200.0  29184.0 28672.0 3840.0 3584.0    101    2.400     2    0.567    2.967
`

// ---- jcmd_gc tests ----

func TestJcmdGC_ParsesHistogram(t *testing.T) {
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		if len(args) >= 2 && args[1] == "GC.heap_info" {
			return heapInfoFixture, "", nil
		}
		if len(args) >= 2 && args[1] == "GC.class_histogram" {
			return histogramFixture, "", nil
		}
		return "", "", nil
	})
	defer restore()

	tool := NewJcmdGCTool()

	if tool.Name() != "jvm.jcmd_gc" {
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
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	// histogramFixture has 3 data rows (excluding header, separator, Total)
	if len(obs.Table.Rows) != 3 {
		t.Fatalf("expected 3 histogram rows, got %d", len(obs.Table.Rows))
	}
	if obs.Table.Rows[0][3] != "[B (java.base@17.0.3)" {
		t.Errorf("unexpected class name: %q", obs.Table.Rows[0][3])
	}
}

func TestJcmdGC_InvalidPID(t *testing.T) {
	tool := NewJcmdGCTool()
	args, _ := json.Marshal(map[string]any{"pid": 0})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for pid=0")
	}
}

// ---- jcmd_thread tests ----

func TestJcmdThread_ParsesStates(t *testing.T) {
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		return threadDumpFixture, "", nil
	})
	defer restore()

	tool := NewJcmdThreadTool()

	if tool.Name() != "jvm.jcmd_thread_dump" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"pid": 42})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	counts, deadlock := parseThreadDump(threadDumpFixture)
	if counts["RUNNABLE"] != 2 {
		t.Errorf("expected RUNNABLE=2, got %d", counts["RUNNABLE"])
	}
	if counts["WAITING"] != 2 {
		t.Errorf("expected WAITING=2, got %d", counts["WAITING"])
	}
	if counts["TIMED_WAITING"] != 1 {
		t.Errorf("expected TIMED_WAITING=1, got %d", counts["TIMED_WAITING"])
	}
	if counts["BLOCKED"] != 1 {
		t.Errorf("expected BLOCKED=1, got %d", counts["BLOCKED"])
	}
	if deadlock == "" {
		t.Error("expected deadlock section to be captured")
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
}

// ---- jstat_gc tests ----

func TestJstatGC_ParsesTable(t *testing.T) {
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		return jstatFixture, "", nil
	})
	defer restore()

	tool := NewJstatGCTool()

	if tool.Name() != "jvm.jstat_gc" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"pid": 42})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	// jstatFixture has 1 header row + 2 data rows
	if len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 data rows, got %d", len(obs.Table.Rows))
	}
	if len(obs.Table.Headers) == 0 {
		t.Fatal("expected headers to be parsed")
	}
}

func TestJstatGC_DefaultsAndCaps(t *testing.T) {
	var capturedArgs []string
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		capturedArgs = args
		return jstatFixture, "", nil
	})
	defer restore()

	tool := NewJstatGCTool()
	// Omit interval_ms and count — should use defaults.
	args, _ := json.Marshal(map[string]any{"pid": 42})
	_, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// args: ["-gc", "42", "1000", "5"]
	if len(capturedArgs) < 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(capturedArgs), capturedArgs)
	}
	if capturedArgs[2] != "1000" {
		t.Errorf("expected interval=1000, got %s", capturedArgs[2])
	}
	if capturedArgs[3] != "5" {
		t.Errorf("expected count=5, got %s", capturedArgs[3])
	}
}

// ---- async_profile tests ----

func TestAsyncProfile_MissingEnv(t *testing.T) {
	t.Setenv(AsyncProfilerEnvVar, "")

	tool := NewAsyncProfileTool()

	if tool.Name() != "jvm.async_profile" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"pid": 42})
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error when CLOUDY_ASYNC_PROFILER is unset")
	}
	var missingErr *MissingEnvError
	if me, ok := err.(*MissingEnvError); !ok {
		t.Errorf("expected *MissingEnvError, got %T: %v", err, err)
	} else {
		missingErr = me
		_ = missingErr
	}
}

func TestAsyncProfile_RunsProfiler(t *testing.T) {
	t.Setenv(AsyncProfilerEnvVar, "/opt/async-profiler/profiler.sh")

	const profilerOut = `Profiler started
--- Execution profile ---
  39.45%  4000  java/lang/Thread.sleep
  20.00%  2000  com/example/App.hotMethod
`
	var capturedArgs []string
	restore := withStubRunner(t, func(ctx context.Context, name string, args ...string) (string, string, error) {
		capturedArgs = args
		return profilerOut, "", nil
	})
	defer restore()

	tool := NewAsyncProfileTool()
	args, _ := json.Marshal(map[string]any{"pid": 42, "duration_seconds": 10, "format": "text", "event": "cpu"})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Text == "" {
		t.Fatal("expected non-empty Text in Observation")
	}
	// Verify -d 10 was passed.
	found := false
	for i, a := range capturedArgs {
		if a == "-d" && i+1 < len(capturedArgs) && capturedArgs[i+1] == "10" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected -d 10 in args, got %v", capturedArgs)
	}
}
