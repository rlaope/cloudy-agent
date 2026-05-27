package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewListCronJobsTool returns the k8s.list_cronjobs tool.
func NewListCronJobsTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[batchv1.CronJob]{
		Name:        "k8s.list_cronjobs",
		Description: "List Kubernetes CronJobs (batch/v1) in a namespace with schedule, suspend state, last-schedule time, and age.",
		Schema: schema(withContextProp(map[string]any{
			"namespace":      strProp("Namespace to list cron jobs in. Empty string means all namespaces."),
			"label_selector": strProp("Label selector."),
			"field_selector": strProp("Field selector."),
			"limit":          intProp("Maximum number of cron jobs to return (0 = server default)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "SCHEDULE", "SUSPEND", "LAST SCHEDULE", "AGE"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *k8sclient.Client, a listArgs, opts metav1.ListOptions) ([]batchv1.CronJob, any, error) {
			list, err := client.CronJobs(a.Namespace, opts)
			if err != nil {
				return nil, nil, err
			}
			return list.Items, list, nil
		},
		ProjectRow: func(c batchv1.CronJob) []string {
			suspend := false
			if c.Spec.Suspend != nil {
				suspend = *c.Spec.Suspend
			}
			lastSched := "<none>"
			if c.Status.LastScheduleTime != nil {
				lastSched = formatAge(time.Since(c.Status.LastScheduleTime.Time))
			}
			age := ""
			if !c.CreationTimestamp.IsZero() {
				age = formatAge(time.Since(c.CreationTimestamp.Time))
			}
			return []string{
				c.Namespace, c.Name, c.Spec.Schedule,
				strconv.FormatBool(suspend),
				lastSched, age,
			}
		},
		Summary: func(items []batchv1.CronJob, a listArgs) string {
			return fmt.Sprintf("%d cronjob(s) in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}
