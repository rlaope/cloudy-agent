package oncall

import (
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// Clients is the per-backend client map for the oncall group. Today only
// PagerDuty is wired; the surface is shaped as a map so a future Opsgenie /
// VictorOps backend can join without reshaping the registration pipeline.
type Clients struct {
	PagerDuty map[string]*PagerDutyClient
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool { return len(c.PagerDuty) == 0 }

// BuildClients constructs the oncall-backend client maps. Endpoints with a
// missing name produce a skip reason the caller surfaces through the registry;
// a missing URL defaults to PagerDuty's REST API root. Connection probing is
// deferred to the first call, matching the gitops/alert convention.
func BuildClients(pdEPs []config.PagerDutyEndpoint) (Clients, []string) {
	cs := Clients{PagerDuty: map[string]*PagerDutyClient{}}
	var skips []string
	for _, ep := range pdEPs {
		if ep.Name == "" {
			skips = append(skips, "oncall: pagerduty entry "+strconv.Quote(ep.Name)+": missing name")
			continue
		}
		url := ep.URL
		if url == "" {
			url = pagerDutyBaseURL
		}
		hc, err := httpapi.NewClient(ep.Name, url, httpapi.Auth{
			TokenEnv:    ep.TokenEnv,
			TokenScheme: pagerDutyTokenScheme,
		})
		if err != nil {
			skips = append(skips, "oncall: pagerduty "+ep.Name+": "+err.Error())
			continue
		}
		cs.PagerDuty[ep.Name] = &PagerDutyClient{Client: hc}
	}
	return cs, skips
}

// RegisterAll adds every oncall.* tool whose backend has at least one client.
// When no backend is wired, the "oncall" group is marked skipped with a
// composed reason from any per-endpoint failures.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	if c.Empty() {
		reason := "no PagerDuty account configured"
		if len(skipReasons) > 0 {
			reason = "no usable oncall endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("oncall", reason)
		return
	}
	if len(c.PagerDuty) > 0 {
		reg.MustRegister(
			newListIncidentsTool(c.PagerDuty),
			newWhoIsOnCallTool(c.PagerDuty),
		)
	}
}
