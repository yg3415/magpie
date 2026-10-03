package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// launch asks the gateway, as the launcher does, which account Claude Code
// should run as.
func launch(t *testing.T, s *Server, query string) (int, map[string]string) {
	t.Helper()
	r := httptest.NewRequest("GET", ClaudeLaunchPath+query, nil)
	r.RemoteAddr = "127.0.0.1:50001"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The launcher gets the account routing puts first for the model, and
// where its sign-in is: "" for the one Claude Code is signed in to, the
// config directory magpie keeps for a saved one, ready for Claude Code to
// run in. An account resting after a refusal waits behind one that can
// answer. Each pick is in the trace, with routing's order.
func TestClaudeLaunchPicksAsRoutingDoes(t *testing.T) {
	claudeAccountsAndAPI(t, answerStream)
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	s := New()

	p, _ := claudeProvider()
	first := s.candidates(p, "claude-sonnet-5-5", provider.Anthropic)[0].p.Account.User
	code, got := launch(t, s, "?model=claude-sonnet-5-5&effort=high")
	if code != 200 || got["account"] != first {
		t.Fatalf("%d %v, routing puts %s first", code, got, first)
	}

	// the first rests: the other goes
	c, _ := claudeCandidate(p, first, "claude-sonnet-5-5")
	s.restAfter(c, http.StatusTooManyRequests, http.Header{"Retry-After": {"3600"}}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
	code, got = launch(t, s, "?model=claude-sonnet-5-5")
	if code != 200 || got["account"] == first || got["account"] == "" {
		t.Fatalf("with %s resting: %d %v", first, code, got)
	}
	for _, who := range []string{"a@example.com", "b@example.com"} {
		if got["account"] != who {
			continue
		}
		if who == "a@example.com" && got["configDir"] != "" {
			t.Fatalf("Claude Code's own sign-in: configDir %q", got["configDir"])
		}
		if who == "b@example.com" {
			if got["configDir"] == "" {
				t.Fatal("a saved account: no configDir")
			}
			if _, err := os.Stat(filepath.Join(got["configDir"], ".credentials.json")); err != nil {
				t.Fatalf("b's config directory isn't ready: %v", err)
			}
		}
	}

	st := s.Trace(t.Context(), 0, 0)
	var picks int
	for _, r := range st.Routes {
		if r.Kind == "claude-launch" {
			picks++
			if !r.Done || r.Status != 200 || r.Pinned == "" || len(r.Order) == 0 {
				t.Fatalf("launch route: %+v", r)
			}
		}
	}
	if picks != 2 {
		t.Fatalf("%d launch routes in the trace", picks)
	}
}

// An account barred from the model, or not ticked, is never picked.
func TestClaudeLaunchSkipsBarred(t *testing.T) {
	claudeAccountsAndAPI(t, answerStream)
	s := New()
	if err := provider.SetAccountModels("claude", "a@example.com", []string{"claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if code, got := launch(t, s, "?model=claude-sonnet-5-5"); code != 200 || got["account"] != "b@example.com" {
			t.Fatalf("%d %v", code, got)
		}
	}
}

// Only this machine is told which account to run as.
func TestClaudeLaunchLocalOnly(t *testing.T) {
	claudeAccountsAndAPI(t, answerStream)
	s := New()
	r := httptest.NewRequest("GET", ClaudeLaunchPath, nil)
	r.RemoteAddr = "192.168.1.20:50001"
	rec := httptest.NewRecorder()
	s.claudeLaunch(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("from another machine: %d %s", rec.Code, rec.Body)
	}
}

// An account asked for by name is the one Claude Code runs as, resting or
// not; one not on for the model is refused.
func TestClaudeLaunchPinned(t *testing.T) {
	claudeAccountsAndAPI(t, answerStream)
	s := New()
	p, _ := claudeProvider()
	c, _ := claudeCandidate(p, "b@example.com", "claude-sonnet-5-5")
	s.restAfter(c, http.StatusTooManyRequests, http.Header{"Retry-After": {"3600"}}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
	if code, got := launch(t, s, "?model=claude-sonnet-5-5&account=B@example.com"); code != 200 || got["account"] != "b@example.com" || got["configDir"] == "" {
		t.Fatalf("pinned b: %d %v", code, got)
	}
	if code, _ := launch(t, s, "?model=claude-sonnet-5-5&account=nobody@example.com"); code != http.StatusConflict {
		t.Fatalf("pinned to no account: %d", code)
	}
}
