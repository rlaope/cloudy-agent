package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// rabbitQueue is the subset of a RabbitMQ management-API queue object the lag
// view needs. consumer_utilisation is nullable (absent until the queue has
// had a consumer), so it is a pointer to distinguish "0% utilised" from "no
// data yet".
type rabbitQueue struct {
	Name         string   `json:"name"`
	Vhost        string   `json:"vhost"`
	Messages     int64    `json:"messages"`
	Ready        int64    `json:"messages_ready"`
	Unacked      int64    `json:"messages_unacknowledged"`
	Consumers    int64    `json:"consumers"`
	ConsumerUtil *float64 `json:"consumer_utilisation"`
	State        string   `json:"state"`
}

// newRabbitMQQueuesTool builds queue.rabbitmq_queues: a read-only view of
// queue depth and consumer health from the RabbitMQ management API. It ranks
// queues by backlog and flags the two failure modes an on-call cares about —
// a queue with a backlog but NO consumer (nothing is draining it) and a queue
// whose consumers are falling behind (large unacked / low utilisation).
func newRabbitMQQueuesTool(clients map[string]*httpapi.Client) tools.Tool {
	type args struct {
		Endpoint    string `json:"endpoint"`
		Vhost       string `json:"vhost"`
		MinMessages int64  `json:"min_messages"`
		Limit       int    `json:"limit"`
	}
	schema := tools.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"endpoint":     map[string]any{"type": "string", "description": "Configured rabbitmq endpoint name; optional when exactly one is configured."},
			"vhost":        map[string]any{"type": "string", "description": "Restrict to a single virtual host (e.g. \"/\"); empty = all vhosts."},
			"min_messages": map[string]any{"type": "integer", "description": "Only show queues whose total message count is at least this; default 0 (all)."},
			"limit":        map[string]any{"type": "integer", "description": "Maximum number of queues to return, ranked by backlog; default 20."},
		},
	})
	return tools.Spec[args]{
		Name:        "queue.rabbitmq_queues",
		Description: "Inspect RabbitMQ queue depth and consumer health via the read-only management API. Ranks queues by backlog (ready + unacknowledged) and flags the two on-call failure modes: a backlogged queue with no consumer draining it, and consumers falling behind (high unacknowledged / low consumer utilisation). Optionally scope to one vhost. Read-only.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			cl, err := pickRabbitMQ(clients, a.Endpoint)
			if err != nil {
				return tools.Observation{}, err
			}
			path := "/api/queues"
			if a.Vhost != "" {
				// The default vhost "/" must be percent-encoded to %2F in the path.
				path += "/" + url.PathEscape(a.Vhost)
			}
			body, err := cl.RawGet(ctx, path, nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("queue.rabbitmq_queues: %w", err)
			}

			var queues []rabbitQueue
			if err := json.Unmarshal(body, &queues); err != nil {
				return tools.Observation{}, fmt.Errorf("queue.rabbitmq_queues: decode management API response: %w", err)
			}

			limit := a.Limit
			if limit <= 0 {
				limit = 20
			}
			return tools.Observation{
				Text: renderRabbitQueues(cl.Name, queues, a.MinMessages, limit),
				Raw:  queues,
			}, nil
		},
	}.Build()
}

// renderRabbitQueues ranks queues by backlog and renders a compact table plus
// a leading summary of the stuck / falling-behind queues, so the model can
// open with "what's wrong" before the per-queue detail.
func renderRabbitQueues(endpoint string, queues []rabbitQueue, minMessages int64, limit int) string {
	filtered := queues[:0:0]
	for _, q := range queues {
		if q.Messages >= minMessages {
			filtered = append(filtered, q)
		}
	}
	// Rank by backlog (total messages) descending; ties by unacked so a
	// falling-behind queue sorts above an idle one of equal depth.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Messages != filtered[j].Messages {
			return filtered[i].Messages > filtered[j].Messages
		}
		return filtered[i].Unacked > filtered[j].Unacked
	})

	var stuck, behind int
	for _, q := range filtered {
		if q.Ready > 0 && q.Consumers == 0 {
			stuck++
		} else if q.Unacked > 0 && q.ConsumerUtil != nil && *q.ConsumerUtil < 0.5 {
			behind++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d queue(s) on %q (ranked by backlog)", len(filtered), endpoint)
	if minMessages > 0 {
		fmt.Fprintf(&b, ", min_messages=%d", minMessages)
	}
	b.WriteByte('\n')
	if stuck > 0 || behind > 0 {
		fmt.Fprintf(&b, "⚠ %d queue(s) backlogged with no consumer; %d queue(s) with consumers falling behind\n", stuck, behind)
	}

	shown := filtered
	if len(shown) > limit {
		shown = shown[:limit]
	}
	for _, q := range shown {
		util := "—"
		if q.ConsumerUtil != nil {
			util = fmt.Sprintf("%.0f%%", *q.ConsumerUtil*100)
		}
		flag := ""
		switch {
		case q.Ready > 0 && q.Consumers == 0:
			flag = " | NO CONSUMER"
		case q.Unacked > 0 && q.ConsumerUtil != nil && *q.ConsumerUtil < 0.5:
			flag = " | FALLING BEHIND"
		}
		fmt.Fprintf(&b, "%s/%s | ready=%d unacked=%d consumers=%d util=%s%s\n",
			q.Vhost, q.Name, q.Ready, q.Unacked, q.Consumers, util, flag)
	}
	if extra := len(filtered) - len(shown); extra > 0 {
		fmt.Fprintf(&b, "…and %d more (raise limit)\n", extra)
	}
	return strings.TrimRight(b.String(), "\n")
}
