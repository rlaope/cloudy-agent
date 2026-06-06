package chatops

import (
	"strings"
	"sync"

	"github.com/rlaope/cloudy/internal/core/llm"
)

// Sink collects one Cloudy response for asynchronous chat delivery.
type Sink struct {
	mu      sync.Mutex
	text    strings.Builder
	tools   []string
	usages  []llm.Usage
	maxTool int
}

// NewSink returns an empty ChatOps sink.
func NewSink() *Sink { return &Sink{maxTool: 20} }

func (s *Sink) WriteToken(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text.WriteString(text)
}

func (s *Sink) BeginToolCall(name, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tools) < s.maxTool {
		s.tools = append(s.tools, name)
	}
}

func (s *Sink) EndToolCall(_ string, _ error) {}

func (s *Sink) RecordUsage(u llm.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usages = append(s.usages, u)
}

// Text returns the assistant prose collected so far.
func (s *Sink) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
}

// ToolNames returns the first tool names observed in this run.
func (s *Sink) ToolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.tools))
	copy(out, s.tools)
	return out
}

// Usage returns recorded usage events.
func (s *Sink) Usage() []llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Usage, len(s.usages))
	copy(out, s.usages)
	return out
}
