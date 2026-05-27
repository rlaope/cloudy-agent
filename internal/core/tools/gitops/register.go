package gitops

import (
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// Clients is the per-backend client map for the gitops group. Today only
// Argo CD is wired; the surface is shaped as a map so a future GitHub /
// Flux backend can join without reshaping the registration pipeline.
type Clients struct {
	Argo map[string]*ArgoClient
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool { return len(c.Argo) == 0 }

// BuildClients constructs the gitops-backend client maps. Endpoints with
// missing name/url produce a skip reason that the caller surfaces through
// the registry's group-skip channel. Connection probing is deferred to the
// first call.
func BuildClients(argoEPs []config.ArgoCDEndpoint) (Clients, []string) {
	cs := Clients{
		Argo: map[string]*ArgoClient{},
	}
	var skips []string
	for _, ep := range argoEPs {
		if ep.Name == "" || ep.URL == "" {
			skips = append(skips, "gitops: argo entry "+strconv.Quote(ep.Name)+": missing name or url")
			continue
		}
		hc, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "gitops: argo "+ep.Name+": "+err.Error())
			continue
		}
		cs.Argo[ep.Name] = &ArgoClient{Client: hc}
	}
	return cs, skips
}

// RegisterAll adds every gitops.* tool whose backend has at least one
// client. When no backend is wired, the "gitops" group is marked skipped
// with a composed reason from any per-endpoint failures.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	if c.Empty() {
		reason := "no Argo CD endpoint configured"
		if len(skipReasons) > 0 {
			reason = "no usable gitops endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("gitops", reason)
		return
	}
	if len(c.Argo) > 0 {
		reg.MustRegister(
			newArgoListAppsTool(c.Argo),
			newArgoAppStatusTool(c.Argo),
			newArgoAppHistoryTool(c.Argo),
		)
	}
}
