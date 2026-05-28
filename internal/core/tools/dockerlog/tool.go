// Package dockerlog exposes the Docker-host side of read-only log inquiry as
// the log.container tool. The Kubernetes / Loki / Elasticsearch log paths
// already live in the log group (internal/core/tools/log); this package fills
// the plain-Docker-host gap by reading a workload's container logs straight off
// the daemon. Every call is read-only — cloudy reads the log stream and never
// execs into, attaches a writer to, or otherwise mutates a container.
//
// The tool name lives in the log.* namespace so it groups with the other log
// tools in the inventory; the wiring layer keeps it from colliding with the
// HTTP log group's skip bookkeeping (see internal/wiring/tools.go).
package dockerlog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// defaultTail is the number of trailing lines returned per container when the
// caller does not specify tail. It bounds output for a chatty container.
const defaultTail = 200

// hubGetter is the read surface log.container needs from a Docker hub: resolve
// a host name to a read-only API handle. *dockerclient.Hub satisfies it; tests
// inject a mock so no live daemon is required.
type hubGetter interface {
	Get(name string) (dockerclient.ReadOnlyAPI, error)
}

type containerLogsArgs struct {
	Workload string `json:"workload"`
	Context  string `json:"context"`
	Tail     int    `json:"tail"`
	Since    string `json:"since"`
}

// containerLogsTool is the imperative implementation behind log.container. Like
// metric.container_stats it is hand-written rather than built via Spec[Args] so
// it can advertise RiskLow via the RiskRated interface.
type containerLogsTool struct {
	hub hubGetter
}

// NewContainerLogsTool returns the log.container tool bound to hub.
func NewContainerLogsTool(hub *dockerclient.Hub) tools.Tool {
	return &containerLogsTool{hub: hub}
}

func (t *containerLogsTool) Name() string { return "log.container" }

func (t *containerLogsTool) Description() string {
	return "Read recent container logs for a workload on a Docker host, with a small error-line summary. Matches containers by exact compose service/project label or exact container name. Read-only; returns the tail of stdout+stderr. For Kubernetes container logs use k8s.logs; for Loki/Elasticsearch use log.loki_* / log.es_*."
}

func (t *containerLogsTool) Schema() json.RawMessage {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload": str("Workload to read (container name or compose service/project name). Matched exactly, not by substring. Required."),
			"context":  str("Docker host name to query; empty = the default configured host."),
			"tail": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Number of trailing lines per container. Defaults to %d when omitted or <= 0.", defaultTail),
			},
			"since": str("Only logs newer than this Go duration ago, e.g. \"15m\" or \"2h\". Empty = no lower bound."),
		},
		"required": []string{"workload"},
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic("dockerlog: schema marshal: " + err.Error())
	}
	return b
}

// Risk implements tools.RiskRated. log.container reads a bounded tail of an
// existing log stream — cheap inspection, classified RiskLow.
func (t *containerLogsTool) Risk() tools.RiskLevel { return tools.RiskLow }

func (t *containerLogsTool) Run(ctx context.Context, raw json.RawMessage) (tools.Observation, error) {
	var a containerLogsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("log.container: parse args: %w", err)
		}
	}
	if a.Workload == "" {
		return tools.Observation{Text: "log.container: workload is required"}, nil
	}

	since, err := parseSince(a.Since)
	if err != nil {
		return tools.Observation{Text: "log.container: " + err.Error()}, nil
	}

	api, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("log.container: get host: %w", err)
	}

	summaries, err := api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return tools.Observation{}, fmt.Errorf("log.container: list containers: %w", err)
	}

	tail := a.Tail
	if tail <= 0 {
		tail = defaultTail
	}
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       fmt.Sprintf("%d", tail),
		Since:      since,
	}

	var logs []containerLog
	var failures []string
	matched := 0
	for _, s := range summaries {
		if !matchesWorkload(s, a.Workload) {
			continue
		}
		matched++
		name := containerName(s)

		// A TTY container's log stream is raw (no stdcopy framing); inspect
		// first so we only demultiplex the multiplexed (non-TTY) stream.
		tty := false
		if insp, ierr := api.ContainerInspect(ctx, s.ID); ierr == nil && insp.Config != nil {
			tty = insp.Config.Tty
		}

		stream, err := api.ContainerLogs(ctx, s.ID, opts)
		if err != nil {
			// Tolerate a single container's log fetch failing (e.g. it was
			// removed between list and read); note it and keep going.
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		text, derr := readLogStream(stream, tty)
		stream.Close()
		if derr != nil && text == "" {
			failures = append(failures, fmt.Sprintf("%s: %v", name, derr))
			continue
		}
		if derr != nil {
			// Partial transcript: some lines decoded before a stream error
			// (e.g. a daemon Systemerr frame). Keep what we have but flag it.
			failures = append(failures, fmt.Sprintf("%s: partial: %v", name, derr))
		}
		logs = append(logs, containerLog{
			Name: name,
			// tail is re-applied locally: the API's Tail counts log entries,
			// while we count split lines after demux — the local cap is the
			// authoritative bound on rendered lines.
			Lines:      tailLines(text, tail),
			ErrorCount: countErrorLines(text),
		})
	}

	return tools.Observation{
		Text: renderLogs(a.Workload, matched, logs, failures),
		Raw:  logs,
	}, nil
}

// parseSince converts a Go duration string into the Unix-second string the
// Docker API's LogsOptions.Since expects. An empty input yields "" (no bound).
// An unparseable duration returns a user-facing error rather than failing the
// whole call.
func parseSince(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return "", fmt.Errorf("invalid since duration %q (e.g. \"15m\", \"2h\")", s)
	}
	return fmt.Sprintf("%d", time.Now().Add(-d).Unix()), nil
}

// matchesWorkload reports whether a container summary relates to workload.
// Matching is case-insensitive and EXACT against the compose service/project
// labels or a container name — substring matching is deliberately avoided so
// "api" does not match "api-gateway". Mirrors metric/tool.go's semantics; the
// small duplication keeps each tool group's internals unexported.
func matchesWorkload(s container.Summary, workload string) bool {
	if workload == "" {
		return false
	}
	wl := strings.ToLower(workload)
	for _, key := range []string{"com.docker.compose.service", "com.docker.compose.project"} {
		if v, ok := s.Labels[key]; ok && strings.ToLower(v) == wl {
			return true
		}
	}
	for _, n := range s.Names {
		if strings.ToLower(strings.TrimPrefix(n, "/")) == wl {
			return true
		}
	}
	return false
}

// containerName returns a stable display name for a summary: the first name
// (slash-trimmed) when present, else the short container ID.
func containerName(s container.Summary) string {
	if len(s.Names) > 0 {
		return strings.TrimPrefix(s.Names[0], "/")
	}
	if len(s.ID) > 12 {
		return s.ID[:12]
	}
	return s.ID
}
