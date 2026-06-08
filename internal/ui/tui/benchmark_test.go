package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func BenchmarkStreamModelDrainNoColor(b *testing.B) {
	chunks := benchmarkStreamChunks(200)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := newStreamModel(true)
		s, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
		for _, chunk := range chunks {
			s, _ = s.Update(streamTokenMsg(chunk))
			s, _ = s.Update(streamFlushTickMsg{})
		}
		if s.content.Len() == 0 {
			b.Fatal("empty stream content")
		}
	}
}

func BenchmarkStreamModelDrainMarkdownColor(b *testing.B) {
	chunks := benchmarkMarkdownChunks(80)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := newStreamModel(false)
		s, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
		for _, chunk := range chunks {
			s, _ = s.Update(streamTokenMsg(chunk))
			s, _ = s.Update(streamFlushTickMsg{})
		}
		s.finalizeAssistantBlock()
		if s.content.Len() == 0 {
			b.Fatal("empty stream content")
		}
	}
}

func BenchmarkStreamModelMarkdownResizeFinalize(b *testing.B) {
	chunks := benchmarkMarkdownChunks(60)
	widths := []int{120, 84, 132, 96}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := newStreamModel(false)
		s, _ = s.Update(tea.WindowSizeMsg{Width: widths[0], Height: 32})
		for j, chunk := range chunks {
			s, _ = s.Update(streamTokenMsg(chunk))
			s, _ = s.Update(streamFlushTickMsg{})
			if j%12 == 11 {
				s, _ = s.Update(tea.WindowSizeMsg{Width: widths[(j/12+1)%len(widths)], Height: 32})
			}
		}
		s.finalizeAssistantBlock()
		if s.content.Len() == 0 {
			b.Fatal("empty stream content")
		}
	}
}

func benchmarkStreamChunks(n int) []string {
	chunks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		chunks = append(chunks, fmt.Sprintf("clause %03d completed, ", i))
	}
	return chunks
}

func benchmarkMarkdownChunks(n int) []string {
	chunks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		chunks = append(chunks, fmt.Sprintf("- **check %03d**: %s.\n", i, strings.Repeat("signal ", 8)))
	}
	return chunks
}
