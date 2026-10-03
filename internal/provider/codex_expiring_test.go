package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A reset is spent by itself only within resetExpiryLead of running out,
// and only when the account's windows have something to start again; an
// account is read again when there may be something to do, not before.
func TestResetSpentBeforeItRunsOut(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	week := func(used float64) []QuotaWindow {
		return []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: used, ResetsAt: at(72 * time.Hour)}}
	}
	for name, c := range map[string]struct {
		windows []QuotaWindow
		resets  *ResetCredits
		spend   bool
		next    time.Duration // when it is read again
	}{
		"runs out soon, used":         {week(40), &ResetCredits{Count: 1, Until: at(time.Hour)}, true, resetExpiryEvery},
		"runs out soon, nothing used": {week(0), &ResetCredits{Count: 1, Until: at(time.Hour)}, false, resetExpiryEvery},
		"on-demand only used":         {[]QuotaWindow{{Span: 30 * 24 * time.Hour, Used: 50, Aside: true}}, &ResetCredits{Count: 1, Until: at(time.Hour)}, false, resetExpiryEvery},
		"runs out tomorrow":           {week(40), &ResetCredits{Count: 1, Until: at(24 * time.Hour)}, false, resetExpiryFar},
		"runs out in five hours":      {week(40), &ResetCredits{Count: 1, Until: at(5 * time.Hour)}, false, 5*time.Hour - resetExpiryLead},
		"never runs out":              {week(40), &ResetCredits{Count: 1}, false, resetExpiryFar},
		"none held":                   {week(40), nil, false, resetExpiryFar},
		"ran out already":             {week(40), &ResetCredits{Count: 1, Until: at(-time.Minute)}, false, resetExpiryEvery},
	} {
		t.Run(name, func(t *testing.T) {
			e := expiringResets{next: map[string]time.Time{}}
			spent := 0
			out, err := e.check("Me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return c.windows, c.resets, nil },
				func() (ResetOutcome, error) { spent++; return ResetOutcome{Code: "reset", Windows: 2}, nil })
			if err != nil || (spent == 1) != c.spend || (out.Code == "reset") != c.spend {
				t.Fatalf("spent %d, %+v %v", spent, out, err)
			}
			if got := e.next["me@example.com"].Sub(now); got != c.next {
				t.Fatalf("read again in %v, want %v", got, c.next)
			}
			// not read again before then
			looked := false
			e.check("me@example.com", now.Add(c.next-time.Second), func() ([]QuotaWindow, *ResetCredits, error) { looked = true; return nil, nil, nil },
				func() (ResetOutcome, error) { t.Fatal("spent"); return ResetOutcome{}, nil })
			if looked {
				t.Fatal("read again too soon")
			}
		})
	}
}

// A read or a spend that fails is tried again later, not on every pass.
func TestResetExpiryRetriesLater(t *testing.T) {
	now := time.Now()
	soon := now.Add(time.Hour)
	e := expiringResets{next: map[string]time.Time{}}
	_, err := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return nil, nil, http.ErrHandlerTimeout },
		func() (ResetOutcome, error) { t.Fatal("spent"); return ResetOutcome{}, nil })
	if err == nil || e.next["me@example.com"].Sub(now) != resetExpiryRetry {
		t.Fatalf("%v, next %v", err, e.next["me@example.com"].Sub(now))
	}
	e.next = map[string]time.Time{}
	out, _ := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) {
		return []QuotaWindow{{Span: 5 * time.Hour, Used: 10}}, &ResetCredits{Count: 1, Until: &soon}, nil
	}, func() (ResetOutcome, error) { return ResetOutcome{Code: "nothing_to_reset"}, nil })
	if out.Code != "nothing_to_reset" || e.next["me@example.com"].Sub(now) != resetExpiryRetry {
		t.Fatalf("%+v, next %v", out, e.next["me@example.com"].Sub(now))
	}
}

// End to end against a fake ChatGPT: an account that lets its resets be
// spent, one of them running out within the hour and its week used, spends
// that one; an account that doesn't let it is never read.
func TestSpendExpiringCodexResets(t *testing.T) {
	signIn(t)
	old := expiring.next
	expiring.next = map[string]time.Time{}
	t.Cleanup(func() { expiring.next = old })
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	later := time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var read, consumed atomic.Int32
	var spentID string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			read.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 35, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": 2}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 2, "credits": []any{
				map[string]any{"id": "later", "status": "available", "expires_at": later},
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			var b struct {
				Credit string `json:"credit_id"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			spentID = b.Credit
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	SpendExpiringCodexResets(t.Context())
	if read.Load() != 0 || consumed.Load() != 0 {
		t.Fatalf("not turned on, yet read %d, spent %d", read.Load(), consumed.Load())
	}
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 1 || spentID != "soon" {
		t.Fatalf("spent %d (%q), want the one running out", consumed.Load(), spentID)
	}
	// the next pass, straight after, doesn't read it again
	n := read.Load()
	SpendExpiringCodexResets(t.Context())
	if read.Load() != n || consumed.Load() != 1 {
		t.Fatalf("read %d more, spent %d", read.Load()-n, consumed.Load())
	}
}
