package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rlaope/cloudy/internal/llm"
)

// ErrTokenBudgetExceeded is returned by CostGuardHook.OnUsage once the
// cumulative input+output token count for the session exceeds the configured
// MaxTokensPerSession.
var ErrTokenBudgetExceeded = errors.New("agent: token budget exceeded for session")

// ErrUSDBudgetExceeded is returned by CostGuardHook.OnUsage once the
// rolling-day USD spend exceeds the configured MaxUSDPerDay.
var ErrUSDBudgetExceeded = errors.New("agent: daily USD budget exceeded")

// CostGuardHook enforces session-level token and daily USD caps. A zero limit
// means "no limit" so a partial Safety configuration (e.g. tokens only) still
// works. The hook is safe for concurrent use, though the agent loop is
// single-threaded in practice.
type CostGuardHook struct {
	NoopHook

	maxTokens int     // 0 = unlimited
	maxUSD    float64 // 0 = unlimited

	mu        sync.Mutex
	inTokens  int
	outTokens int
	usdToday  float64
	dayStart  time.Time
}

// NewCostGuardHook constructs a CostGuardHook with the given caps. Zero values
// disable the corresponding check.
func NewCostGuardHook(maxTokensPerSession int, maxUSDPerDay float64) *CostGuardHook {
	return &CostGuardHook{
		maxTokens: maxTokensPerSession,
		maxUSD:    maxUSDPerDay,
		dayStart:  utcDayStart(time.Now()),
	}
}

// OnUsage accumulates token and USD counters and returns a sentinel error once
// either configured cap is exceeded. The day counter rolls at UTC midnight.
func (h *CostGuardHook) OnUsage(_ context.Context, u llm.Usage) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	today := utcDayStart(time.Now())
	if today.After(h.dayStart) {
		h.usdToday = 0
		h.dayStart = today
	}

	h.inTokens += u.InputTokens
	h.outTokens += u.OutputTokens
	h.usdToday += u.CostUSD

	total := h.inTokens + h.outTokens
	if h.maxTokens > 0 && total > h.maxTokens {
		return fmt.Errorf("%w: %d > %d", ErrTokenBudgetExceeded, total, h.maxTokens)
	}
	if h.maxUSD > 0 && h.usdToday > h.maxUSD {
		return fmt.Errorf("%w: $%.4f > $%.4f", ErrUSDBudgetExceeded, h.usdToday, h.maxUSD)
	}
	return nil
}

// Totals returns the current cumulative counters. Useful for UI display and
// for tests that need to assert pre-overrun state.
func (h *CostGuardHook) Totals() (inTokens, outTokens int, usdToday float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inTokens, h.outTokens, h.usdToday
}

func utcDayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
