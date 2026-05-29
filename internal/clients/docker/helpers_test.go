package dockerclient

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestMatchesWorkload(t *testing.T) {
	tests := []struct {
		name     string
		summary  container.Summary
		workload string
		want     bool
	}{
		{"empty workload never matches", container.Summary{Names: []string{"/api"}}, "", false},
		{"exact name match (slash-trimmed)", container.Summary{Names: []string{"/api"}}, "api", true},
		{"case-insensitive name match", container.Summary{Names: []string{"/API"}}, "api", true},
		{"substring is NOT a match", container.Summary{Names: []string{"/api-gateway"}}, "api", false},
		{"compose service label match", container.Summary{Labels: map[string]string{"com.docker.compose.service": "web"}}, "web", true},
		{"compose project label match", container.Summary{Labels: map[string]string{"com.docker.compose.project": "shop"}}, "shop", true},
		{"no match", container.Summary{Names: []string{"/db"}}, "cache", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesWorkload(tt.summary, tt.workload); got != tt.want {
				t.Errorf("MatchesWorkload(%+v, %q) = %v, want %v", tt.summary, tt.workload, got, tt.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		summary container.Summary
		want    string
	}{
		{"first name slash-trimmed", container.Summary{Names: []string{"/web", "/web2"}}, "web"},
		{"short id when no names", container.Summary{ID: "abcdef0123456789"}, "abcdef012345"},
		{"full id when shorter than 12", container.Summary{ID: "abc"}, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayName(tt.summary); got != tt.want {
				t.Errorf("DisplayName(%+v) = %q, want %q", tt.summary, got, tt.want)
			}
		})
	}
}
