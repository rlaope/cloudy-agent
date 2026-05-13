package perf

import (
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// Clients is the per-backend client map for the perf group.
type Clients struct {
	Pprof          map[string]*PprofClient
	NodeInspectors map[string]*NodeInspectorClient
}

// Empty reports whether no HTTP backend has any client wired. Mirrors the
// shape on db.Clients / log.Clients / trace.Clients so callers can ask the
// same question across groups; note the perf group also has the always-on
// rbspy tool, so an Empty() == true Clients does NOT imply an empty
// `perf.*` namespace in the registry.
func (c Clients) Empty() bool { return len(c.Pprof) == 0 && len(c.NodeInspectors) == 0 }

// BuildClients constructs perf-backend HTTP clients. The local-exec rbspy
// tool needs no client and is always registered when its package is
// imported — its probe happens at call time (binary lookup).
func BuildClients(pprofEps, nodeEps []config.HTTPEndpoint) (Clients, []string) {
	cs := Clients{
		Pprof:          map[string]*PprofClient{},
		NodeInspectors: map[string]*NodeInspectorClient{},
	}
	var skips []string

	for _, ep := range pprofEps {
		if ep.Name == "" || ep.URL == "" {
			skips = append(skips, "perf: pprof entry "+strconv.Quote(ep.Name)+": missing name or url")
			continue
		}
		hc, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "perf: pprof "+ep.Name+": "+err.Error())
			continue
		}
		cs.Pprof[ep.Name] = &PprofClient{Client: hc}
	}

	for _, ep := range nodeEps {
		if ep.Name == "" || ep.URL == "" {
			skips = append(skips, "perf: node entry "+strconv.Quote(ep.Name)+": missing name or url")
			continue
		}
		hc, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "perf: node "+ep.Name+": "+err.Error())
			continue
		}
		cs.NodeInspectors[ep.Name] = &NodeInspectorClient{Client: hc}
	}
	return cs, skips
}

// RegisterAll adds every perf.* tool whose backend has at least one
// usable client, plus the always-on local-exec rbspy tool. The group is
// only marked skipped when every backend, including rbspy, has nothing
// to offer — currently rbspy is always registered, so the group will
// always be present in the inventory.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	// rbspy is always registered — exec lookup happens at call time.
	reg.MustRegister(newRBSpyDumpTool())

	if len(c.Pprof) > 0 {
		reg.MustRegister(
			newPprofGoroutineTool(c.Pprof),
			newPprofHeapTool(c.Pprof),
			newPprofAllocsTool(c.Pprof),
			newPprofThreadcreateTool(c.Pprof),
			newPprofCPUTool(c.Pprof),
		)
	} else {
		reg.MarkSkipped("perf-pprof", composeReason("no pprof endpoints configured", skipReasons, "pprof"))
	}

	if len(c.NodeInspectors) > 0 {
		reg.MustRegister(
			newNodeInspectorTargetsTool(c.NodeInspectors),
			newV8CDPCPUProfileTool(c.NodeInspectors),
		)
	} else {
		reg.MarkSkipped("perf-v8", composeReason("no node_inspectors endpoints configured", skipReasons, "node"))
	}

	if bin, err := linuxPerfSupported(); err == nil {
		reg.MustRegister(newLinuxPerfRecordTool(bin))
	} else {
		reg.MarkSkipped("perf-linux", err.Error())
	}
}

// composeReason filters skipReasons relevant to a given subgroup hint
// and prefixes them with a default when nothing matched.
func composeReason(def string, reasons []string, hint string) string {
	var filtered []string
	for _, r := range reasons {
		if strings.Contains(r, hint) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		return def
	}
	return def + ": " + strings.Join(filtered, "; ")
}
