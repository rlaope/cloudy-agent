package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// DCGMTool implements gpu.dcgm_metrics.
type DCGMTool struct {
	clients    map[string]*promclient.Client
	defaultKey string
}

// NewDCGMTool constructs a DCGMTool. clients maps endpoint names to Prom clients.
func NewDCGMTool(clients map[string]*promclient.Client) *DCGMTool {
	def := ""
	for k := range clients {
		def = k
		break
	}
	return &DCGMTool{clients: clients, defaultKey: def}
}

func (t *DCGMTool) Name() string { return "gpu.dcgm_metrics" }
func (t *DCGMTool) Description() string {
	return "Query DCGM GPU metrics from a Prometheus endpoint (DCGM exporter). Returns top-N GPUs by utilization."
}
func (t *DCGMTool) Schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"endpoint": map[string]any{
				"type":        "string",
				"description": "Named Prometheus endpoint (empty = default).",
			},
			"top": map[string]any{
				"type":        "integer",
				"description": "Number of top GPUs to return (default: 10).",
				"default":     10,
				"minimum":     1,
			},
		},
	}
	b, _ := json.Marshal(s)
	return b
}

func (t *DCGMTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint string `json:"endpoint"`
		Top      int    `json:"top"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("gpu.dcgm_metrics: parse args: %w", err)
	}
	if a.Top <= 0 {
		a.Top = 10
	}

	key := a.Endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return tools.Observation{}, fmt.Errorf("gpu.dcgm_metrics: unknown endpoint %q", a.Endpoint)
	}

	now := time.Time{} // zero = use current time in promclient.Client.Query

	// Query the four DCGM metrics. Errors are non-fatal; missing metrics yield empty columns.
	utilVec, _ := c.Query(ctx, "DCGM_FI_DEV_GPU_UTIL", now)
	fbUsedVec, _ := c.Query(ctx, "DCGM_FI_DEV_FB_USED", now)
	fbFreeVec, _ := c.Query(ctx, "DCGM_FI_DEV_FB_FREE", now)
	tempVec, _ := c.Query(ctx, "DCGM_FI_DEV_GPU_TEMP", now)

	type gpuKey struct{ gpu, instance string }
	type gpuData struct {
		util, fbUsed, fbFree, temp float64
	}
	data := map[gpuKey]*gpuData{}

	ensure := func(k gpuKey) *gpuData {
		if data[k] == nil {
			data[k] = &gpuData{}
		}
		return data[k]
	}

	if utilVec != nil {
		for _, s := range utilVec.Vector {
			k := gpuKey{gpu: s.Labels["gpu"], instance: s.Labels["instance"]}
			ensure(k).util = s.Value
		}
	}
	if fbUsedVec != nil {
		for _, s := range fbUsedVec.Vector {
			k := gpuKey{gpu: s.Labels["gpu"], instance: s.Labels["instance"]}
			ensure(k).fbUsed = s.Value
		}
	}
	if fbFreeVec != nil {
		for _, s := range fbFreeVec.Vector {
			k := gpuKey{gpu: s.Labels["gpu"], instance: s.Labels["instance"]}
			ensure(k).fbFree = s.Value
		}
	}
	if tempVec != nil {
		for _, s := range tempVec.Vector {
			k := gpuKey{gpu: s.Labels["gpu"], instance: s.Labels["instance"]}
			ensure(k).temp = s.Value
		}
	}

	// Sort by utilization descending, then by instance+gpu for stability.
	type entry struct {
		key  gpuKey
		data *gpuData
	}
	var entries []entry
	for k, d := range data {
		entries = append(entries, entry{k, d})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].data.util != entries[j].data.util {
			return entries[i].data.util > entries[j].data.util
		}
		if entries[i].key.instance != entries[j].key.instance {
			return entries[i].key.instance < entries[j].key.instance
		}
		return entries[i].key.gpu < entries[j].key.gpu
	})

	if a.Top < len(entries) {
		entries = entries[:a.Top]
	}

	tbl := &render.Table{
		Headers: []string{"INSTANCE", "GPU", "UTIL%", "FB USED MiB", "FB FREE MiB", "TEMP°C"},
		Aligns: []render.Align{
			render.AlignLeft,
			render.AlignLeft,
			render.AlignRight,
			render.AlignRight,
			render.AlignRight,
			render.AlignRight,
		},
	}

	var sb strings.Builder
	for _, e := range entries {
		fbTotal := e.data.fbUsed + e.data.fbFree
		tbl.Rows = append(tbl.Rows, []string{
			e.key.instance,
			e.key.gpu,
			fmt.Sprintf("%.1f", e.data.util),
			fmt.Sprintf("%.0f", e.data.fbUsed),
			fmt.Sprintf("%.0f", e.data.fbFree),
			fmt.Sprintf("%.1f", e.data.temp),
		})
		fmt.Fprintf(&sb, "instance=%s gpu=%s util=%.1f%% mem=%.0f/%.0fMiB temp=%.1f°C\n",
			e.key.instance, e.key.gpu, e.data.util, e.data.fbUsed, fbTotal, e.data.temp)
	}

	return tools.Observation{
		Text:  sb.String(),
		Table: tbl,
		Raw:   data,
	}, nil
}
