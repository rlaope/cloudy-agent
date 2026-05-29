package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// newAzureInventoryTools builds the Azure read-only inventory / managed-service
// health tools (SQL servers, Function apps, AKS clusters).
func newAzureInventoryTools(accts map[string]*azureAccount) []tools.Tool {
	return []tools.Tool{
		newAzureSQLServerListTool(accts),
		newAzureFunctionAppListTool(accts),
		newAzureAKSListTool(accts),
	}
}

// newAzureSQLServerListTool wraps `az sql server list` — the Azure SQL logical-
// server inventory with region and state.
func newAzureSQLServerListTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": azureAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_sql_server_list",
		Description: "List Azure SQL logical servers with region and state (read-only `az sql server list`). Use to inventory managed databases in a subscription.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"sql", "server", "list"}, acct.baseArgs()...)
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_sql_server_list: %w", err)
			}
			var servers []struct {
				Name          string `json:"name"`
				Location      string `json:"location"`
				ResourceGroup string `json:"resourceGroup"`
				State         string `json:"state"`
				Version       string `json:"version"`
			}
			if err := json.Unmarshal(body, &servers); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_sql_server_list: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"SERVER", "RESOURCE GROUP", "LOCATION", "VERSION", "STATE"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, s := range servers {
				tbl.Rows = append(tbl.Rows, []string{s.Name, s.ResourceGroup, s.Location, s.Version, s.State})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d Azure SQL server(s) in account %q.", len(servers), acct.name),
				Table: tbl,
				Raw:   servers,
			}, nil
		},
	}.Build()
}

// newAzureFunctionAppListTool wraps `az functionapp list` — the Azure Functions
// serverless inventory with region and state.
func newAzureFunctionAppListTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": azureAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_functionapp_list",
		Description: "List Azure Function apps with region and running state (read-only `az functionapp list`). Use to inventory serverless functions in a subscription.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"functionapp", "list"}, acct.baseArgs()...)
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_functionapp_list: %w", err)
			}
			var apps []struct {
				Name            string `json:"name"`
				Location        string `json:"location"`
				ResourceGroup   string `json:"resourceGroup"`
				State           string `json:"state"`
				Kind            string `json:"kind"`
				DefaultHostName string `json:"defaultHostName"`
			}
			if err := json.Unmarshal(body, &apps); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_functionapp_list: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"FUNCTION APP", "RESOURCE GROUP", "LOCATION", "STATE", "HOST"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, app := range apps {
				tbl.Rows = append(tbl.Rows, []string{app.Name, app.ResourceGroup, app.Location, app.State, app.DefaultHostName})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d Function app(s) in account %q.", len(apps), acct.name),
				Table: tbl,
				Raw:   apps,
			}, nil
		},
	}.Build()
}

// newAzureAKSListTool wraps `az aks list` — the AKS managed-k8s inventory with
// version, node count, and provisioning/power state.
func newAzureAKSListTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": azureAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_aks_list",
		Description: "List AKS clusters with Kubernetes version, node count, and state (read-only `az aks list`). Use to inventory managed Kubernetes clusters in a subscription.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"aks", "list"}, acct.baseArgs()...)
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_aks_list: %w", err)
			}
			var clusters []struct {
				Name              string `json:"name"`
				Location          string `json:"location"`
				ResourceGroup     string `json:"resourceGroup"`
				KubernetesVersion string `json:"kubernetesVersion"`
				ProvisioningState string `json:"provisioningState"`
				PowerState        struct {
					Code string `json:"code"`
				} `json:"powerState"`
				AgentPoolProfiles []struct {
					Count int `json:"count"`
				} `json:"agentPoolProfiles"`
			}
			if err := json.Unmarshal(body, &clusters); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_aks_list: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"CLUSTER", "RESOURCE GROUP", "LOCATION", "VERSION", "NODES", "POWER", "STATE"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignLeft, render.AlignLeft},
			}
			for _, c := range clusters {
				nodes := 0
				for _, p := range c.AgentPoolProfiles {
					nodes += p.Count
				}
				tbl.Rows = append(tbl.Rows, []string{
					c.Name, c.ResourceGroup, c.Location, c.KubernetesVersion,
					strconv.Itoa(nodes), c.PowerState.Code, c.ProvisioningState,
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d AKS cluster(s) in account %q.", len(clusters), acct.name),
				Table: tbl,
				Raw:   clusters,
			}, nil
		},
	}.Build()
}
