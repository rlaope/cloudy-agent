// Package gpu provides read-only GPU diagnostic tools for the cloudy SRE agent.
package gpu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// maxOutputBytes caps the combined stdout+stderr captured from any subprocess.
const maxOutputBytes = 1 << 20 // 1 MiB

// limitWriter drops writes once n bytes have been accepted.
type limitWriter struct {
	buf *bytes.Buffer
	n   int
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.n <= 0 {
		return len(p), nil
	}
	if len(p) > lw.n {
		p = p[:lw.n]
	}
	n, err := lw.buf.Write(p)
	lw.n -= n
	return len(p), err
}

// smiRunner is a function variable so tests can stub nvidia-smi.
var smiRunner = runSMI

func runSMI(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &limitWriter{buf: &outBuf, n: maxOutputBytes}
	cmd.Stderr = &limitWriter{buf: &errBuf, n: maxOutputBytes}
	runErr := cmd.Run()
	stdout := outBuf.String()
	stderr := errBuf.String()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return stdout, stderr, &SMIExitError{Stderr: stderr, Code: exitErr.ExitCode()}
		}
		return stdout, stderr, fmt.Errorf("gpu: exec %s: %w", name, runErr)
	}
	return stdout, stderr, nil
}

// SMIExitError is returned when nvidia-smi exits non-zero.
type SMIExitError struct {
	Stderr string
	Code   int
}

func (e *SMIExitError) Error() string {
	return fmt.Sprintf("gpu: nvidia-smi exited %d: %s", e.Code, e.Stderr)
}

// NvidiaSMITool implements gpu.nvidia_smi.
type NvidiaSMITool struct{}

func NewNvidiaSMITool() *NvidiaSMITool { return &NvidiaSMITool{} }

func (t *NvidiaSMITool) Name() string { return "gpu.nvidia_smi" }
func (t *NvidiaSMITool) Description() string {
	return "Query GPU status via nvidia-smi: utilization, memory, temperature, and power."
}
func (t *NvidiaSMITool) Schema() json.RawMessage {
	s := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	b, _ := json.Marshal(s)
	return b
}

func (t *NvidiaSMITool) Run(ctx context.Context, _ json.RawMessage) (tools.Observation, error) {
	out, _, err := smiRunner(ctx, "nvidia-smi",
		"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("gpu.nvidia_smi: %w", err)
	}

	tbl, text, parseErr := parseNvidiaSMIOutput(out)
	if parseErr != nil {
		return tools.Observation{}, fmt.Errorf("gpu.nvidia_smi: parse: %w", parseErr)
	}

	return tools.Observation{
		Text:  text,
		Table: tbl,
		Raw:   out,
	}, nil
}

// gpuRow holds parsed values for one GPU.
type gpuRow struct {
	index    string
	name     string
	util     float64
	memUsed  float64
	memTotal float64
	temp     float64
	power    string
}

func parseNvidiaSMIOutput(output string) (*render.Table, string, error) {
	tbl := &render.Table{
		Headers: []string{"IDX", "NAME", "UTIL%", "MEM USED", "MEM TOTAL", "TEMP°C", "POWER W"},
		Aligns: []render.Align{
			render.AlignRight,
			render.AlignLeft,
			render.AlignRight,
			render.AlignRight,
			render.AlignRight,
			render.AlignRight,
			render.AlignRight,
		},
	}

	var rows []gpuRow
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 7 {
			continue
		}
		trim := func(s string) string { return strings.TrimSpace(s) }
		row := gpuRow{
			index: trim(fields[0]),
			name:  trim(fields[1]),
			power: trim(fields[6]),
		}
		row.util, _ = strconv.ParseFloat(trim(fields[2]), 64)
		row.memUsed, _ = strconv.ParseFloat(trim(fields[3]), 64)
		row.memTotal, _ = strconv.ParseFloat(trim(fields[4]), 64)
		row.temp, _ = strconv.ParseFloat(trim(fields[5]), 64)
		rows = append(rows, row)
	}

	// Build colorizer: warn/err thresholds.
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))  // red

	tbl.Colorizer = func(rowIdx, colIdx int, cell string) (lipgloss.Style, bool) {
		if rowIdx < 0 || rowIdx >= len(rows) {
			return lipgloss.Style{}, false
		}
		r := rows[rowIdx]
		switch colIdx {
		case 2: // UTIL%
			if r.util > 90 {
				return warnStyle, true
			}
		case 3, 4: // MEM USED / MEM TOTAL
			if r.memTotal > 0 && r.memUsed/r.memTotal > 0.85 {
				return warnStyle, true
			}
		case 5: // TEMP
			if r.temp > 85 {
				return errStyle, true
			}
		}
		return lipgloss.Style{}, false
	}

	var sb strings.Builder
	for _, r := range rows {
		memUsedStr := fmt.Sprintf("%.0f", r.memUsed)
		memTotalStr := fmt.Sprintf("%.0f", r.memTotal)
		utilStr := fmt.Sprintf("%.0f", r.util)
		tempStr := fmt.Sprintf("%.0f", r.temp)
		tbl.Rows = append(tbl.Rows, []string{
			r.index, r.name, utilStr, memUsedStr, memTotalStr, tempStr, r.power,
		})
		fmt.Fprintf(&sb, "GPU %s (%s): util=%.0f%% mem=%.0f/%.0fMiB temp=%.0f°C power=%sW\n",
			r.index, r.name, r.util, r.memUsed, r.memTotal, r.temp, r.power)
	}

	return tbl, sb.String(), nil
}
