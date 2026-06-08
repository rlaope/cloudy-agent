package render

import (
	"sync"

	"github.com/charmbracelet/glamour"
)

type markdownRendererKey struct {
	noColor bool
	width   int
}

type cachedMarkdownRenderer struct {
	mu       sync.Mutex
	renderer *glamour.TermRenderer
}

var markdownRendererCache sync.Map

// RenderMarkdown renders markdown-formatted text to an ANSI-escaped string
// suitable for terminal display.
//
// When theme.NoColor() is true the "notty" glamour style is used, producing
// plain text without ANSI sequences.  Otherwise the "dark" style is used.
//
// The width parameter constrains the rendered line length; pass 0 for
// glamour's default (80 columns).
func RenderMarkdown(md string, theme Theme, width int) (string, error) {
	renderer, err := cachedTermRenderer(theme.NoColor(), width)
	if err != nil {
		return "", err
	}
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	return renderer.renderer.Render(md)
}

func cachedTermRenderer(noColor bool, width int) (*cachedMarkdownRenderer, error) {
	if width < 0 {
		width = 0
	}
	key := markdownRendererKey{noColor: noColor, width: width}
	if cached, ok := markdownRendererCache.Load(key); ok {
		return cached.(*cachedMarkdownRenderer), nil
	}

	style := glamour.WithStylePath("dark")
	if noColor {
		style = glamour.WithStylePath("notty")
	}
	opts := []glamour.TermRendererOption{style}
	if width > 0 {
		opts = append(opts, glamour.WithWordWrap(width))
	}

	termRenderer, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return nil, err
	}
	renderer := &cachedMarkdownRenderer{renderer: termRenderer}
	cached, loaded := markdownRendererCache.LoadOrStore(key, renderer)
	if loaded {
		return cached.(*cachedMarkdownRenderer), nil
	}
	return renderer, nil
}
