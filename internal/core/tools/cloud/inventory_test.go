package cloud

import (
	"context"
	"strings"
	"testing"
)

func TestAWSRDSDescribeInstances_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"DBInstances":[{"DBInstanceIdentifier":"db1","DBInstanceClass":"db.t3.medium","Engine":"postgres","EngineVersion":"15.4","DBInstanceStatus":"available","MultiAZ":true,"AvailabilityZone":"us-east-1a"}]}`)

	tool := newAWSRDSDescribeInstancesTool(oneAWS())
	obs := runTool(t, tool, `{}`)

	if args[0] != "rds" || args[1] != "describe-db-instances" {
		t.Errorf("command path = %v, want rds describe-db-instances", args[:2])
	}
	if !hasFlag(args, "--region", "us-east-1") || !hasFlag(args, "--output", "json") {
		t.Errorf("per-account flags missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[0] != "db1" || row[1] != "postgres" || row[4] != "available" || row[5] != "true" {
		t.Errorf("unexpected row: %v", row)
	}
}

func TestAWSLambdaListFunctions_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`{"Functions":[{"FunctionName":"fn1","Runtime":"go1.x","MemorySize":256,"Timeout":30,"LastModified":"2026-05-01T00:00:00Z"}]}`)

	tool := newAWSLambdaListFunctionsTool(oneAWS())
	obs := runTool(t, tool, `{}`)

	if args[0] != "lambda" || args[1] != "list-functions" {
		t.Errorf("command path = %v, want lambda list-functions", args[:2])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][2] != "256" {
		t.Errorf("unexpected memory size: %+v", obs.Table)
	}
}

func TestAWSEKSListClusters_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args, `{"clusters":["a","b","c"]}`)

	tool := newAWSEKSListClustersTool(oneAWS())
	obs := runTool(t, tool, `{}`)

	if args[0] != "eks" || args[1] != "list-clusters" {
		t.Errorf("command path = %v, want eks list-clusters", args[:2])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 3 {
		t.Errorf("expected 3 clusters, got %+v", obs.Table)
	}
}

func TestGCPSQLInstancesList_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"name":"sql1","databaseVersion":"POSTGRES_15","region":"us-central1","state":"RUNNABLE","settings":{"tier":"db-custom-2-7680"}}]`)

	tool := newGCPSQLInstancesListTool(oneGCP())
	obs := runTool(t, tool, `{}`)

	if got := subcommandPrefix(args); got != "sql instances list" {
		t.Errorf("allowlist prefix = %q, want %q", got, "sql instances list")
	}
	if !hasFlag(args, "--project", "proj-id") || !hasFlag(args, "--format", "json") {
		t.Errorf("per-project flags missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[0] != "sql1" || row[1] != "POSTGRES_15" || row[2] != "db-custom-2-7680" || row[4] != "RUNNABLE" {
		t.Errorf("unexpected row: %v", row)
	}
}

func TestGCPRunServicesList_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"metadata":{"name":"svc1","labels":{"cloud.googleapis.com/location":"us-central1"}},"status":{"url":"https://svc1.run.app"}}]`)

	tool := newGCPRunServicesListTool(oneGCP())
	obs := runTool(t, tool, `{}`)

	if got := subcommandPrefix(args); got != "run services list" {
		t.Errorf("allowlist prefix = %q, want %q", got, "run services list")
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	if row[0] != "svc1" || row[1] != "us-central1" || row[2] != "https://svc1.run.app" {
		t.Errorf("unexpected row: %v", row)
	}
}

func TestGCPContainerClustersList_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"name":"gke1","location":"us-central1","status":"RUNNING","currentMasterVersion":"1.29.4","currentNodeCount":6}]`)

	tool := newGCPContainerClustersListTool(oneGCP())
	obs := runTool(t, tool, `{}`)

	if got := subcommandPrefix(args); got != "container clusters list" {
		t.Errorf("allowlist prefix = %q, want %q", got, "container clusters list")
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][3] != "6" {
		t.Errorf("unexpected node count: %+v", obs.Table)
	}
}

func TestAzureSQLServerList_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"name":"sqlsrv1","location":"eastus","resourceGroup":"rg1","state":"Ready","version":"12.0"}]`)

	tool := newAzureSQLServerListTool(oneAzure())
	obs := runTool(t, tool, `{}`)

	if got := subcommandPrefix(args); got != "sql server list" {
		t.Errorf("allowlist prefix = %q, want %q", got, "sql server list")
	}
	if !hasFlag(args, "--subscription", "sub-123") {
		t.Errorf("subscription flag missing: %v", args)
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][0] != "sqlsrv1" {
		t.Errorf("unexpected row: %+v", obs.Table)
	}
}

func TestAzureFunctionAppList_ArgvAndParse(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"name":"fnapp1","location":"eastus","resourceGroup":"rg1","state":"Running","kind":"functionapp","defaultHostName":"fnapp1.azurewebsites.net"}]`)

	tool := newAzureFunctionAppListTool(oneAzure())
	obs := runTool(t, tool, `{}`)

	if args[0] != "functionapp" || args[1] != "list" {
		t.Errorf("command path = %v, want functionapp list", args[:2])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 || obs.Table.Rows[0][3] != "Running" {
		t.Errorf("unexpected row: %+v", obs.Table)
	}
}

func TestAzureAKSList_ArgvAndParseAndNodeSum(t *testing.T) {
	var args []string
	stubRunner(t, nil, &args,
		`[{"name":"aks1","location":"eastus","resourceGroup":"rg1","kubernetesVersion":"1.29.2","provisioningState":"Succeeded","powerState":{"code":"Running"},"agentPoolProfiles":[{"count":3},{"count":2}]}]`)

	tool := newAzureAKSListTool(oneAzure())
	obs := runTool(t, tool, `{}`)

	if args[0] != "aks" || args[1] != "list" {
		t.Errorf("command path = %v, want aks list", args[:2])
	}
	if obs.Table == nil || len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", obs.Table)
	}
	row := obs.Table.Rows[0]
	// node count is summed across agent pools: 3 + 2 = 5.
	if row[0] != "aks1" || row[4] != "5" || row[5] != "Running" || row[6] != "Succeeded" {
		t.Errorf("unexpected row: %v", row)
	}
}

// TestInventoryAllowlist_RefusesMutatingVerbs proves the new inventory entries
// did not widen the boundary to any mutating verb on the three binaries.
func TestInventoryAllowlist_RefusesMutatingVerbs(t *testing.T) {
	cases := []struct {
		bin  string
		args []string
	}{
		{"aws", []string{"rds", "delete-db-instance", "--db-instance-identifier", "db1"}},
		{"aws", []string{"eks", "delete-cluster", "--name", "c1"}},
		{"gcloud", []string{"sql", "instances", "delete", "sql1"}},
		{"gcloud", []string{"container", "clusters", "delete", "gke1"}},
		{"az", []string{"aks", "delete", "--name", "aks1"}},
	}
	for _, c := range cases {
		if _, err := CloudExec(context.Background(), c.bin, c.args); err == nil {
			t.Errorf("%s %v: expected refusal, got nil", c.bin, c.args)
		} else if !strings.Contains(err.Error(), "not a read-only allowlisted sub-command") {
			t.Errorf("%s %v: want allowlist refusal, got %v", c.bin, c.args, err)
		}
	}
}
