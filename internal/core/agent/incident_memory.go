package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rlaope/cloudy/internal/incidentmemory"
	"github.com/rlaope/cloudy/internal/permission"
)

const (
	incidentMemoryMaxCases       = 3
	incidentMemoryMaxRenderBytes = 4096
)

// BuildIncidentMemoryPrompt retrieves approved similar cases for text and
// renders the agent-facing prompt adapter. Storage, scoring, and rendering stay
// separated so incidentmemory can later feed RFC-RAG without carrying prompt
// wording as part of its storage contract.
func BuildIncidentMemoryPrompt(text string, profile *permission.Profile) (string, error) {
	store := incidentmemory.NewDefaultStore()
	cases, err := store.Retrieve(incidentmemory.QueryFromText(text), incidentmemory.RetrieveOptions{Limit: incidentMemoryMaxCases})
	if err != nil {
		return "", err
	}
	return RenderIncidentMemoryPrompt(cases, profile, incidentMemoryMaxCases, incidentMemoryMaxRenderBytes), nil
}

func RenderIncidentMemoryPrompt(cases []incidentmemory.SimilarCase, profile *permission.Profile, maxCases, maxBytes int) string {
	if len(cases) == 0 {
		return ""
	}
	if maxCases <= 0 {
		maxCases = incidentMemoryMaxCases
	}
	if maxBytes <= 0 {
		maxBytes = incidentMemoryMaxRenderBytes
	}
	if len(cases) > maxCases {
		cases = cases[:maxCases]
	}

	var sb strings.Builder
	sb.WriteString("## Prior similar incident cases\n")
	sb.WriteString("These approved local case cards are references only. They are not proof of the current root cause; re-check live signals before diagnosing.\n")
	for _, sc := range cases {
		c := sc.Card
		fmt.Fprintf(&sb, "\n- %s (%s, confidence %.2f)\n", c.ID, c.CauseStatus, c.Confidence)
		fmt.Fprintf(&sb, "  service: %s\n", c.AffectedService)
		if len(c.Symptoms) > 0 {
			fmt.Fprintf(&sb, "  symptoms: %s\n", strings.Join(c.Symptoms, "; "))
		}
		if len(c.Signals) > 0 {
			fmt.Fprintf(&sb, "  signals: %s\n", strings.Join(c.Signals, "; "))
		}
		if c.Cause != "" {
			fmt.Fprintf(&sb, "  prior cause: %s\n", c.Cause)
		}
		if c.FixOrMitigation != "" {
			fmt.Fprintf(&sb, "  prior fix or mitigation: %s\n", c.FixOrMitigation)
		}
		if len(sc.Reasons) > 0 {
			fmt.Fprintf(&sb, "  similarity reasons: %s\n", strings.Join(sc.Reasons, "; "))
		}
		if sc.DifferenceNotes != "" {
			fmt.Fprintf(&sb, "  what was different: %s\n", sc.DifferenceNotes)
		}
		if c.Source.Type != "" {
			fmt.Fprintf(&sb, "  source: %s %s %s\n", c.Source.Type, c.Source.ID, c.Source.Ref)
		}
		if sb.Len() >= maxBytes {
			break
		}
	}
	out := sb.String()
	if len(out) > maxBytes {
		out = truncateUTF8(out, maxBytes)
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
		out += "\n...[incident memory truncated]..."
	}
	out = strings.TrimSpace(out)
	masker := permission.MaskerOrDefault(profile)
	if masked, err := masker.MaskJSON([]byte(out)); err == nil {
		out = string(masked)
	}
	return masker.MaskString(out)
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.ValidString(s[:maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
