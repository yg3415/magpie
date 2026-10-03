package provider

// A Codex rate-limit reset about to run out unused is spent by itself
// (#624), for the accounts the user lets spend their resets: within
// resetExpiryLead of the soonest one running out, if the account's windows
// have been used at all — a reset that would start nothing again is left
// to run out, nothing lost. It is the reset that runs out first that is
// spent (UseCodexReset), so one that lasts longer is kept. This is apart
// from the week's one when the week is used up (AutoUseCodexReset): a
// reset that would be lost anyway takes none of the week's.

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

var (
	// resetExpiryLead is how long before a reset runs out it is spent: as
	// late as can be, so the most use it starts again, with room for a
	// look or two missed
	resetExpiryLead = 3 * time.Hour
	// resetExpiryEvery is how often the accounts are gone over; each is
	// read only when its next look is due
	resetExpiryEvery = 10 * time.Minute
	// resetExpiryFar is the longest an account goes unread: a reset
	// granted meanwhile is seen by then
	resetExpiryFar = 6 * time.Hour
	// resetExpiryRetry is when an account is read again after a read or a
	// spend that failed
	resetExpiryRetry = 30 * time.Minute
)

var expiring = expiringResets{next: map[string]time.Time{}}

type expiringResets struct {
	sync.Mutex
	next map[string]time.Time // by account, when to read it again
}

// check spends one of user's resets if the soonest of them runs out within
// resetExpiryLead and its windows have been used; look reads its windows
// and resets, spend spends the one that runs out first. Code is "" when
// none was tried.
func (e *expiringResets) check(user string, now time.Time, look func() ([]QuotaWindow, *ResetCredits, error), spend func() (ResetOutcome, error)) (ResetOutcome, error) {
	key := strings.ToLower(user)
	e.Lock()
	defer e.Unlock()
	if now.Before(e.next[key]) {
		return ResetOutcome{}, nil
	}
	windows, resets, err := look()
	if err != nil {
		e.next[key] = now.Add(resetExpiryRetry)
		return ResetOutcome{}, err
	}
	if resets == nil || resets.Count <= 0 || resets.Until == nil {
		// none held, or none runs out
		e.next[key] = now.Add(resetExpiryFar)
		return ResetOutcome{}, nil
	}
	left := resets.Until.Sub(now)
	if left > resetExpiryLead {
		e.next[key] = minTime(resets.Until.Add(-resetExpiryLead), now.Add(resetExpiryFar))
		return ResetOutcome{}, nil
	}
	if left <= 0 || !windowsUsed(windows) {
		// gone already, or nothing to start again yet: look again soon,
		// the account may be used before it runs out
		e.next[key] = now.Add(resetExpiryEvery)
		return ResetOutcome{}, nil
	}
	out, err := spend()
	if err != nil || out.Code != "reset" {
		e.next[key] = now.Add(resetExpiryRetry)
		return out, err
	}
	// another may run out soon after it
	e.next[key] = now.Add(resetExpiryEvery)
	return out, nil
}

// windowsUsed says whether any of windows, the on-demand ones aside, has
// been used: a reset starts those again.
func windowsUsed(windows []QuotaWindow) bool {
	for _, w := range windows {
		if !w.Aside && w.Used > 0 {
			return true
		}
	}
	return false
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// SpendExpiringCodexResets goes over the Codex accounts the user lets
// spend their resets and spends one about to run out unused (see check).
func SpendExpiringCodexResets(ctx context.Context) {
	for _, user := range settings.Load().CodexAutoReset {
		if ctx.Err() != nil {
			return
		}
		var who string
		out, err := expiring.check(user, time.Now(), func() ([]QuotaWindow, *ResetCredits, error) {
			var tok, accountID string
			var err error
			who, tok, accountID, err = codexUserToken(ViaLogin(ctx, "codex", user), user)
			if err != nil {
				return nil, nil, err
			}
			_, windows, resets, _, err := codexWindows(ViaLogin(ctx, "codex", who), tok, accountID)
			return windows, resets, err
		}, func() (ResetOutcome, error) {
			// one spend at a time, with the week's used-up one too
			autoReset.Lock()
			defer autoReset.Unlock()
			return UseCodexReset(ctx, who)
		})
		switch {
		case err != nil:
			log.Printf("codex reset about to run out: %s: %v", user, err)
		case out.Code != "":
			log.Printf("codex reset about to run out: %s used it: %s", user, out.Text())
		}
	}
}

// KeepResetsFromRunningOut spends the Codex resets about to run out unused
// (SpendExpiringCodexResets), three minutes after it starts and every
// resetExpiryEvery after that, until ctx ends.
func KeepResetsFromRunningOut(ctx context.Context) {
	t := time.NewTimer(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		SpendExpiringCodexResets(c)
		cancel()
		t.Reset(resetExpiryEvery)
	}
}
