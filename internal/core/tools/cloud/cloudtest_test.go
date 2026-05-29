package cloud

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// stubRunner swaps cloudExecRunner for one that captures the (bin, args) it was
// called with and returns the supplied canned stdout. The capture pointers let
// a test assert the exact argv cloudy built without invoking a real CLI.
func stubRunner(t *testing.T, gotBin *string, gotArgs *[]string, out string) {
	t.Helper()
	cloudExecRunner = func(_ context.Context, bin string, args []string) ([]byte, error) {
		if gotBin != nil {
			*gotBin = bin
		}
		if gotArgs != nil {
			*gotArgs = args
		}
		return []byte(out), nil
	}
	t.Cleanup(func() { cloudExecRunner = runCloudExec })
}

// runTool drives a tool's Run with a JSON argument object and fails the test on
// error.
func runTool(t *testing.T, tool tools.Tool, argsJSON string) tools.Observation {
	t.Helper()
	obs, err := tool.Run(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("%s.Run(%s) error: %v", tool.Name(), argsJSON, err)
	}
	return obs
}

// runToolErr drives a tool's Run expecting an error, returning it.
func runToolErr(t *testing.T, tool tools.Tool, argsJSON string) error {
	t.Helper()
	_, err := tool.Run(context.Background(), json.RawMessage(argsJSON))
	if err == nil {
		t.Fatalf("%s.Run(%s) expected error, got nil", tool.Name(), argsJSON)
	}
	return err
}

// hasFlag reports whether args contains `flag` immediately followed by `value`.
func hasFlag(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// hasToken reports whether args contains tok anywhere.
func hasToken(args []string, tok string) bool {
	for _, a := range args {
		if a == tok {
			return true
		}
	}
	return false
}

// oneAWS / oneAzure build a single-account handle map so a tool can be called
// without specifying the account argument.
func oneAWS() map[string]*awsAccount {
	return map[string]*awsAccount{"prod": {name: "prod", region: "us-east-1", profile: "p1"}}
}

func oneAzure() map[string]*azureAccount {
	return map[string]*azureAccount{"prod": {name: "prod", subscriptionID: "sub-123"}}
}

func oneGCP() map[string]*gcpProject {
	return map[string]*gcpProject{"prod": {name: "prod", projectID: "proj-id"}}
}
