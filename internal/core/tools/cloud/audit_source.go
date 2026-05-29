package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// auditMaxEvents caps how many audit records each provider returns per call so
// a busy account cannot flood the change timeline.
const auditMaxEvents = 50

// auditChangeSource folds cloud control-plane audit events onto the
// change.recent timeline as ChangeEvents whose Kind is "cloud_audit" and Source
// identifies the provider ("cloud_audit_aws" / "cloud_audit_azure" /
// "cloud_audit_gcp"). It draws from whichever providers are configured:
//
//   - AWS CloudTrail (`cloudtrail lookup-events`), filtered server-side to the
//     workload via the ResourceName lookup attribute.
//   - GCP Cloud Audit Logs (reuses `gcloud logging read` with a
//     cloudaudit.googleapis.com + protoPayload.resourceName filter).
//   - Azure Activity Log (`monitor activity-log list`), filtered client-side by
//     matching the workload against the resource id / operation (the CLI has no
//     free-text resource-name filter).
//
// Each provider is queried independently; a provider error is tolerated as long
// as another yields events. The source surfaces *who changed what, when* in the
// cloud control plane — the cloud counterpart to k8s/docker rollout history.
type auditChangeSource struct {
	aws   map[string]*awsAccount
	azure map[string]*azureAccount
	gcp   map[string]*gcpProject
}

// NewAuditChangeSource builds a change.ChangeSource over the configured cloud
// providers' audit logs. It returns nil — so callers can omit the source — when
// no provider is configured.
func NewAuditChangeSource(c Clients) change.ChangeSource {
	if c.Empty() {
		return nil
	}
	return &auditChangeSource{aws: c.AWS, azure: c.Azure, gcp: c.GCP}
}

func (s *auditChangeSource) Name() string { return "cloud_audit" }

// RecentChanges queries each configured provider's audit log for q.Workload
// over [now-Since, now] (default 24h) and merges the results newest-first. A
// per-provider failure is collected, not fatal: an error is returned only when
// every configured provider failed and none produced events.
func (s *auditChangeSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	if err := safeArg("workload", q.Workload); err != nil {
		return nil, err
	}
	end := time.Now()
	window := q.Since
	if window <= 0 {
		window = 24 * time.Hour
	}
	start := end.Add(-window)

	var groups [][]change.ChangeEvent
	var failures []string

	if len(s.aws) > 0 {
		if evs, err := s.awsCloudTrail(ctx, q.Workload, start, end); err != nil {
			failures = append(failures, "aws: "+err.Error())
		} else {
			groups = append(groups, evs)
		}
	}
	if len(s.gcp) > 0 {
		if evs, err := s.gcpAuditLogs(ctx, q.Workload, window); err != nil {
			failures = append(failures, "gcp: "+err.Error())
		} else {
			groups = append(groups, evs)
		}
	}
	if len(s.azure) > 0 {
		if evs, err := s.azureActivityLog(ctx, q.Workload, start, end); err != nil {
			failures = append(failures, "azure: "+err.Error())
		} else {
			groups = append(groups, evs)
		}
	}

	merged := change.MergeSorted(0, groups...)
	if len(merged) == 0 && len(failures) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return merged, nil
}

// awsCloudTrail runs `aws cloudtrail lookup-events` filtered to the workload via
// the ResourceName lookup attribute and converts the management events.
func (s *auditChangeSource) awsCloudTrail(ctx context.Context, workload string, start, end time.Time) ([]change.ChangeEvent, error) {
	_, acct, err := tools.PickDefaultEndpoint(s.aws, "change", "aws account")
	if err != nil {
		return nil, err
	}
	cmd := append([]string{"cloudtrail", "lookup-events"}, acct.baseArgs()...)
	cmd = append(cmd,
		"--lookup-attributes", "AttributeKey=ResourceName,AttributeValue="+workload,
		"--start-time", start.UTC().Format(time.RFC3339),
		"--end-time", end.UTC().Format(time.RFC3339),
		"--max-results", strconv.Itoa(auditMaxEvents),
	)
	body, err := CloudExec(ctx, "aws", cmd)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Events []struct {
			EventName string `json:"EventName"`
			Username  string `json:"Username"`
			// CloudTrail renders EventTime as a Unix epoch number under
			// --output json; decode leniently to tolerate a string form too.
			EventTime json.RawMessage `json:"EventTime"`
		} `json:"Events"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]change.ChangeEvent, 0, len(parsed.Events))
	for _, e := range parsed.Events {
		summary := e.EventName
		if e.Username != "" {
			summary = fmt.Sprintf("%s by %s", e.EventName, e.Username)
		}
		out = append(out, change.ChangeEvent{
			Time:    parseEpochOrRFC3339(e.EventTime),
			Kind:    "cloud_audit",
			Target:  workload,
			Summary: summary,
			Source:  "cloud_audit_aws",
		})
	}
	return out, nil
}

// gcpAuditLogs reuses `gcloud logging read` (already allowlisted) with a Cloud
// Audit Logs filter scoped to the workload's resource name.
func (s *auditChangeSource) gcpAuditLogs(ctx context.Context, workload string, window time.Duration) ([]change.ChangeEvent, error) {
	_, proj, err := tools.PickDefaultEndpoint(s.gcp, "change", "gcp project")
	if err != nil {
		return nil, err
	}
	cmd := append([]string{"logging", "read"}, proj.baseArgs()...)
	cmd = append(cmd,
		"--limit", strconv.Itoa(auditMaxEvents),
		"--freshness", durationToGCPFreshness(window),
		// Trailing positional filter (keeps the allowlist prefix "logging read").
		fmt.Sprintf(`logName:"cloudaudit.googleapis.com" AND protoPayload.resourceName:%q`, workload),
	)
	body, err := CloudExec(ctx, "gcloud", cmd)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Timestamp    string `json:"timestamp"`
		ProtoPayload struct {
			MethodName         string `json:"methodName"`
			AuthenticationInfo struct {
				PrincipalEmail string `json:"principalEmail"`
			} `json:"authenticationInfo"`
		} `json:"protoPayload"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]change.ChangeEvent, 0, len(entries))
	for _, e := range entries {
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)
		summary := e.ProtoPayload.MethodName
		if email := e.ProtoPayload.AuthenticationInfo.PrincipalEmail; email != "" {
			summary = fmt.Sprintf("%s by %s", e.ProtoPayload.MethodName, email)
		}
		out = append(out, change.ChangeEvent{
			Time:    ts,
			Kind:    "cloud_audit",
			Target:  workload,
			Summary: summary,
			Source:  "cloud_audit_gcp",
		})
	}
	return out, nil
}

// azureActivityLog runs `az monitor activity-log list` over the window and
// filters client-side: the CLI has no free-text resource-name filter, so a
// record is kept when the workload appears in its resource id or operation.
func (s *auditChangeSource) azureActivityLog(ctx context.Context, workload string, start, end time.Time) ([]change.ChangeEvent, error) {
	_, acct, err := tools.PickDefaultEndpoint(s.azure, "change", "azure account")
	if err != nil {
		return nil, err
	}
	cmd := append([]string{"monitor", "activity-log", "list"}, acct.baseArgs()...)
	cmd = append(cmd,
		"--start-time", start.UTC().Format(time.RFC3339),
		"--end-time", end.UTC().Format(time.RFC3339),
		"--max-events", strconv.Itoa(auditMaxEvents),
	)
	body, err := CloudExec(ctx, "az", cmd)
	if err != nil {
		return nil, err
	}
	var events []struct {
		EventTimestamp string `json:"eventTimestamp"`
		ResourceID     string `json:"resourceId"`
		Caller         string `json:"caller"`
		OperationName  struct {
			LocalizedValue string `json:"localizedValue"`
			Value          string `json:"value"`
		} `json:"operationName"`
	}
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]change.ChangeEvent, 0)
	for _, e := range events {
		op := e.OperationName.LocalizedValue
		if op == "" {
			op = e.OperationName.Value
		}
		// Client-side workload match against the resource id or operation name.
		if !strings.Contains(e.ResourceID, workload) && !strings.Contains(op, workload) {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, e.EventTimestamp)
		summary := op
		if e.Caller != "" {
			summary = fmt.Sprintf("%s by %s", op, e.Caller)
		}
		out = append(out, change.ChangeEvent{
			Time:    ts,
			Kind:    "cloud_audit",
			Target:  workload,
			Summary: summary,
			Source:  "cloud_audit_azure",
		})
	}
	return out, nil
}

// parseEpochOrRFC3339 decodes a CloudTrail EventTime, which is a Unix epoch
// number (possibly fractional) under --output json, but tolerates an RFC3339
// string form too. An unparseable value yields the zero time.
func parseEpochOrRFC3339(raw json.RawMessage) time.Time {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			if t, err := time.Parse(time.RFC3339, str); err == nil {
				return t.UTC()
			}
		}
		return time.Time{}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec).UTC()
	}
	return time.Time{}
}

// durationToGCPFreshness renders a window as a gcloud --freshness value (whole
// seconds, e.g. "3600s"), clamped to at least 1s so a zero window is valid.
func durationToGCPFreshness(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs < 1 {
		secs = 1
	}
	return strconv.FormatInt(secs, 10) + "s"
}
