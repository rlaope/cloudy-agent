package render

import (
	"github.com/charmbracelet/glamour"
)

// RenderMarkdown renders markdown-formatted text to an ANSI-escaped string
// suitable for terminal display.
//
// When theme.NoColor() is true the "notty" glamour style is used, producing
// plain text without ANSI sequences.  Otherwise the "dark" style is used.
//
// The width parameter constrains the rendered line length; pass 0 for
// glamour's default (80 columns).
func RenderMarkdown(md string, theme Theme, width int) (string, error) {
	style := glamour.WithStylePath("dark")
	if theme.NoColor() {
		style = glamour.WithStylePath("notty")
	}

	opts := []glamour.TermRendererOption{style}
	if width > 0 {
		opts = append(opts, glamour.WithWordWrap(width))
	}

	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return "", err
	}
	return r.Render(md)
}
