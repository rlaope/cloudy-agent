// Package metric exposes tech-agnostic, read-only resource-metric tools. The
// Kubernetes metric path already lives in prom.* and k8s.top_pods/top_nodes;
// this package fills the Docker-host gap with metric.container_stats, a
// one-shot container resource sampler. Every call here is list/inspect/stats
// only — cloudy never starts, stops, or execs a container.
package metric

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// hubGetter is the read surface metric.container_stats needs from a Docker
// hub: resolve a host name to a read-only API handle. *dockerclient.Hub
// satisfies it; tests inject a mock so no live daemon is required.
type hubGetter interface {
	Get(name string) (dockerclient.ReadOnlyAPI, error)
}

type containerStatsArgs struct {
	Workload string `json:"workload"`
	Context  string `json:"context"`
}

// containerStatsTool is the imperative implementation behind
// metric.container_stats. Like change.recent it is hand-written rather than
// built via Spec[Args] so it can advertise RiskLow via the RiskRated interface.
type containerStatsTool struct {
	hub hubGetter
}

// NewContainerStatsTool returns the metric.container_stats tool bound to hub.
func NewContainerStatsTool(hub *dockerclient.Hub) tools.Tool {
	return &containerStatsTool{hub: hub}
}

func (t *containerStatsTool) Name() string { return "metric.container_stats" }

func (t *containerStatsTool) Description() string {
	return "Read one-shot CPU, memory, network, and block-IO usage for a workload's containers on a Docker host. Matches containers by exact compose service/project label or exact container name. Read-only; a single sample per container. For Kubernetes use prom.* or k8s.top_pods/top_nodes."
}

func (t *containerStatsTool) Schema() json.RawMessage {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload": str("Workload to sample (container name or compose service/project name). Matched exactly, not by substring. Required."),
			"context":  str("Docker host name to query; empty = the default configured host."),
		},
		"required": []string{"workload"},
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic("metric: schema marshal: " + err.Error())
	}
	return b
}

// Risk implements tools.RiskRated. metric.container_stats reads a single stats
// sample per container — cheap inspection, classified RiskLow.
func (t *containerStatsTool) Risk() tools.RiskLevel { return tools.RiskLow }

func (t *containerStatsTool) Run(ctx context.Context, raw json.RawMessage) (tools.Observation, error) {
	var a containerStatsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("metric.container_stats: parse args: %w", err)
		}
	}
	if a.Workload == "" {
		return tools.Observation{Text: "metric.container_stats: workload is required"}, nil
	}

	api, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("metric.container_stats: get host: %w", err)
	}

	summaries, err := api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return tools.Observation{}, fmt.Errorf("metric.container_stats: list containers: %w", err)
	}

	var rows []containerMetric
	var failures []string
	matched := 0
	for _, s := range summaries {
		if !dockerclient.MatchesWorkload(s, a.Workload) {
			continue
		}
		matched++
		name := dockerclient.DisplayName(s)
		stats, err := api.ContainerStats(ctx, s.ID)
		if err != nil {
			// Tolerate a single container's stats failing (e.g. it stopped
			// between list and stats); note it and keep going.
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		rows = append(rows, computeMetric(name, stats))
	}

	return tools.Observation{
		Text: renderStats(a.Workload, matched, rows, failures),
		Raw:  rows,
	}, nil
}
