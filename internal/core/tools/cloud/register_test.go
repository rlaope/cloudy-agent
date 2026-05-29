package cloud

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
)

func TestBuildClients_ValidatesAndSkips(t *testing.T) {
	aws := []config.AWSAccount{
		{Name: "prod", Region: "us-east-1"},
		{Name: "", Region: "us-east-1"}, // missing name → skip
		{Name: "no-region", Region: ""}, // missing region → skip
	}
	az := []config.AzureAccount{
		{Name: "az1", SubscriptionID: "sub-1"},
		{Name: "az-bad", SubscriptionID: ""}, // missing sub → skip
	}
	gcp := []config.GCPProject{
		{Name: "g1", ProjectID: "proj"}, // valid → handle (Cloud Logging)
		{Name: "g-bad", ProjectID: ""},  // missing project_id → skip
	}

	c, skips := BuildClients(aws, gcp, az)

	if len(c.AWS) != 1 || c.AWS["prod"] == nil {
		t.Errorf("expected only the valid AWS account, got %v", c.AWS)
	}
	if len(c.Azure) != 1 || c.Azure["az1"] == nil {
		t.Errorf("expected only the valid Azure account, got %v", c.Azure)
	}
	if len(c.GCP) != 1 || c.GCP["g1"] == nil {
		t.Errorf("expected only the valid GCP project, got %v", c.GCP)
	}
	joined := strings.Join(skips, " | ")
	for _, want := range []string{"missing name or region", "missing name or subscription_id", "missing name or project_id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("skip reasons missing %q; got: %s", want, joined)
		}
	}
}

func TestRegisterAll_RegistersExpectedToolNames(t *testing.T) {
	reg := tools.New()
	c, _ := BuildClients(
		[]config.AWSAccount{{Name: "prod", Region: "us-east-1"}},
		[]config.GCPProject{{Name: "gprod", ProjectID: "proj-1"}},
		[]config.AzureAccount{{Name: "az1", SubscriptionID: "sub-1"}},
	)
	RegisterAll(reg, c, nil)

	want := []string{
		"cloud.aws_cw_list_metrics",
		"cloud.aws_cw_get_metric_statistics",
		"cloud.aws_logs_describe_groups",
		"cloud.aws_logs_filter_events",
		"cloud.aws_logs_insights_query",
		"cloud.azure_monitor_metric_definitions",
		"cloud.azure_monitor_metrics",
		"cloud.azure_log_analytics_query",
		"cloud.gcp_logging_read",
	}
	got := map[string]bool{}
	for _, tl := range reg.List() {
		got[tl.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q was not registered", name)
		}
	}
	if len(reg.List()) != len(want) {
		t.Errorf("registered %d tools, want %d", len(reg.List()), len(want))
	}
}

func TestRegisterAll_EmptyMarksSkipped(t *testing.T) {
	reg := tools.New()
	RegisterAll(reg, Clients{}, []string{"cloud: gcp project \"g1\": deferred"})
	if len(reg.List()) != 0 {
		t.Errorf("no tools should be registered for empty clients, got %d", len(reg.List()))
	}
	// The group must be marked skipped so the UI/skill validator knows why.
	if _, skipped := reg.Skipped()["cloud"]; !skipped {
		t.Error("empty cloud clients must mark the group skipped")
	}
}
