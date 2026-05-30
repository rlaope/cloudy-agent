package cloud

import (
	"errors"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// ErrSubcommandNotAllowed is returned by CloudExec when a binary or
// "<service> <verb>" prefix is outside the read-only allowlist.
var ErrSubcommandNotAllowed = errors.New("cloud: sub-command not allowed")

// mustJSON aliases the shared schema marshaller used by every tool group.
var mustJSON = tools.MustJSON

// awsAccount is a validated AWS account handle: the label plus the flags
// CloudExec injects on every call (--region, optional --profile).
type awsAccount struct {
	name    string
	region  string
	profile string
}

// azureAccount is a validated Azure subscription handle.
type azureAccount struct {
	name           string
	subscriptionID string
}

// gcpProject is a validated GCP project handle. Only Cloud Logging is wired —
// GCP metric/trace reads have no clean read-only gcloud command (see RFC).
type gcpProject struct {
	name          string
	projectID     string
	configuration string
}

// Clients holds the per-provider account handles. These are not SDK clients —
// each handle just carries the CLI flags for its account; the actual work is
// an allowlisted CloudExec shell-out at call time.
type Clients struct {
	AWS   map[string]*awsAccount
	Azure map[string]*azureAccount
	GCP   map[string]*gcpProject
}

// Empty reports whether no provider has a usable account wired.
func (c Clients) Empty() bool {
	return len(c.AWS) == 0 && len(c.Azure) == 0 && len(c.GCP) == 0
}

// BuildClients validates the configured cloud accounts into handle maps and
// returns per-entry skip reasons surfaced through the registry. No CLI is
// invoked here — credential resolution happens lazily on the first tool call,
// matching the HTTP groups' deferred-probe convention.
func BuildClients(awsAccts []config.AWSAccount, gcpProjs []config.GCPProject, azAccts []config.AzureAccount) (Clients, []string) {
	cs := Clients{
		AWS:   map[string]*awsAccount{},
		Azure: map[string]*azureAccount{},
		GCP:   map[string]*gcpProject{},
	}
	var skips []string

	for _, a := range awsAccts {
		if a.Name == "" || a.Region == "" {
			skips = append(skips, "cloud: aws account "+strconv.Quote(a.Name)+": missing name or region")
			continue
		}
		cs.AWS[a.Name] = &awsAccount{name: a.Name, region: a.Region, profile: a.Profile}
	}

	for _, a := range azAccts {
		if a.Name == "" || a.SubscriptionID == "" {
			skips = append(skips, "cloud: azure account "+strconv.Quote(a.Name)+": missing name or subscription_id")
			continue
		}
		cs.Azure[a.Name] = &azureAccount{name: a.Name, subscriptionID: a.SubscriptionID}
	}

	// GCP wires Cloud Logging only; metric/trace reads remain deferred (no clean
	// read-only gcloud command — see docs/RFC-CLOUD-OBSERVABILITY.md).
	for _, p := range gcpProjs {
		if p.Name == "" || p.ProjectID == "" {
			skips = append(skips, "cloud: gcp project "+strconv.Quote(p.Name)+": missing name or project_id")
			continue
		}
		cs.GCP[p.Name] = &gcpProject{name: p.Name, projectID: p.ProjectID, configuration: p.Configuration}
	}

	return cs, skips
}

// RegisterAll adds every cloud.* tool whose provider has at least one account.
// When no provider is wired, the "cloud" group is marked skipped with a reason
// composed from any per-account failures.
func RegisterAll(reg *tools.Registry, c Clients, skipReasons []string) {
	if c.Empty() {
		reason := "no cloud accounts configured (cloud_aws / cloud_gcp / cloud_azure)"
		if len(skipReasons) > 0 {
			reason = "no usable cloud accounts: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("cloud", reason)
		return
	}
	if len(c.AWS) > 0 {
		reg.MustRegister(newAWSMetricTools(c.AWS)...)
		reg.MustRegister(newAWSLogTools(c.AWS)...)
		reg.MustRegister(newAWSTraceTools(c.AWS)...)
		reg.MustRegister(newAWSInventoryTools(c.AWS)...)
		reg.MustRegister(newAWSQueueTools(c.AWS)...)
		reg.MustRegister(newAWSCostTools(c.AWS)...)
	}
	if len(c.Azure) > 0 {
		reg.MustRegister(newAzureMetricTools(c.Azure)...)
		reg.MustRegister(newAzureLogTools(c.Azure)...)
		reg.MustRegister(newAzureTraceTools(c.Azure)...)
		reg.MustRegister(newAzureInventoryTools(c.Azure)...)
		reg.MustRegister(newAzureCostTools(c.Azure)...)
	}
	if len(c.GCP) > 0 {
		reg.MustRegister(newGCPLogTools(c.GCP)...)
		reg.MustRegister(newGCPInventoryTools(c.GCP)...)
	}
}
