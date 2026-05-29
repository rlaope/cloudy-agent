package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

func pickAWS(m map[string]*awsAccount, name string) (*awsAccount, error) {
	return tools.PickEndpoint(m, name, "cloud", "aws account")
}

// baseArgs returns the per-account flags injected on every aws CLI call:
// region, JSON output, and (optionally) the named profile.
func (a *awsAccount) baseArgs() []string {
	args := []string{"--region", a.region, "--output", "json"}
	if a.profile != "" {
		args = append(args, "--profile", a.profile)
	}
	return args
}

// safeArg rejects values that would be mis-parsed as CLI flags. argv-only exec
// already blocks shell injection; this guards the remaining flag-injection edge
// (a value like "--debug") for user/LLM-supplied fields.
func safeArg(field, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("cloud: %s value %q must not start with '-'", field, value)
	}
	return nil
}

var awsAccountSchema = map[string]any{
	"type":        "string",
	"description": "Name of the AWS account configured under cloud_aws. Optional if exactly one is configured.",
}

// newAWSMetricTools builds the AWS CloudWatch read-only metric tools.
func newAWSMetricTools(accts map[string]*awsAccount) []tools.Tool {
	return []tools.Tool{
		newAWSListMetricsTool(accts),
		newAWSGetMetricStatisticsTool(accts),
	}
}

// newAWSListMetricsTool wraps `aws cloudwatch list-metrics` — discovery of
// which metrics exist in a namespace and their dimensions.
func newAWSListMetricsTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account    string `json:"account"`
		Namespace  string `json:"namespace"`
		MetricName string `json:"metric_name"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":     awsAccountSchema,
			"namespace":   map[string]any{"type": "string", "description": "CloudWatch namespace to filter by, e.g. \"AWS/EC2\", \"AWS/RDS\" (optional)."},
			"metric_name": map[string]any{"type": "string", "description": "Metric name to filter by, e.g. \"CPUUtilization\" (optional)."},
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_cw_list_metrics",
		Description: "List CloudWatch metrics available in an AWS account (read-only `aws cloudwatch list-metrics`). Use to discover metric names and dimensions before querying statistics.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"cloudwatch", "list-metrics"}, acct.baseArgs()...)
			if a.Namespace != "" {
				if err := safeArg("namespace", a.Namespace); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--namespace", a.Namespace)
			}
			if a.MetricName != "" {
				if err := safeArg("metric_name", a.MetricName); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--metric-name", a.MetricName)
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_cw_list_metrics: %w", err)
			}
			var parsed struct {
				Metrics []struct {
					Namespace  string `json:"Namespace"`
					MetricName string `json:"MetricName"`
					Dimensions []struct {
						Name  string `json:"Name"`
						Value string `json:"Value"`
					} `json:"Dimensions"`
				} `json:"Metrics"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_cw_list_metrics: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"NAMESPACE", "METRIC", "DIMENSIONS"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, m := range parsed.Metrics {
				dims := make([]string, 0, len(m.Dimensions))
				for _, d := range m.Dimensions {
					dims = append(dims, d.Name+"="+d.Value)
				}
				tbl.Rows = append(tbl.Rows, []string{m.Namespace, m.MetricName, strings.Join(dims, ",")})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d CloudWatch metric(s) in account %q.", len(parsed.Metrics), acct.name),
				Table: tbl,
				Raw:   parsed.Metrics,
			}, nil
		},
	}.Build()
}

// newAWSGetMetricStatisticsTool wraps `aws cloudwatch get-metric-statistics` —
// a time-bounded statistic series for one metric. Chosen over get-metric-data
// because its flat flag shape is far easier for the LLM to fill correctly than
// get-metric-data's nested MetricDataQueries JSON.
func newAWSGetMetricStatisticsTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account    string   `json:"account"`
		Namespace  string   `json:"namespace"`
		MetricName string   `json:"metric_name"`
		Dimensions []string `json:"dimensions"`
		StartTime  string   `json:"start_time"`
		EndTime    string   `json:"end_time"`
		Period     int      `json:"period"`
		Statistics []string `json:"statistics"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":     awsAccountSchema,
			"namespace":   map[string]any{"type": "string", "description": "CloudWatch namespace, e.g. \"AWS/EC2\"."},
			"metric_name": map[string]any{"type": "string", "description": "Metric name, e.g. \"CPUUtilization\"."},
			"dimensions":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Dimensions as \"Name=Value\" strings, e.g. [\"InstanceId=i-0abc\"] (optional)."},
			"start_time":  map[string]any{"type": "string", "description": "ISO8601 start, e.g. \"2026-05-29T00:00:00Z\"."},
			"end_time":    map[string]any{"type": "string", "description": "ISO8601 end, e.g. \"2026-05-29T01:00:00Z\"."},
			"period":      map[string]any{"type": "integer", "description": "Granularity in seconds (e.g. 300).", "default": 300},
			"statistics":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Statistics to return, e.g. [\"Average\",\"Maximum\"]. Default [\"Average\"]."},
		},
		"required": []any{"namespace", "metric_name", "start_time", "end_time"},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_cw_get_metric_statistics",
		Description: "Fetch a time-bounded CloudWatch statistic series for one metric (read-only `aws cloudwatch get-metric-statistics`). Provide namespace, metric_name, a time window, and statistics.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Namespace == "" || a.MetricName == "" || a.StartTime == "" || a.EndTime == "" {
				return tools.Observation{}, fmt.Errorf("cloud.aws_cw_get_metric_statistics: namespace, metric_name, start_time, end_time are required")
			}
			if a.Period <= 0 {
				a.Period = 300
			}
			if len(a.Statistics) == 0 {
				a.Statistics = []string{"Average"}
			}
			for field, v := range map[string]string{"namespace": a.Namespace, "metric_name": a.MetricName, "start_time": a.StartTime, "end_time": a.EndTime} {
				if err := safeArg(field, v); err != nil {
					return tools.Observation{}, err
				}
			}
			cmd := append([]string{"cloudwatch", "get-metric-statistics"}, acct.baseArgs()...)
			cmd = append(cmd,
				"--namespace", a.Namespace,
				"--metric-name", a.MetricName,
				"--start-time", a.StartTime,
				"--end-time", a.EndTime,
				"--period", fmt.Sprintf("%d", a.Period),
			)
			cmd = append(cmd, "--statistics")
			for _, s := range a.Statistics {
				if err := safeArg("statistics", s); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, s)
			}
			if len(a.Dimensions) > 0 {
				cmd = append(cmd, "--dimensions")
				for _, d := range a.Dimensions {
					// CloudWatch expects "Name=foo,Value=bar"; accept the simpler
					// "foo=bar" the schema documents and convert it.
					name, value, ok := strings.Cut(d, "=")
					if !ok {
						return tools.Observation{}, fmt.Errorf("cloud.aws_cw_get_metric_statistics: dimension %q must be \"Name=Value\"", d)
					}
					if err := safeArg("dimension", name); err != nil {
						return tools.Observation{}, err
					}
					cmd = append(cmd, fmt.Sprintf("Name=%s,Value=%s", name, value))
				}
			}
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_cw_get_metric_statistics: %w", err)
			}
			var parsed struct {
				Label      string           `json:"Label"`
				Datapoints []map[string]any `json:"Datapoints"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_cw_get_metric_statistics: decode: %w", err)
			}
			return tools.Observation{
				Text: fmt.Sprintf("%s: %d datapoint(s) over [%s, %s] period=%ds.",
					parsed.Label, len(parsed.Datapoints), a.StartTime, a.EndTime, a.Period),
				Raw: parsed,
			}, nil
		},
	}.Build()
}
