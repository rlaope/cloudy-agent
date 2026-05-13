package jvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/tools"
)

// AsyncProfileTool implements jvm.async_profile.
type AsyncProfileTool struct{}

func NewAsyncProfileTool() *AsyncProfileTool { return &AsyncProfileTool{} }

func (t *AsyncProfileTool) Name() string        { return "jvm.async_profile" }
func (t *AsyncProfileTool) ReadOnly() bool      { return true }
func (t *AsyncProfileTool) Description() string {
	return "Profile a local JVM process with async-profiler. Requires CLOUDY_ASYNC_PROFILER env var pointing to profiler.sh."
}
func (t *AsyncProfileTool) Schema() json.RawMessage {
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pid": map[string]any{
				"type":        "integer",
				"description": "PID of the local JVM process.",
				"minimum":     1,
			},
			"duration_seconds": map[string]any{
				"type":        "integer",
				"description": "Profiling duration in seconds (default: 15, max: 60).",
				"default":     15,
				"minimum":     1,
				"maximum":     60,
			},
			"format": map[string]any{
				"type":        "string",
				"description": `Output format: "text" or "svg" (default: "text").`,
				"enum":        []string{"text", "svg"},
				"default":     "text",
			},
			"event": map[string]any{
				"type":        "string",
				"description": `Profiling event: "cpu" or "alloc" (default: "cpu").`,
				"enum":        []string{"cpu", "alloc"},
				"default":     "cpu",
			},
		},
		"required": []string{"pid"},
	}
	b, _ := json.Marshal(s)
	return b
}

// AsyncProfilerEnvVar is the environment variable that must point to profiler.sh.
const AsyncProfilerEnvVar = "CLOUDY_ASYNC_PROFILER"

func (t *AsyncProfileTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		PID             int    `json:"pid"`
		DurationSeconds int    `json:"duration_seconds"`
		Format          string `json:"format"`
		Event           string `json:"event"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.async_profile: parse args: %w", err)
	}
	if a.PID < 1 {
		return tools.Observation{}, fmt.Errorf("jvm.async_profile: pid must be >= 1")
	}
	if a.DurationSeconds <= 0 {
		a.DurationSeconds = 15
	}
	if a.DurationSeconds > 60 {
		a.DurationSeconds = 60
	}
	if a.Format == "" {
		a.Format = "text"
	}
	if a.Event == "" {
		a.Event = "cpu"
	}

	profilerSh := os.Getenv(AsyncProfilerEnvVar)
	if profilerSh == "" {
		return tools.Observation{}, &MissingEnvError{Var: AsyncProfilerEnvVar}
	}

	ext := a.Format
	if a.Format == "svg" {
		ext = "svg"
	} else {
		ext = "txt"
	}
	outFile := fmt.Sprintf("/tmp/cloudy-prof-%d-%d.%s", a.PID, time.Now().UnixNano(), ext)

	pid := strconv.Itoa(a.PID)
	dur := strconv.Itoa(a.DurationSeconds)

	out, _, err := runner(ctx, profilerSh,
		"-d", dur,
		"-e", a.Event,
		"-o", a.Format,
		"-f", outFile,
		pid,
	)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("jvm.async_profile: profiler.sh: %w", err)
	}

	topFrames := extractTopFrames(out, 20)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Profiled pid=%d duration=%ds event=%s format=%s\n", a.PID, a.DurationSeconds, a.Event, a.Format)
	fmt.Fprintf(&sb, "Output file: %s\n\n", outFile)
	if topFrames != "" {
		sb.WriteString("Top frames:\n")
		sb.WriteString(topFrames)
	} else {
		sb.WriteString(out)
	}

	return tools.Observation{
		Text: sb.String(),
		Raw:  outFile,
	}, nil
}

// MissingEnvError is returned when CLOUDY_ASYNC_PROFILER is not set.
type MissingEnvError struct {
	Var string
}

func (e *MissingEnvError) Error() string {
	return fmt.Sprintf("jvm.async_profile: %s environment variable is not set; "+
		"set it to the path of async-profiler's profiler.sh, e.g.: "+
		"export %s=/opt/async-profiler/profiler.sh", e.Var, e.Var)
}

// extractTopFrames pulls the first n non-empty lines from profiler text output
// that look like frame entries (contain %, [ or are indented code lines).
func extractTopFrames(output string, n int) string {
	var result []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(result) >= n {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Include lines that look like frame/sample data.
		if strings.Contains(trimmed, "%") || strings.Contains(trimmed, "samples") ||
			strings.HasPrefix(trimmed, "[") || len(result) > 0 {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}
