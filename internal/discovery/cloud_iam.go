package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CloudProvider identifies which cloud the identity belongs to.
type CloudProvider string

const (
	CloudAWS   CloudProvider = "aws"
	CloudGCP   CloudProvider = "gcp"
	CloudAzure CloudProvider = "azure"
)

// CloudIdentity is the effective identity the agent presents to a public
// cloud — what STS / gcloud / az reports today, not what RBAC may further
// narrow inside that cloud. Surfaced in /setup so users can verify that the
// credentials cloudy will use match the account they expect.
type CloudIdentity struct {
	Provider CloudProvider

	// Principal is the user-facing identity string:
	//   AWS:   "arn:aws:iam::123456789012:user/alice"
	//   GCP:   "alice@example.com"
	//   Azure: "alice@example.com"
	Principal string

	// Account is the tenant / project / account identifier:
	//   AWS:   "123456789012"
	//   GCP:   "my-project-id"
	//   Azure: "subscription-uuid"
	Account string

	// Available reports whether the underlying CLI / credentials are present.
	// false with Reason="..." means cloudy could not authenticate to that
	// cloud; the wizard renders this as an informational row, not an error.
	Available bool

	// Reason carries the failure message when Available is false.
	Reason string
}

// cloudProbeTimeout caps each shell-out so a hung CLI doesn't stall /setup.
const cloudProbeTimeout = 5 * time.Second

// cloudProbeRunner is the subprocess runner used by every cloud probe. It is
// a package variable so tests can swap it for a stub without invoking the
// real aws/gcloud/az binaries.
var cloudProbeRunner = runCloudCmd

func runCloudCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(errOut.String())
		if stderr != "" {
			return nil, fmt.Errorf("%w: %s", err, stderr)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

// ProbeCloudIdentities runs the AWS / GCP / Azure identity probes concurrently
// and returns one CloudIdentity per cloud in a stable order (aws, gcp, azure).
// Every probe is bounded by cloudProbeTimeout; a missing CLI or absent
// credentials surfaces as Available=false with the reason, never as a panic
// or returned error. The function is best-effort: cloudy still works without
// any cloud credentials present.
func ProbeCloudIdentities(ctx context.Context) []CloudIdentity {
	var wg sync.WaitGroup
	results := make([]CloudIdentity, 3)

	wg.Add(3)
	go func() {
		defer wg.Done()
		results[0] = probeAWS(ctx)
	}()
	go func() {
		defer wg.Done()
		results[1] = probeGCP(ctx)
	}()
	go func() {
		defer wg.Done()
		results[2] = probeAzure(ctx)
	}()
	wg.Wait()
	return results
}

func probeAWS(ctx context.Context) CloudIdentity {
	id := CloudIdentity{Provider: CloudAWS}
	pCtx, cancel := context.WithTimeout(ctx, cloudProbeTimeout)
	defer cancel()
	out, err := cloudProbeRunner(pCtx, "aws", "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		id.Reason = err.Error()
		return id
	}
	var parsed struct {
		UserID  string `json:"UserId"`
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		id.Reason = fmt.Sprintf("parse aws output: %v", jerr)
		return id
	}
	if parsed.Arn == "" {
		id.Reason = "empty aws sts response"
		return id
	}
	id.Available = true
	id.Principal = parsed.Arn
	id.Account = parsed.Account
	return id
}

func probeGCP(ctx context.Context) CloudIdentity {
	id := CloudIdentity{Provider: CloudGCP}
	pCtx, cancel := context.WithTimeout(ctx, cloudProbeTimeout)
	defer cancel()

	out, err := cloudProbeRunner(pCtx, "gcloud", "config", "list", "--format=json")
	if err != nil {
		id.Reason = err.Error()
		return id
	}
	var parsed struct {
		Core struct {
			Account string `json:"account"`
			Project string `json:"project"`
		} `json:"core"`
	}
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		id.Reason = fmt.Sprintf("parse gcloud output: %v", jerr)
		return id
	}
	if parsed.Core.Account == "" {
		id.Reason = "no active gcloud account"
		return id
	}
	id.Available = true
	id.Principal = parsed.Core.Account
	id.Account = parsed.Core.Project
	return id
}

func probeAzure(ctx context.Context) CloudIdentity {
	id := CloudIdentity{Provider: CloudAzure}
	pCtx, cancel := context.WithTimeout(ctx, cloudProbeTimeout)
	defer cancel()

	out, err := cloudProbeRunner(pCtx, "az", "account", "show", "--output", "json")
	if err != nil {
		id.Reason = err.Error()
		return id
	}
	var parsed struct {
		ID   string `json:"id"`
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		id.Reason = fmt.Sprintf("parse az output: %v", jerr)
		return id
	}
	if parsed.ID == "" {
		id.Reason = "no active azure subscription"
		return id
	}
	id.Available = true
	id.Principal = parsed.User.Name
	id.Account = parsed.ID
	return id
}

// AvailableIdentities filters out unavailable ids — handy for callers (the
// /setup wizard's overview row) that only want to render successful probes.
func AvailableIdentities(ids []CloudIdentity) []CloudIdentity {
	out := make([]CloudIdentity, 0, len(ids))
	for _, id := range ids {
		if id.Available {
			out = append(out, id)
		}
	}
	return out
}

// ErrNoCloudIdentity is returned by some callers that require at least one
// cloud identity to proceed. ProbeCloudIdentities itself never returns errors.
var ErrNoCloudIdentity = errors.New("discovery: no cloud identity available")
