package ebpf

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/tools"
)

// bpftraceCatalogEntry pairs a human-readable name with a fixed,
// pre-vetted bpftrace one-liner. Entries are append-only — never accept
// user-supplied bpftrace source. Every entry must be read-only: it
// reads kernel state via tracepoints or k/uprobes and prints results, with
// no `system()` calls, no kfunc attach to mutating paths, and no probes
// on userspace functions that take pointers we'd then dereference under
// LLM control.
type bpftraceCatalogEntry struct {
	Key  string
	Desc string
	Prog string
}

// bpftraceCatalog is the closed set of read-only one-liners cloudy will
// run. Adding to this list is a deliberate codebase change and a code
// review point — that is the security mechanism.
var bpftraceCatalog = []bpftraceCatalogEntry{
	{
		Key:  "syscall_counts",
		Desc: "Aggregate syscall counts by command name (tracepoint:raw_syscalls:sys_enter).",
		Prog: `tracepoint:raw_syscalls:sys_enter { @[comm] = count(); }`,
	},
	{
		Key:  "file_opens",
		Desc: "Trace openat() calls with pid, comm, and filename.",
		Prog: `tracepoint:syscalls:sys_enter_openat { printf("%-6d %-16s %s\n", pid, comm, str(args->filename)); }`,
	},
	{
		Key:  "tcp_connects",
		Desc: "Trace TCP connect() attempts with pid and comm.",
		Prog: `tracepoint:syscalls:sys_enter_connect { printf("%-6d %-16s\n", pid, comm); }`,
	},
	{
		Key:  "vfs_read_lat",
		Desc: "Histogram of vfs_read latency in microseconds via kfunc enter/exit pair.",
		Prog: `kfunc:vfs_read { @start[tid] = nsecs; } kretfunc:vfs_read /@start[tid]/ { @us = hist((nsecs - @start[tid]) / 1000); delete(@start[tid]); }`,
	},
}

func catalogEntryByKey(key string) (bpftraceCatalogEntry, bool) {
	for _, e := range bpftraceCatalog {
		if e.Key == key {
			return e, true
		}
	}
	return bpftraceCatalogEntry{}, false
}

func catalogKeys() []string {
	out := make([]string, len(bpftraceCatalog))
	for i, e := range bpftraceCatalog {
		out[i] = e.Key
	}
	return out
}

// newBpftraceOnelinerTool exposes the catalog as a single tool whose only
// input is a script_key naming an entry. Free-form `program` input is
// not accepted — the schema does not declare it.
func newBpftraceOnelinerTool(bin string) tools.Tool {
	type args struct {
		ScriptKey string `json:"script_key"`
		Duration  int    `json:"duration_seconds"`
	}
	keys := catalogKeys()
	descLines := make([]string, len(bpftraceCatalog))
	for i, e := range bpftraceCatalog {
		descLines[i] = fmt.Sprintf("- %s: %s", e.Key, e.Desc)
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"script_key": map[string]any{
				"type":        "string",
				"description": "Catalog key. Allowed values: " + strings.Join(keys, ", "),
				"enum":        keys,
			},
			"duration_seconds": bccDurationArg,
		},
		"required": []string{"script_key"},
	})
	return tools.Spec[args]{
		Name: "ebpf.bpftrace_oneliner",
		Description: "Run a pre-vetted read-only bpftrace one-liner from the catalog for the configured duration. " +
			"Free-form scripts are intentionally not accepted.\n\nCatalog:\n" + strings.Join(descLines, "\n"),
		Schema: schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			entry, ok := catalogEntryByKey(a.ScriptKey)
			if !ok {
				return tools.Observation{}, fmt.Errorf("ebpf.bpftrace_oneliner: unknown script_key %q (allowed: %s)", a.ScriptKey, strings.Join(keys, ", "))
			}
			d := boundDuration(a.Duration)
			ctx, cancel := context.WithTimeout(ctx, time.Duration(d+5)*time.Second)
			defer cancel()
			out, _, err := runner(ctx, bin, "-e", entry.Prog, "-q")
			if err != nil {
				return tools.Observation{}, fmt.Errorf("ebpf.bpftrace_oneliner: %w", err)
			}
			return tools.Observation{
				Text: out + "\n(catalog=" + entry.Key + " duration=" + strconv.Itoa(d) + "s)",
				Raw:  out,
			}, nil
		},
	}.Build()
}
