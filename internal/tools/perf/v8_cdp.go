package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/transport"
)

// V8 Inspector CPU profile capture. The Chrome DevTools Protocol exchange is:
//
//  1. List debuggable targets via GET /json/list — already exposed by
//     perf.v8_inspector_targets. We re-fetch here so the caller can refer
//     to a target by name+index without a prior call.
//  2. Open a WebSocket to webSocketDebuggerUrl.
//  3. Send Profiler.enable, Profiler.start.
//  4. Sleep for the configured window.
//  5. Send Profiler.stop — response carries the V8 CPUProfile object.
//  6. Disconnect.
//
// The dialer's NetDial is wired through transport.New so even the CDP
// websocket's upgrade request is GET-only enforced — the WebSocket
// handshake is a GET upgrade, so this is a no-op in practice but stays
// consistent with the rest of the codebase.

// cpuProfile is the subset of the V8 CDP CPUProfile shape we use for the
// flattened-by-function top-N rendering.
type cpuProfile struct {
	Nodes []struct {
		ID        int `json:"id"`
		HitCount  int `json:"hitCount"`
		Children  []int
		CallFrame struct {
			FunctionName string `json:"functionName"`
			URL          string `json:"url"`
			LineNumber   int    `json:"lineNumber"`
		} `json:"callFrame"`
	} `json:"nodes"`
	StartTime int64 `json:"startTime"`
	EndTime   int64 `json:"endTime"`
	Samples   []int `json:"samples"`
}

func newV8CDPCPUProfileTool(clients map[string]*NodeInspectorClient) tools.Tool {
	type args struct {
		Name        string `json:"name"`
		TargetIndex int    `json:"target_index"`
		Duration    int    `json:"duration_seconds"`
		TopN        int    `json:"top_n"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":             nodeEndpointSchema,
			"target_index":     map[string]any{"type": "integer", "description": "Index into /json/list (default 0).", "default": 0, "minimum": 0},
			"duration_seconds": map[string]any{"type": "integer", "description": "Sample window (default 5, max 60).", "default": 5, "minimum": 1, "maximum": 60},
			"top_n":            map[string]any{"type": "integer", "description": "Top N functions by hitCount (default 20, max 200).", "default": 20, "minimum": 1, "maximum": 200},
		},
	})
	return tools.Spec[args]{
		Name:        "perf.v8_inspector_cpu_profile",
		Description: "Capture a V8 CPU profile via the CDP Profiler domain (Profiler.enable → start → sleep → stop) and return the top-N functions by hitCount.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Duration <= 0 {
				a.Duration = 5
			}
			if a.Duration > 60 {
				a.Duration = 60
			}
			if a.TopN <= 0 {
				a.TopN = 20
			}
			if a.TopN > 200 {
				a.TopN = 200
			}
			c, err := pickNode(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			wsURL, err := resolveInspectorWS(ctx, c, a.TargetIndex)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.v8_inspector_cpu_profile: %w", err)
			}
			profile, err := captureCPUProfile(ctx, wsURL, a.Duration)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.v8_inspector_cpu_profile: %w", err)
			}
			tbl, summary := renderTopByHitCount(profile, a.TopN)
			return tools.Observation{Text: summary, Table: tbl, Raw: profile}, nil
		},
	}.Build()
}

// resolveInspectorWS picks the webSocketDebuggerUrl from /json/list at the
// requested index.
func resolveInspectorWS(ctx context.Context, c *NodeInspectorClient, idx int) (string, error) {
	body, err := c.RawGet(ctx, "/json/list", nil)
	if err != nil {
		return "", err
	}
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		return "", fmt.Errorf("decode /json/list: %w", err)
	}
	if idx < 0 || idx >= len(arr) {
		return "", fmt.Errorf("target_index %d out of range (have %d targets)", idx, len(arr))
	}
	ws, _ := arr[idx]["webSocketDebuggerUrl"].(string)
	if ws == "" {
		return "", fmt.Errorf("target %d has no webSocketDebuggerUrl", idx)
	}
	return ws, nil
}

// captureCPUProfile runs the CDP exchange. The dialer carries a hard
// deadline of (duration + 30s) to cover slow start/stop round-trips.
func captureCPUProfile(ctx context.Context, wsURL string, durationSecs int) (*cpuProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(durationSecs+30)*time.Second)
	defer cancel()

	// The inspector ws URL is http-ish (ws://host:port/<id>) — the gorilla
	// dialer uses net.Dial under the hood; wire NetDialContext to the
	// shared read-only transport so the upgrade GET is auditable.
	roTransport := transport.New(nil)
	dialer := &websocket.Dialer{
		NetDialContext:   roTransport.DialContext,
		HandshakeTimeout: 10 * time.Second,
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("parse ws url: %w", err)
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendCDP(conn, 1, "Profiler.enable", nil); err != nil {
		return nil, err
	}
	if _, err := waitFor(conn, 1); err != nil {
		return nil, err
	}
	if err := sendCDP(conn, 2, "Profiler.start", nil); err != nil {
		return nil, err
	}
	if _, err := waitFor(conn, 2); err != nil {
		return nil, err
	}
	select {
	case <-time.After(time.Duration(durationSecs) * time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := sendCDP(conn, 3, "Profiler.stop", nil); err != nil {
		return nil, err
	}
	resp, err := waitFor(conn, 3)
	if err != nil {
		return nil, err
	}

	var stopResp struct {
		Result struct {
			Profile cpuProfile `json:"profile"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &stopResp); err != nil {
		return nil, fmt.Errorf("decode Profiler.stop result: %w", err)
	}
	return &stopResp.Result.Profile, nil
}

// sendCDP writes a CDP request frame on conn.
func sendCDP(conn *websocket.Conn, id int, method string, params any) error {
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	return conn.WriteJSON(msg)
}

// waitFor reads frames until one with the matching id arrives, returning
// its raw bytes. Notification frames (no id) are discarded.
func waitFor(conn *websocket.Conn, id int) ([]byte, error) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("ws read: %w", err)
		}
		var hdr struct {
			ID    int             `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &hdr); err != nil {
			continue
		}
		if hdr.ID == 0 {
			continue // notification
		}
		if hdr.ID != id {
			continue
		}
		if len(hdr.Error) > 0 && string(hdr.Error) != "null" {
			return nil, fmt.Errorf("cdp error: %s", string(hdr.Error))
		}
		return data, nil
	}
}

// renderTopByHitCount renders the top-N nodes by hitCount.
func renderTopByHitCount(p *cpuProfile, topN int) (*render.Table, string) {
	type entry struct {
		hits int
		name string
		url  string
	}
	rows := make([]entry, 0, len(p.Nodes))
	var total int
	for _, n := range p.Nodes {
		total += n.HitCount
		rows = append(rows, entry{n.HitCount, n.CallFrame.FunctionName, n.CallFrame.URL})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].hits > rows[j].hits })
	if len(rows) > topN {
		rows = rows[:topN]
	}
	tbl := &render.Table{
		Headers: []string{"HITS", "HITS%", "FUNCTION", "URL"},
		Aligns:  []render.Align{render.AlignRight, render.AlignRight, render.AlignLeft, render.AlignLeft},
	}
	pct := func(v int) string {
		if total == 0 {
			return "0.0%"
		}
		return fmt.Sprintf("%.1f%%", float64(v)*100/float64(total))
	}
	for _, r := range rows {
		name := r.name
		if name == "" {
			name = "(anonymous)"
		}
		tbl.Rows = append(tbl.Rows, []string{
			fmt.Sprintf("%d", r.hits),
			pct(r.hits),
			name,
			strings.TrimSpace(r.url),
		})
	}
	return tbl, fmt.Sprintf("v8 cpu profile: %d total hits across %d nodes, %d samples, window=%dms",
		total, len(p.Nodes), len(p.Samples), (p.EndTime-p.StartTime)/1000)
}
