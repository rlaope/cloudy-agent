package trace

import (
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// Clients is the per-backend client map for the trace group.
type Clients struct {
	Tempo  map[string]*TempoClient
	Jaeger map[string]*JaegerClient
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool { return len(c.Tempo) == 0 && len(c.Jaeger) == 0 }

// BuildClients constructs tracing-backend clients. Endpoints with missing
// name/url or an unknown kind produce skip reasons surfaced through the
// registry. Connection probing is deferred to the first call.
func BuildClients(eps []config.HTTPEndpoint) (Clients, []string) {
	cs := Clients{
		Tempo:  map[string]*TempoClient{},
		Jaeger: map[string]*JaegerClient{},
	}
	var skips []string
	for _, ep := range eps {
		if ep.Name == "" || ep.URL == "" {
			skips = append(skips, "trace: entry "+strconv.Quote(ep.Name)+": missing name or url")
			continue
		}
		hc, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "trace: "+ep.Name+": "+err.Error())
			continue
		}
		switch strings.ToLower(ep.Kind) {
		case "tempo":
			cs.Tempo[ep.Name] = &TempoClient{Client: hc}
		case "jaeger":
			cs.Jaeger[ep.Name] = &JaegerClient{Client: hc}
		default:
			skips = append(skips, "trace: "+ep.Name+": unknown kind "+strconv.Quote(ep.Kind))
		}
	}
	return cs, skips
}

// RegisterAll adds every trace.* tool whose backend has at least one client.
// When no backend is wired, the "trace" group is marked skipped with a
// composed reason from any per-endpoint failures.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	if c.Empty() {
		reason := "no tracing endpoints configured"
		if len(skipReasons) > 0 {
			reason = "no usable tracing endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("trace", reason)
		return
	}
	if len(c.Tempo) > 0 {
		reg.MustRegister(
			newTempoGetTraceTool(c.Tempo),
			newTempoSearchTool(c.Tempo),
			newTempoServiceGraphTool(c.Tempo),
			newTempoRouteREDTool(c.Tempo),
		)
	}
	if len(c.Jaeger) > 0 {
		reg.MustRegister(
			newJaegerServicesTool(c.Jaeger),
			newJaegerOperationsTool(c.Jaeger),
			newJaegerSearchTracesTool(c.Jaeger),
		)
	}
}
