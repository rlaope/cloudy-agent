package queue

import (
	"context"
	"crypto/tls"
	"fmt"
	"sort"
	"strings"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// topTopicsPerGroup bounds how many per-topic lag breakdowns are shown for a
// group, so a group consuming hundreds of topics stays one readable line.
const topTopicsPerGroup = 3

// flagNoActiveConsumer marks a group that has lag but no member consuming it —
// the Kafka analog of RabbitMQ's NO CONSUMER (the group is Empty/Dead while its
// committed offsets fall further behind the log end).
const flagNoActiveConsumer = "NO ACTIVE CONSUMER"

// lagSource is the seam between the Kafka admin protocol and the tool logic so
// the rollup/render can be tested without a live broker. The real
// implementation is *kafkaClient.
type lagSource interface {
	name() string
	listGroups(ctx context.Context) ([]string, error)
	groupLag(ctx context.Context, groups ...string) (kadm.DescribedGroupLags, error)
}

// kafkaClient holds a franz-go admin client bound to one configured endpoint.
// The underlying kgo.Client connects lazily on first request, so an unused
// endpoint holds no broker connections or goroutines.
type kafkaClient struct {
	endpoint string
	kc       *kgo.Client
	adm      *kadm.Client
}

func (c *kafkaClient) name() string { return c.endpoint }

func (c *kafkaClient) listGroups(ctx context.Context) ([]string, error) {
	listed, err := c.adm.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	return listed.Groups(), nil
}

func (c *kafkaClient) groupLag(ctx context.Context, groups ...string) (kadm.DescribedGroupLags, error) {
	return c.adm.Lag(ctx, groups...)
}

// Close releases the underlying broker connections and goroutines.
func (c *kafkaClient) Close() {
	if c.kc != nil {
		c.kc.Close()
	}
}

// newKafkaClient builds a kafkaClient from a kafka endpoint. It returns a
// human-readable reason (and nil client) when the endpoint is unusable, so the
// group skip banner can explain why. password is the already-resolved SASL
// password (empty when no SASL).
func newKafkaClient(name, brokers, mechanism, user, password string, useTLS bool) (*kafkaClient, string) {
	seeds := splitBrokers(brokers)
	if len(seeds) == 0 {
		return nil, fmt.Sprintf("kafka endpoint %q: no brokers", name)
	}

	opts := []kgo.Opt{kgo.SeedBrokers(seeds...)}
	if useTLS {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	switch strings.ToLower(mechanism) {
	case "":
		// no SASL
	case "plain":
		opts = append(opts, kgo.SASL(plain.Auth{User: user, Pass: password}.AsMechanism()))
	case "scram-sha-256":
		opts = append(opts, kgo.SASL(scram.Auth{User: user, Pass: password}.AsSha256Mechanism()))
	case "scram-sha-512":
		opts = append(opts, kgo.SASL(scram.Auth{User: user, Pass: password}.AsSha512Mechanism()))
	default:
		return nil, fmt.Sprintf("kafka endpoint %q: unsupported sasl_mechanism %q", name, mechanism)
	}

	kc, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Sprintf("kafka endpoint %q: %v", name, err)
	}
	return &kafkaClient{endpoint: name, kc: kc, adm: kadm.NewClient(kc)}, ""
}

// splitBrokers parses a comma-separated broker list, trimming spaces and
// dropping empties.
func splitBrokers(s string) []string {
	var out []string
	for _, b := range strings.Split(s, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// groupLagRow is the flattened, broker-free view of one consumer group's lag
// that the renderer consumes. Deriving it from kadm types keeps the render and
// rollup testable with literals instead of a live cluster.
type groupLagRow struct {
	group   string
	state   string
	members int
	total   int64
	topics  []kadm.TopicLag
	err     error
}

// summarizeLags flattens the admin lag result into ranked rows. A group whose
// lag could not be computed becomes a row carrying its error rather than being
// dropped, so the operator sees the failure instead of a silent gap.
func summarizeLags(dls kadm.DescribedGroupLags) []groupLagRow {
	rows := make([]groupLagRow, 0, len(dls))
	for _, dl := range dls.Sorted() {
		row := groupLagRow{
			group:   dl.Group,
			state:   dl.State,
			members: len(dl.Members),
		}
		if err := dl.Error(); err != nil {
			row.err = err
			rows = append(rows, row)
			continue
		}
		row.total = dl.Lag.Total()
		row.topics = dl.Lag.TotalByTopic().Sorted()
		rows = append(rows, row)
	}
	return rows
}

// rowFlag returns the NO ACTIVE CONSUMER flag for a group that has lag but no
// member consuming it; "" otherwise.
func rowFlag(r groupLagRow) string {
	if r.err == nil && r.members == 0 && r.total > 0 {
		return flagNoActiveConsumer
	}
	return ""
}

// renderKafkaLag ranks groups by total lag and renders a compact table plus a
// leading summary of groups that are falling behind with no active consumer.
// A flagged group is shown regardless of minLag so a small-but-orphaned group
// is never hidden.
func renderKafkaLag(endpoint string, rows []groupLagRow, minLag int64, limit int) string {
	kept := rows[:0:0]
	for _, r := range rows {
		if r.err != nil || r.total >= minLag || rowFlag(r) != "" {
			kept = append(kept, r)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].total > kept[j].total })

	var orphaned int
	for _, r := range kept {
		if rowFlag(r) != "" {
			orphaned++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d consumer group(s) on %q (ranked by lag)", len(kept), endpoint)
	if minLag > 0 {
		fmt.Fprintf(&b, ", min_lag=%d (orphaned groups always shown)", minLag)
	}
	b.WriteByte('\n')
	if orphaned > 0 {
		fmt.Fprintf(&b, "⚠ %d group(s) with lag but no active consumer\n", orphaned)
	}

	shown := kept
	if len(shown) > limit {
		shown = shown[:limit]
	}
	for _, r := range shown {
		if r.err != nil {
			fmt.Fprintf(&b, "%s | state=%s | lag unavailable: %v\n", r.group, r.state, r.err)
			continue
		}
		flag := ""
		if f := rowFlag(r); f != "" {
			flag = " | " + f
		}
		fmt.Fprintf(&b, "%s | state=%s | members=%d | lag=%d%s | top: %s\n",
			r.group, r.state, r.members, r.total, flag, topTopics(r.topics))
	}
	if extra := len(kept) - len(shown); extra > 0 {
		fmt.Fprintf(&b, "…and %d more (raise limit)\n", extra)
	}
	return strings.TrimRight(b.String(), "\n")
}

// topTopics renders the highest-lag topics for a group, capped.
func topTopics(topics []kadm.TopicLag) string {
	if len(topics) == 0 {
		return "—"
	}
	// TotalByTopic().Sorted() orders by topic name; re-rank by lag desc so the
	// worst topic leads.
	sorted := append([]kadm.TopicLag(nil), topics...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Lag > sorted[j].Lag })
	if len(sorted) > topTopicsPerGroup {
		sorted = sorted[:topTopicsPerGroup]
	}
	parts := make([]string, len(sorted))
	for i, t := range sorted {
		parts[i] = fmt.Sprintf("%s=%d", t.Topic, t.Lag)
	}
	return strings.Join(parts, " ")
}

// newKafkaConsumerLagTool builds queue.kafka_consumer_lag: a read-only view of
// consumer-group lag against the brokers directly (no exporter required). It
// ranks groups by total lag and flags groups that have fallen behind with no
// active member consuming them.
func newKafkaConsumerLagTool(clients map[string]*kafkaClient) tools.Tool {
	type args struct {
		Endpoint string `json:"endpoint"`
		Group    string `json:"group"`
		MinLag   int64  `json:"min_lag"`
		Limit    int    `json:"limit"`
	}
	schema := tools.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"endpoint": map[string]any{"type": "string", "description": "Configured kafka endpoint name; optional when exactly one is configured."},
			"group":    map[string]any{"type": "string", "description": "Restrict to a single consumer group; empty = all groups in the cluster."},
			"min_lag":  map[string]any{"type": "integer", "description": "Only show groups whose total lag is at least this; default 0 (all). Groups with lag but no active consumer are always shown."},
			"limit":    map[string]any{"type": "integer", "description": "Maximum number of groups to return, ranked by lag; default 20."},
		},
	})
	return tools.Spec[args]{
		Name:        "queue.kafka_consumer_lag",
		Description: "Inspect Kafka consumer-group lag directly against the brokers via the admin protocol (no exporter required). Ranks consumer groups by total lag (how far their committed offsets trail the log end), breaks it down by the worst topics, and flags groups that have lag but no active consumer member draining them. Optionally scope to one group. Read-only — only admin describe/list and offset reads are issued.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			cl, err := pickKafka(clients, a.Endpoint)
			if err != nil {
				return tools.Observation{}, err
			}
			return runKafkaLag(ctx, cl, a.Group, a.MinLag, a.Limit)
		},
	}.Build()
}

// runKafkaLag is the tool body split out so it can be driven by a fake
// lagSource in tests.
func runKafkaLag(ctx context.Context, src lagSource, group string, minLag int64, limit int) (tools.Observation, error) {
	var groups []string
	if group != "" {
		groups = []string{group}
	} else {
		all, err := src.listGroups(ctx)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("queue.kafka_consumer_lag: list groups: %w", err)
		}
		groups = all
	}
	if len(groups) == 0 {
		return tools.Observation{Text: fmt.Sprintf("no consumer groups found on %q", src.name())}, nil
	}

	dls, err := src.groupLag(ctx, groups...)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("queue.kafka_consumer_lag: %w", err)
	}

	if limit <= 0 {
		limit = 20
	}
	rows := summarizeLags(dls)
	return tools.Observation{
		Text: renderKafkaLag(src.name(), rows, minLag, limit),
		Raw:  dls,
	}, nil
}
