package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
)

// describedLag builds a one-group DescribedGroupLags with the given per-topic
// lag so the summarize/render path can be exercised without a live broker.
func describedLag(group, state string, members int, topicLags map[string]int64) kadm.DescribedGroupLag {
	gl := kadm.GroupLag{}
	for topic, lag := range topicLags {
		gl[topic] = map[int32]kadm.GroupMemberLag{
			0: {Topic: topic, Partition: 0, Lag: lag},
		}
	}
	mem := make([]kadm.DescribedGroupMember, members)
	return kadm.DescribedGroupLag{Group: group, State: state, Members: mem, Lag: gl}
}

func TestSummarizeLags(t *testing.T) {
	dls := kadm.DescribedGroupLags{
		"g1": describedLag("g1", "Stable", 2, map[string]int64{"orders": 100, "events": 50}),
	}
	rows := summarizeLags(dls)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.group != "g1" || r.state != "Stable" || r.members != 2 {
		t.Errorf("unexpected row identity: %+v", r)
	}
	if r.total != 150 {
		t.Errorf("total lag should sum partitions across topics = 150, got %d", r.total)
	}
}

// TestRenderKafkaLag_RanksAndFlags pins the ranking and the NO ACTIVE CONSUMER
// flag (lag present, zero members) and the orphaned-summary count.
func TestRenderKafkaLag_RanksAndFlags(t *testing.T) {
	rows := []groupLagRow{
		{group: "small", state: "Stable", members: 3, total: 10, topics: []kadm.TopicLag{{Topic: "a", Lag: 10}}},
		{group: "orphan", state: "Empty", members: 0, total: 4000, topics: []kadm.TopicLag{{Topic: "b", Lag: 4000}}},
	}
	out := renderKafkaLag("kfk", rows, 0, 20)

	if strings.Index(out, "orphan") > strings.Index(out, "small") {
		t.Errorf("higher lag must rank first; got:\n%s", out)
	}
	if !strings.Contains(out, "NO ACTIVE CONSUMER") {
		t.Errorf("a group with lag and 0 members must be flagged; got:\n%s", out)
	}
	if !strings.Contains(out, "1 group(s) with lag but no active consumer") {
		t.Errorf("summary should count orphaned groups; got:\n%s", out)
	}
}

// TestRenderKafkaLag_MinLagKeepsOrphans confirms min_lag drops quiet groups
// but never an orphaned one, and that the limit reports the remainder.
func TestRenderKafkaLag_MinLagKeepsOrphans(t *testing.T) {
	rows := []groupLagRow{
		{group: "busy", state: "Stable", members: 2, total: 5000, topics: []kadm.TopicLag{{Topic: "a", Lag: 5000}}},
		{group: "quiet", state: "Stable", members: 1, total: 5, topics: []kadm.TopicLag{{Topic: "b", Lag: 5}}},
		{group: "orphan-small", state: "Empty", members: 0, total: 3, topics: []kadm.TopicLag{{Topic: "c", Lag: 3}}},
	}
	out := renderKafkaLag("kfk", rows, 1000, 1)
	if strings.Contains(out, "quiet") {
		t.Errorf("min_lag=1000 should drop the quiet group; got:\n%s", out)
	}
	// orphan-small has lag 3 < 1000 but is orphaned, so the summary must still
	// count it even though limit=1 truncates the row list.
	if !strings.Contains(out, "no active consumer") {
		t.Errorf("an orphaned group below min_lag must still be surfaced; got:\n%s", out)
	}
	if !strings.Contains(out, "…and 1 more") {
		t.Errorf("limit=1 over 2 kept groups should report 1 more; got:\n%s", out)
	}
}

// TestRenderKafkaLag_ErrorRow confirms a group whose lag could not be computed
// is shown with its error, not dropped.
func TestRenderKafkaLag_ErrorRow(t *testing.T) {
	rows := []groupLagRow{
		{group: "broken", state: "Stable", err: errors.New("coordinator unavailable")},
	}
	out := renderKafkaLag("kfk", rows, 0, 20)
	if !strings.Contains(out, "broken") || !strings.Contains(out, "lag unavailable") {
		t.Errorf("error rows must surface the failure; got:\n%s", out)
	}
}

// fakeLag is a broker-free lagSource for the tool-body tests.
type fakeLag struct {
	groups  []string
	dls     kadm.DescribedGroupLags
	listErr error
	lagErr  error
}

func (f fakeLag) name() string                                 { return "fake" }
func (f fakeLag) listGroups(context.Context) ([]string, error) { return f.groups, f.listErr }
func (f fakeLag) groupLag(_ context.Context, _ ...string) (kadm.DescribedGroupLags, error) {
	return f.dls, f.lagErr
}

func TestRunKafkaLag_AllGroups(t *testing.T) {
	f := fakeLag{
		groups: []string{"g1"},
		dls: kadm.DescribedGroupLags{
			"g1": describedLag("g1", "Stable", 1, map[string]int64{"orders": 200}),
		},
	}
	obs, err := runKafkaLag(context.Background(), f, "", 0, 20)
	if err != nil {
		t.Fatalf("runKafkaLag: %v", err)
	}
	if !strings.Contains(obs.Text, "g1") || !strings.Contains(obs.Text, "lag=200") {
		t.Errorf("observation should report g1 lag; got:\n%s", obs.Text)
	}
}

func TestRunKafkaLag_NoGroups(t *testing.T) {
	obs, err := runKafkaLag(context.Background(), fakeLag{groups: nil}, "", 0, 20)
	if err != nil {
		t.Fatalf("runKafkaLag: %v", err)
	}
	if !strings.Contains(obs.Text, "no consumer groups found") {
		t.Errorf("an empty cluster should say so; got: %s", obs.Text)
	}
}

func TestRunKafkaLag_ListError(t *testing.T) {
	f := fakeLag{listErr: errors.New("auth failed")}
	if _, err := runKafkaLag(context.Background(), f, "", 0, 20); err == nil {
		t.Error("a list-groups failure must propagate as an error")
	}
}

func TestSplitBrokers(t *testing.T) {
	got := splitBrokers(" b1:9092 , b2:9092 ,, ")
	if len(got) != 2 || got[0] != "b1:9092" || got[1] != "b2:9092" {
		t.Errorf("splitBrokers should trim and drop empties, got %v", got)
	}
	if len(splitBrokers("")) != 0 {
		t.Error("empty broker string should yield no brokers")
	}
}

func TestNewKafkaClient_Reasons(t *testing.T) {
	if _, reason := newKafkaClient("k", "", "", "", "", false); reason == "" {
		t.Error("no brokers should yield a skip reason")
	}
	if _, reason := newKafkaClient("k", "b:9092", "kerberos", "u", "p", false); !strings.Contains(reason, "unsupported sasl_mechanism") {
		t.Errorf("unknown mechanism should be rejected, got: %q", reason)
	}
	// A valid plaintext endpoint builds (lazy connect, no dial here).
	if cl, reason := newKafkaClient("k", "b:9092", "", "", "", false); cl == nil || reason != "" {
		t.Errorf("valid endpoint should build, got reason %q", reason)
	} else {
		cl.Close()
	}
}
