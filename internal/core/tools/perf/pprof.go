// Package perf provides read-only profiler-attach tools across the
// polyglot SRE surface: Go pprof endpoints, the rbspy Ruby sampler, and
// the V8 Inspector discovery surface for Node.js.
//
// HTTP-backed tools (pprof, V8 Inspector) flow through httpapi.Client and
// therefore inherit the transport-layer GET-only contract. Local-exec
// tools (rbspy) restrict themselves to read-only subcommands by argv
// allow-list — the binary is invoked with a fixed argv vector, never
// concatenated from user input.
package perf

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// PprofClient wraps an httpapi.Client with the /debug/pprof/* layout.
type PprofClient struct {
	*httpapi.Client
}

func pickPprof(m map[string]*PprofClient, name string) (*PprofClient, error) {
	return tools.PickEndpoint(m, name, "perf", "pprof endpoint")
}

var pprofEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the pprof endpoint configured under pprof. Optional if exactly one is configured.",
}

// fetchText hits /debug/pprof/<kind>?debug=<n> and returns the body as a
// string. Only the text-formatted variants are exposed; the binary CPU
// profile (which requires the google/pprof parser) is intentionally left
// for a follow-up release.
func fetchText(ctx context.Context, c *PprofClient, kind string, debug int) (string, error) {
	params := url.Values{"debug": {strconv.Itoa(debug)}}
	body, err := c.RawGet(ctx, "/debug/pprof/"+kind, params)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// pprofTextTool is the shared shape for the four text-format pprof tools.
func pprofTextTool(clients map[string]*PprofClient, toolName, kind, desc string, debug int) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        toolName,
		Description: desc,
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": pprofEndpointSchema},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickPprof(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			out, err := fetchText(ctx, c, kind, debug)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("%s: %w", toolName, err)
			}
			return tools.Observation{Text: out, Raw: out}, nil
		},
	}.Build()
}

func newPprofGoroutineTool(clients map[string]*PprofClient) tools.Tool {
	return pprofTextTool(clients, "perf.go_pprof_goroutine", "goroutine",
		"Full goroutine stack dump from a Go service's /debug/pprof/goroutine?debug=2.", 2)
}

func newPprofHeapTool(clients map[string]*PprofClient) tools.Tool {
	return pprofTextTool(clients, "perf.go_pprof_heap", "heap",
		"Heap profile (live allocations) in text form from /debug/pprof/heap?debug=1.", 1)
}

func newPprofAllocsTool(clients map[string]*PprofClient) tools.Tool {
	return pprofTextTool(clients, "perf.go_pprof_allocs", "allocs",
		"Allocation profile (all allocations since process start) in text form from /debug/pprof/allocs?debug=1.", 1)
}

func newPprofThreadcreateTool(clients map[string]*PprofClient) tools.Tool {
	return pprofTextTool(clients, "perf.go_pprof_threadcreate", "threadcreate",
		"OS-thread creation profile in text form from /debug/pprof/threadcreate?debug=1.", 1)
}

var mustJSON = tools.MustJSON
