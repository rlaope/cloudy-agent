// Package queue provides read-only diagnostic tools for the cloudy SRE agent
// against message-queue / streaming backends. The first wired backend is
// RabbitMQ via its HTTP management API; the tools surface queue depth and
// consumer lag — the "is a consumer keeping up?" signal that is one of the
// most common async-system pages. All access is read-only inspection: cloudy
// only issues GETs against the management API and never publishes, acks, or
// purges.
package queue

import (
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/config"
)

// Clients holds connected backend handles keyed by the endpoint Name from
// cloudy.yaml. Each backend kind has its own map; tools look up by name.
type Clients struct {
	RabbitMQ map[string]*httpapi.Client
}

// Empty reports whether no queue backend was established.
func (c Clients) Empty() bool {
	return len(c.RabbitMQ) == 0
}

// BuildClients constructs the per-backend client maps from the configured
// queue endpoints. It returns the established clients plus a list of
// human-readable skip reasons for endpoints that could not be built, so the
// caller can fold them into the group's skip banner. A malformed endpoint is
// skipped, not fatal — one bad entry must not drop the whole group.
func BuildClients(eps []config.QueueEndpoint) (Clients, []string) {
	c := Clients{}
	var skips []string

	for _, ep := range eps {
		switch strings.ToLower(ep.Kind) {
		case "rabbitmq":
			if ep.Name == "" || ep.URL == "" {
				skips = append(skips, fmt.Sprintf("rabbitmq endpoint %q: missing name or url", ep.Name))
				continue
			}
			cl, err := httpapi.NewClient(ep.Name, ep.URL, httpapi.Auth{
				BasicUser:    ep.BasicUser,
				BasicPassEnv: ep.PasswordEnv,
			})
			if err != nil {
				skips = append(skips, fmt.Sprintf("rabbitmq endpoint %q: %v", ep.Name, err))
				continue
			}
			if c.RabbitMQ == nil {
				c.RabbitMQ = map[string]*httpapi.Client{}
			}
			c.RabbitMQ[ep.Name] = cl
		case "":
			skips = append(skips, fmt.Sprintf("queue endpoint %q: missing kind", ep.Name))
		default:
			skips = append(skips, fmt.Sprintf("queue endpoint %q: unsupported kind %q", ep.Name, ep.Kind))
		}
	}

	return c, skips
}

// pickRabbitMQ resolves the named RabbitMQ client, or the sole client when
// name is empty and exactly one is configured. It mirrors the deterministic
// single-endpoint default the other tool groups use so a one-cluster operator
// never has to name the endpoint.
func pickRabbitMQ(clients map[string]*httpapi.Client, name string) (*httpapi.Client, error) {
	if name != "" {
		cl, ok := clients[name]
		if !ok {
			return nil, fmt.Errorf("queue: no rabbitmq endpoint named %q", name)
		}
		return cl, nil
	}
	if len(clients) == 1 {
		for _, cl := range clients {
			return cl, nil
		}
	}
	return nil, fmt.Errorf("queue: endpoint is required (%d rabbitmq endpoints configured)", len(clients))
}
