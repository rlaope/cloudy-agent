package log

import (
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// Clients is the per-backend client map for the log group.
type Clients struct {
	Loki map[string]*LokiClient
	ES   map[string]*ESClient
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool { return len(c.Loki) == 0 && len(c.ES) == 0 }

// BuildClients constructs the log-backend client maps. Endpoints whose kind
// is unknown or whose URL is missing produce a skip reason that the caller
// surfaces through the registry's group-skip channel. Connection probing is
// deferred to the first call — these backends often live behind L7 gateways
// where startup pings would inflate boot time.
func BuildClients(eps []config.HTTPEndpoint) (Clients, []string) {
	cs := Clients{
		Loki: map[string]*LokiClient{},
		ES:   map[string]*ESClient{},
	}
	var skips []string
	for _, ep := range eps {
		if ep.Name == "" || ep.URL == "" {
			skips = append(skips, "log: entry "+quote(ep.Name)+": missing name or url")
			continue
		}
		hc, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "log: "+ep.Name+": "+err.Error())
			continue
		}
		switch strings.ToLower(ep.Kind) {
		case "loki":
			cs.Loki[ep.Name] = &LokiClient{Client: hc}
		case "elasticsearch", "es", "opensearch":
			cs.ES[ep.Name] = &ESClient{Client: hc}
		default:
			skips = append(skips, "log: "+ep.Name+": unknown kind "+quote(ep.Kind))
		}
	}
	return cs, skips
}

// RegisterAll adds every log.* tool whose backend has at least one client.
// When no backend is wired, the "log" group is marked skipped with a
// composed reason from any per-endpoint failures.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	if c.Empty() {
		reason := "no log endpoints configured"
		if len(skipReasons) > 0 {
			reason = "no usable log endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("log", reason)
		return
	}
	if len(c.Loki) > 0 {
		reg.MustRegister(
			newLokiQueryRangeTool(c.Loki),
			newLokiLabelsTool(c.Loki),
			newLokiLabelValuesTool(c.Loki),
			newLokiSeriesTool(c.Loki),
		)
	}
	if len(c.ES) > 0 {
		reg.MustRegister(
			newESSearchTool(c.ES),
			newESIndicesTool(c.ES),
			newESClusterHealthTool(c.ES),
		)
	}
}

func quote(s string) string { return "\"" + s + "\"" }
