package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListJobsTool returns the k8s.list_jobs tool.
func NewListJobsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[batchv1.Job]{
		Name:        "k8s.list_jobs",
		Description: "List Kubernetes Jobs (batch/v1) in a namespace with completion counts, duration, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list jobs in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of jobs to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "COMPLETIONS", "DURATION", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]batchv1.Job, any, error) {
			list, err := client.Jobs(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(j batchv1.Job) []string {
			completions := int32(1)
			if j.Spec.Completions != nil {
				completions = *j.Spec.Completions
			}
			age := ""
			if !j.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(j.CreationTimestamp.Time))
			}
			return []string{
				j.Namespace, j.Name,
				fmt.Sprintf("%d/%d", j.Status.Succeeded, completions),
				jobDuration(j),
				age,
			}
		},
		Summary: func(items []batchv1.Job, a listArgs) string {
			return fmt.Sprintf("%d job(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}

// jobDuration returns "<age>" while running, the elapsed start->completion
// span when finished, or "" when the job has not started yet.
func jobDuration(j batchv1.Job) string {
	if j.Status.StartTime == nil {
		return ""
	}
	end := time.Now()
	if j.Status.CompletionTime != nil {
		end = j.Status.CompletionTime.Time
	}
	return formatAge(end.Sub(j.Status.StartTime.Time))
}
