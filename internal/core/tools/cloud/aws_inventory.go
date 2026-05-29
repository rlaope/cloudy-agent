package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// newAWSInventoryTools builds the AWS read-only inventory / managed-service
// health tools (RDS instances, Lambda functions, EKS clusters).
func newAWSInventoryTools(accts map[string]*awsAccount) []tools.Tool {
	return []tools.Tool{
		newAWSRDSDescribeInstancesTool(accts),
		newAWSLambdaListFunctionsTool(accts),
		newAWSEKSListClustersTool(accts),
	}
}

// newAWSRDSDescribeInstancesTool wraps `aws rds describe-db-instances` — the
// managed-database inventory with per-instance health and engine version.
func newAWSRDSDescribeInstancesTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": awsAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_rds_describe_instances",
		Description: "List RDS database instances with status, engine, and class (read-only `aws rds describe-db-instances`). Use to inventory managed databases and spot an instance that is not 'available'.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"rds", "describe-db-instances"}, acct.baseArgs()...)
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_rds_describe_instances: %w", err)
			}
			var parsed struct {
				DBInstances []struct {
					DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
					DBInstanceClass      string `json:"DBInstanceClass"`
					Engine               string `json:"Engine"`
					EngineVersion        string `json:"EngineVersion"`
					DBInstanceStatus     string `json:"DBInstanceStatus"`
					MultiAZ              bool   `json:"MultiAZ"`
					AvailabilityZone     string `json:"AvailabilityZone"`
				} `json:"DBInstances"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_rds_describe_instances: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"INSTANCE", "ENGINE", "VERSION", "CLASS", "STATUS", "MULTI-AZ", "AZ"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, d := range parsed.DBInstances {
				tbl.Rows = append(tbl.Rows, []string{
					d.DBInstanceIdentifier, d.Engine, d.EngineVersion, d.DBInstanceClass,
					d.DBInstanceStatus, strconv.FormatBool(d.MultiAZ), d.AvailabilityZone,
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d RDS instance(s) in account %q.", len(parsed.DBInstances), acct.name),
				Table: tbl,
				Raw:   parsed.DBInstances,
			}, nil
		},
	}.Build()
}

// newAWSLambdaListFunctionsTool wraps `aws lambda list-functions` — the
// serverless-function inventory with runtime and memory sizing.
func newAWSLambdaListFunctionsTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": awsAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_lambda_list_functions",
		Description: "List Lambda functions with runtime, memory, and last-modified time (read-only `aws lambda list-functions`). Use to inventory serverless functions.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"lambda", "list-functions"}, acct.baseArgs()...)
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_lambda_list_functions: %w", err)
			}
			var parsed struct {
				Functions []struct {
					FunctionName string `json:"FunctionName"`
					Runtime      string `json:"Runtime"`
					MemorySize   int    `json:"MemorySize"`
					Timeout      int    `json:"Timeout"`
					LastModified string `json:"LastModified"`
				} `json:"Functions"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_lambda_list_functions: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"FUNCTION", "RUNTIME", "MEM(MB)", "TIMEOUT(s)", "LAST MODIFIED"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignLeft},
			}
			for _, f := range parsed.Functions {
				tbl.Rows = append(tbl.Rows, []string{
					f.FunctionName, f.Runtime, strconv.Itoa(f.MemorySize), strconv.Itoa(f.Timeout), f.LastModified,
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d Lambda function(s) in account %q.", len(parsed.Functions), acct.name),
				Table: tbl,
				Raw:   parsed.Functions,
			}, nil
		},
	}.Build()
}

// newAWSEKSListClustersTool wraps `aws eks list-clusters` — the managed-k8s
// inventory. Returns cluster names; feed a name to existing k8s tools (or
// `aws eks describe-cluster`, not wired) for deeper inspection.
func newAWSEKSListClustersTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": awsAccountSchema,
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_eks_list_clusters",
		Description: "List EKS cluster names in an AWS account (read-only `aws eks list-clusters`). Use to inventory managed Kubernetes clusters.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			cmd := append([]string{"eks", "list-clusters"}, acct.baseArgs()...)
			body, err := CloudExec(ctx, "aws", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_eks_list_clusters: %w", err)
			}
			var parsed struct {
				Clusters []string `json:"clusters"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_eks_list_clusters: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"CLUSTER"},
				Aligns:  []render.Align{render.AlignLeft},
			}
			for _, c := range parsed.Clusters {
				tbl.Rows = append(tbl.Rows, []string{c})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d EKS cluster(s) in account %q.", len(parsed.Clusters), acct.name),
				Table: tbl,
				Raw:   parsed.Clusters,
			}, nil
		},
	}.Build()
}
