package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// newAzureCostTools builds the Azure read-only FinOps / cost tools.
func newAzureCostTools(accts map[string]*azureAccount) []tools.Tool {
	return []tools.Tool{
		newAzureConsumptionUsageTool(accts),
	}
}

// newAzureConsumptionUsageTool wraps `az consumption usage list` — per-resource
// usage detail records with pre-tax cost, the Azure read for cost-anomaly
// inquiry. An optional date window narrows the records; both dates must be set
// together (the CLI requires the pair).
func newAzureConsumptionUsageTool(accts map[string]*azureAccount) tools.Tool {
	type args struct {
		Account   string `json:"account"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Top       int    `json:"top"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":    azureAccountSchema,
			"start_date": map[string]any{"type": "string", "description": "Start date \"YYYY-MM-DD\" (optional; must be paired with end_date)."},
			"end_date":   map[string]any{"type": "string", "description": "End date \"YYYY-MM-DD\" (optional; must be paired with start_date)."},
			"top":        map[string]any{"type": "integer", "description": "Max usage records to return (default 50, max 1000).", "default": 50, "minimum": 1, "maximum": 1000},
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.azure_consumption_usage",
		Description: "List Azure consumption usage detail records with pre-tax cost (read-only `az consumption usage list`). Provide an optional start_date/end_date pair for cost-anomaly inquiry over a subscription.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAzure(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if (a.StartDate == "") != (a.EndDate == "") {
				return tools.Observation{}, fmt.Errorf("cloud.azure_consumption_usage: start_date and end_date must be set together")
			}
			if a.Top <= 0 || a.Top > 1000 {
				a.Top = 50
			}
			cmd := append([]string{"consumption", "usage", "list"}, acct.baseArgs()...)
			cmd = append(cmd, "--top", strconv.Itoa(a.Top))
			if a.StartDate != "" {
				if err := safeArg("start_date", a.StartDate); err != nil {
					return tools.Observation{}, err
				}
				if err := safeArg("end_date", a.EndDate); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--start-date", a.StartDate, "--end-date", a.EndDate)
			}
			body, err := CloudExec(ctx, "az", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_consumption_usage: %w", err)
			}
			var usages []struct {
				InstanceName string `json:"instanceName"`
				PretaxCost   string `json:"pretaxCost"`
				Currency     string `json:"currency"`
				UsageStart   string `json:"usageStart"`
				MeterDetails struct {
					MeterName     string `json:"meterName"`
					MeterCategory string `json:"meterCategory"`
				} `json:"meterDetails"`
			}
			if err := json.Unmarshal(body, &usages); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.azure_consumption_usage: decode: %w", err)
			}
			var total float64
			currency := ""
			tbl := &render.Table{
				Headers: []string{"USAGE START", "METER", "INSTANCE", "COST", "CURRENCY"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignLeft},
			}
			for _, u := range usages {
				if c, err := strconv.ParseFloat(u.PretaxCost, 64); err == nil {
					total += c
				}
				if currency == "" {
					currency = u.Currency
				}
				meter := u.MeterDetails.MeterName
				if u.MeterDetails.MeterCategory != "" {
					meter = u.MeterDetails.MeterCategory + "/" + meter
				}
				tbl.Rows = append(tbl.Rows, []string{u.UsageStart, meter, u.InstanceName, u.PretaxCost, u.Currency})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d usage record(s), pre-tax total %.2f %s for account %q.", len(usages), total, currency, acct.name),
				Table: tbl,
				Raw:   usages,
			}, nil
		},
	}.Build()
}
