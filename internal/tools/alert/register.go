package alert

import (
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// Clients is the per-backend client map for the alert group. The Alertmanager
// half drives alert.list_active + alert.list_silences; the PromRules half
// drives alert.list_rules and is built from the existing PrometheusEndpoint
// slice (rules live in Prometheus, not Alertmanager).
type Clients struct {
	AM        map[string]*AMClient
	PromRules map[string]*PromRulesClient
}

// Empty reports whether no backend has any client wired.
func (c Clients) Empty() bool { return len(c.AM) == 0 && len(c.PromRules) == 0 }

// BuildClients constructs both client maps from the relevant config slices.
// Endpoints with missing name/url produce a skip reason surfaced through the
// registry. Connection probing is deferred to the first call to match the
// log/trace group convention.
func BuildClients(amEPs []config.AlertmanagerEndpoint, promEPs []config.PrometheusEndpoint) (Clients, []string) {
	cs := Clients{
		AM:        map[string]*AMClient{},
		PromRules: map[string]*PromRulesClient{},
	}
	var skips []string
	for _, ep := range amEPs {
		if ep.Name == "" || ep.URL == "" {
			skips = append(skips, "alert: alertmanager entry "+strconv.Quote(ep.Name)+": missing name or url")
			continue
		}
		hc, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "alert: alertmanager "+ep.Name+": "+err.Error())
			continue
		}
		cs.AM[ep.Name] = &AMClient{Client: hc}
	}
	for _, ep := range promEPs {
		if ep.URL == "" {
			continue
		}
		name := ep.Name
		if name == "" {
			name = ep.URL
		}
		hc, err := httpapi.NewClient(name, ep.URL, httpapi.Auth{
			BearerEnv:    ep.BearerEnv,
			BasicUser:    ep.BasicUser,
			BasicPassEnv: ep.BasicPassEnv,
		})
		if err != nil {
			skips = append(skips, "alert: prometheus "+name+": "+err.Error())
			continue
		}
		cs.PromRules[name] = &PromRulesClient{Client: hc}
	}
	return cs, skips
}

// RegisterAll adds every alert.* tool whose backend has at least one client.
// When neither backend is wired, the "alert" group is marked skipped with a
// composed reason from any per-endpoint failures.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	if c.Empty() {
		reason := "no Alertmanager endpoint configured"
		if len(skipReasons) > 0 {
			reason = "no usable alert endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("alert", reason)
		return
	}
	if len(c.AM) > 0 {
		reg.MustRegister(
			newAMListActiveTool(c.AM),
			newAMListSilencesTool(c.AM),
		)
	}
	if len(c.PromRules) > 0 {
		reg.MustRegister(
			newPromListRulesTool(c.PromRules),
		)
	}
}
