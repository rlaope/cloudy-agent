package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// sqsDefaultLimit bounds how many queues are attributed (and shown). Each queue
// costs one extra `aws sqs get-queue-attributes` process, so the default keeps
// the call count modest; narrow with a name prefix to target a service.
const sqsDefaultLimit = 20

// newAWSQueueTools builds the AWS read-only messaging tools (SQS queue depth).
func newAWSQueueTools(accts map[string]*awsAccount) []tools.Tool {
	return []tools.Tool{
		newAWSSQSQueueDepthTool(accts),
	}
}

// sqsQueueRow is one queue's depth view.
type sqsQueueRow struct {
	name     string
	visible  int64 // ApproximateNumberOfMessages — the backlog waiting to be received
	inFlight int64 // ApproximateNumberOfMessagesNotVisible — received, not yet deleted (being processed)
	delayed  int64 // ApproximateNumberOfMessagesDelayed
	err      string
}

// newAWSSQSQueueDepthTool wraps `aws sqs list-queues` + `get-queue-attributes`
// into a ranked queue-depth view. SQS has no consumer-count concept, so the
// proxy for "nothing is draining it" is a visible backlog with zero in-flight
// messages — no consumer has received any.
func newAWSSQSQueueDepthTool(accts map[string]*awsAccount) tools.Tool {
	type args struct {
		Account string `json:"account"`
		Prefix  string `json:"prefix"`
		Limit   int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account": awsAccountSchema,
			"prefix":  map[string]any{"type": "string", "description": "Only queues whose name starts with this prefix; narrows the listing (and the per-queue attribute calls)."},
			"limit":   map[string]any{"type": "integer", "description": "Maximum number of queues to attribute and show, ranked by backlog; default 20. Each queue costs one extra get-queue-attributes call."},
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.aws_sqs_queue_depth",
		Description: "Inspect SQS queue depth in real time (read-only `aws sqs list-queues` + `get-queue-attributes`). Ranks queues by visible backlog, shows in-flight (received, not yet deleted) and delayed counts, and flags a backlog with zero in-flight as NO IN-FLIGHT (nothing is draining it). Narrow with a name prefix; each shown queue costs one extra attribute call. Read-only — never sends, receives, or deletes messages.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			acct, err := pickAWS(accts, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Prefix != "" {
				if err := safeArg("prefix", a.Prefix); err != nil {
					return tools.Observation{}, err
				}
			}
			limit := a.Limit
			if limit <= 0 {
				limit = sqsDefaultLimit
			}

			listCmd := append([]string{"sqs", "list-queues"}, acct.baseArgs()...)
			if a.Prefix != "" {
				listCmd = append(listCmd, "--queue-name-prefix", a.Prefix)
			}
			listBody, err := CloudExec(ctx, "aws", listCmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_sqs_queue_depth: list-queues: %w", err)
			}
			var listed struct {
				QueueUrls []string `json:"QueueUrls"`
			}
			if err := json.Unmarshal(listBody, &listed); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.aws_sqs_queue_depth: decode list-queues: %w", err)
			}

			total := len(listed.QueueUrls)
			urls := listed.QueueUrls
			if len(urls) > limit {
				urls = urls[:limit]
			}

			rows := make([]sqsQueueRow, 0, len(urls))
			for _, u := range urls {
				rows = append(rows, attributeQueue(ctx, acct, u))
			}
			return tools.Observation{
				Text:  renderSQSDepth(acct.name, rows, total, len(urls)),
				Table: sqsTable(rows),
				Raw:   rows,
			}, nil
		},
	}.Build()
}

// attributeQueue reads one queue's approximate message counts. A per-queue
// failure becomes a row carrying the error rather than aborting the whole view
// — one unreadable queue must not blank out the rest.
func attributeQueue(ctx context.Context, acct *awsAccount, queueURL string) sqsQueueRow {
	row := sqsQueueRow{name: queueNameFromURL(queueURL)}
	cmd := append([]string{"sqs", "get-queue-attributes"}, acct.baseArgs()...)
	cmd = append(cmd, "--queue-url", queueURL, "--attribute-names",
		"ApproximateNumberOfMessages",
		"ApproximateNumberOfMessagesNotVisible",
		"ApproximateNumberOfMessagesDelayed",
	)
	body, err := CloudExec(ctx, "aws", cmd)
	if err != nil {
		row.err = err.Error()
		return row
	}
	var parsed struct {
		Attributes map[string]string `json:"Attributes"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		row.err = "decode attributes: " + err.Error()
		return row
	}
	row.visible = atoiQuiet(parsed.Attributes["ApproximateNumberOfMessages"])
	row.inFlight = atoiQuiet(parsed.Attributes["ApproximateNumberOfMessagesNotVisible"])
	row.delayed = atoiQuiet(parsed.Attributes["ApproximateNumberOfMessagesDelayed"])
	return row
}

// sqsFlag returns the NO IN-FLIGHT flag for a queue with a backlog but nothing
// being processed; "" otherwise.
func sqsFlag(r sqsQueueRow) string {
	if r.err == "" && r.visible > 0 && r.inFlight == 0 {
		return "NO IN-FLIGHT"
	}
	return ""
}

// renderSQSDepth produces the leading summary line; the table carries the rows.
func renderSQSDepth(account string, rows []sqsQueueRow, total, attributed int) string {
	var stuck int
	for _, r := range rows {
		if sqsFlag(r) != "" {
			stuck++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d SQS queue(s) in account %q", total, account)
	if attributed < total {
		// Truncation is by listing order, NOT by backlog, so the busiest queue
		// could itself be beyond the cut. Say so plainly rather than implying
		// full coverage.
		fmt.Fprintf(&b, " — attributed the first %d by listing order; narrow with prefix so the busiest are covered", attributed)
	}
	if stuck > 0 {
		fmt.Fprintf(&b, "\n⚠ %d of the %d shown queue(s) have a backlog and nothing in flight", stuck, attributed)
	}
	return b.String()
}

// sqsTable ranks the rows by visible backlog descending and renders them.
func sqsTable(rows []sqsQueueRow) *render.Table {
	sorted := append([]sqsQueueRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].visible > sorted[j].visible })

	tbl := &render.Table{
		Headers: []string{"QUEUE", "VISIBLE", "IN-FLIGHT", "DELAYED", "FLAG"},
		Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight, render.AlignRight, render.AlignLeft},
	}
	for _, r := range sorted {
		flag := sqsFlag(r)
		if r.err != "" {
			flag = "ERROR: " + r.err
		}
		tbl.Rows = append(tbl.Rows, []string{
			r.name,
			strconv.FormatInt(r.visible, 10),
			strconv.FormatInt(r.inFlight, 10),
			strconv.FormatInt(r.delayed, 10),
			flag,
		})
	}
	return tbl
}

// queueNameFromURL extracts the queue name (the last path segment) from an SQS
// queue URL like https://sqs.us-east-1.amazonaws.com/123456789012/my-queue.
func queueNameFromURL(u string) string {
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndex(u, "/"); i >= 0 && i < len(u)-1 {
		return u[i+1:]
	}
	return u
}

// atoiQuiet parses a non-negative count, returning 0 for empty or unparseable
// input (SQS reports attribute values as decimal strings).
func atoiQuiet(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
