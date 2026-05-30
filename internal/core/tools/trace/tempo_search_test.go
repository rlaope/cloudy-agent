package trace_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

// TestTempoSearchTraces verifies SearchTraces decodes Tempo's /api/search
// summary (startTimeUnixNano + durationMs + root names) into TempoTraceSummary
// values, exercising parseTempoSearch with a canned response.
func TestTempoSearchTraces(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	body := `{"traces":[
		{"rootServiceName":"checkout","rootTraceName":"POST /pay","startTimeUnixNano":"` +
		strconv.FormatInt(start.UnixNano(), 10) + `","durationMs":1234},
		{"rootServiceName":"checkout","rootTraceName":"GET /cart","startTimeUnixNano":"not-a-number","durationMs":7}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got == "" {
			t.Errorf("missing q param")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := httpapi.NewClient("tempo-1", srv.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tc := &trace.TempoClient{Client: c}

	got, err := tc.SearchTraces(context.Background(), `{ status = error }`, start.Add(-time.Hour), start, 50)
	if err != nil {
		t.Fatalf("SearchTraces: %v", err)
	}
	// The unparseable startTimeUnixNano row is skipped.
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (bad-timestamp row skipped)", len(got))
	}
	s := got[0]
	if !s.StartTime.Equal(start) {
		t.Errorf("StartTime = %v, want %v", s.StartTime, start)
	}
	if s.Duration != 1234*time.Millisecond {
		t.Errorf("Duration = %v, want 1234ms", s.Duration)
	}
	if s.RootService != "checkout" || s.RootName != "POST /pay" {
		t.Errorf("root = %q/%q, want checkout/POST /pay", s.RootService, s.RootName)
	}
}
