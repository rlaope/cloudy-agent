package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// newGCPInventoryTools builds the GCP read-only inventory / managed-service
// health tools (Cloud SQL instances, Cloud Run services, GKE clusters). Unlike
// GCP metrics/traces, these list commands are first-class read-only gcloud
// verbs (see docs/RFC-CLOUD-OBSERVABILITY.md §9).
func newGCPInventoryTools(projs map[string]*gcpProject) []tools.Tool {
	return []tools.Tool{
		newGCPSQLInstancesListTool(projs),
		newGCPRunServicesListTool(projs),
		newGCPContainerClustersListTool(projs),
	}
}

// newGCPSQLInstancesListTool wraps `gcloud sql instances list` — the Cloud SQL
// managed-database inventory with state and version.
func newGCPSQLInstancesListTool(projs map[string]*gcpProject) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": gcpAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.gcp_sql_instances_list",
		Description: "List Cloud SQL instances with state, version, and tier (read-only `gcloud sql instances list`). Use to inventory managed databases in a GCP project.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			proj, err := pickGCP(projs, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"sql", "instances", "list"}, proj.baseArgs()...)
			body, err := CloudExec(ctx, "gcloud", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_sql_instances_list: %w", err)
			}
			var instances []struct {
				Name            string `json:"name"`
				DatabaseVersion string `json:"databaseVersion"`
				Region          string `json:"region"`
				State           string `json:"state"`
				Settings        struct {
					Tier string `json:"tier"`
				} `json:"settings"`
			}
			if err := json.Unmarshal(body, &instances); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_sql_instances_list: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"INSTANCE", "VERSION", "TIER", "REGION", "STATE"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, i := range instances {
				tbl.Rows = append(tbl.Rows, []string{i.Name, i.DatabaseVersion, i.Settings.Tier, i.Region, i.State})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d Cloud SQL instance(s) in project %q.", len(instances), proj.name),
				Table: tbl,
				Raw:   instances,
			}, nil
		},
	}.Build()
}

// newGCPRunServicesListTool wraps `gcloud run services list` — the Cloud Run
// serverless-service inventory with region and serving URL.
func newGCPRunServicesListTool(projs map[string]*gcpProject) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": gcpAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.gcp_run_services_list",
		Description: "List Cloud Run services with region and serving URL (read-only `gcloud run services list`). Use to inventory serverless services in a GCP project.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			proj, err := pickGCP(projs, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"run", "services", "list"}, proj.baseArgs()...)
			body, err := CloudExec(ctx, "gcloud", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_run_services_list: %w", err)
			}
			// `gcloud run services list --format json` returns the Knative-style
			// service objects: {metadata:{name,labels},status:{url}}.
			var services []struct {
				Metadata struct {
					Name   string `json:"name"`
					Labels struct {
						Region string `json:"cloud.googleapis.com/location"`
					} `json:"labels"`
				} `json:"metadata"`
				Status struct {
					URL string `json:"url"`
				} `json:"status"`
			}
			if err := json.Unmarshal(body, &services); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_run_services_list: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"SERVICE", "REGION", "URL"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, s := range services {
				tbl.Rows = append(tbl.Rows, []string{s.Metadata.Name, s.Metadata.Labels.Region, s.Status.URL})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d Cloud Run service(s) in project %q.", len(services), proj.name),
				Table: tbl,
				Raw:   services,
			}, nil
		},
	}.Build()
}

// newGCPContainerClustersListTool wraps `gcloud container clusters list` — the
// GKE managed-k8s inventory with status, version, and node count.
func newGCPContainerClustersListTool(projs map[string]*gcpProject) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": gcpAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.gcp_container_clusters_list",
		Description: "List GKE clusters with status, master version, and node count (read-only `gcloud container clusters list`). Use to inventory managed Kubernetes clusters in a GCP project.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			proj, err := pickGCP(projs, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"container", "clusters", "list"}, proj.baseArgs()...)
			body, err := CloudExec(ctx, "gcloud", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_container_clusters_list: %w", err)
			}
			var clusters []struct {
				Name                 string `json:"name"`
				Location             string `json:"location"`
				Status               string `json:"status"`
				CurrentMasterVersion string `json:"currentMasterVersion"`
				CurrentNodeCount     int    `json:"currentNodeCount"`
			}
			if err := json.Unmarshal(body, &clusters); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_container_clusters_list: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"CLUSTER", "LOCATION", "VERSION", "NODES", "STATUS"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignLeft},
			}
			for _, c := range clusters {
				tbl.Rows = append(tbl.Rows, []string{
					c.Name, c.Location, c.CurrentMasterVersion, fmt.Sprintf("%d", c.CurrentNodeCount), c.Status,
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d GKE cluster(s) in project %q.", len(clusters), proj.name),
				Table: tbl,
				Raw:   clusters,
			}, nil
		},
	}.Build()
}
