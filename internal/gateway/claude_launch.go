package gateway

// Claude Code started through magpie's launcher asks here which of the
// subscription's accounts to run as: routing picks it as it would pick one
// for a request — the subscription's own Routing, the accounts ticked and
// not resting, those that serve the model — and the launcher starts Claude
// Code signed in to it. Its requests then come through as its own
// (claude_passthrough.go), and that account answers them for as long as
// it runs: Claude Code names its account in every request, so another
// can't take one over mid-run.

import (
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// ClaudeLaunchPath is where the launcher asks for an account.
const ClaudeLaunchPath = "/v1/magpie/claude-launch"

// claudeLaunch answers GET /v1/magpie/claude-launch?model=<m>&effort=<e>
// with the account Claude Code should run as and where its sign-in is:
// {"account": user, "configDir": dir}, dir "" when it is the account
// Claude Code itself is signed in to, else the config directory magpie
// keeps that account's sign-in in. Only this machine is answered.
func (s *Server) claudeLaunch(w http.ResponseWriter, r *http.Request) {
	if !local(r) {
		writeError(w, provider.Anthropic, http.StatusForbidden, "magpie picks Claude Code's account only for this machine")
		return
	}
	start := time.Now()
	p, ok := claudeProvider()
	if !ok {
		writeError(w, provider.Anthropic, http.StatusServiceUnavailable, "magpie has no Claude account: add one on the Providers page")
		return
	}
	q := r.URL.Query()
	model, effort := strings.TrimSpace(q.Get("model")), strings.TrimSpace(q.Get("effort"))
	cs, pl := claudeLaunchPlan(p, model, effort)
	tr := s.trace.begin(Route{Kind: "claude-launch", Time: start, Agent: "claude", Model: model, Effort: effort, Provider: p.ID,
		Order: pl.order, Left: pl.left})
	if len(cs) == 0 {
		msg := "none of magpie's Claude accounts can take " + cmpOr(model, "Claude Code") + ": tick one on the Providers page"
		s.trace.update(tr, func(t *Route) { t.Done, t.Status, t.Error = true, http.StatusServiceUnavailable, msg })
		writeError(w, provider.Anthropic, http.StatusServiceUnavailable, msg)
		return
	}
	c := cs[0]
	dir, _, err := c.p.Account.Token(r.Context())
	if err != nil {
		msg := "magpie couldn't ready " + c.p.Account.User + "'s sign-in: " + err.Error()
		s.trace.update(tr, func(t *Route) { t.Done, t.Status, t.Error = true, http.StatusBadGateway, msg })
		writeError(w, provider.Anthropic, http.StatusBadGateway, msg)
		return
	}
	if dir != "" {
		// Claude Code there is the user's but for the account: a part
		// that couldn't be shared is said, and the launch goes on
		if err := shareClaudeConfig(dir); err != nil {
			log.Printf("claude launch: %s's config directory shares only part of the user's: %v", c.p.Account.User, err)
		}
	}
	s.trace.update(tr, func(t *Route) {
		t.Pinned = c.p.Account.User
		t.Tries = []Try{{ID: c.rest, Model: model, Effort: effort, Start: start, Done: true, Status: http.StatusOK}}
		t.Done, t.Status, t.Millis = true, http.StatusOK, time.Since(start).Milliseconds()
	})
	writeJSON(w, http.StatusOK, map[string]string{"account": c.p.Account.User, "configDir": dir})
}

// claudeLaunchPlan is the subscription's accounts in the order routing
// would try them for model at effort, the first the one to launch as:
// the subscription's own Routing over the accounts ticked, an account
// resting after a failure, or one whose plan turned the effort away, put
// after those that can take it. No fallback, group or affinity: Claude
// Code runs as one Claude account, from start to end.
func claudeLaunchPlan(p provider.Provider, model, effort string) ([]candidate, planned) {
	var pl planned
	cs, aside, left, barred := perKeyBarred(p, model, provider.Anthropic)
	pl.left = append(pl.left, barredOf(barred, p, false, provider.Anthropic, nil)...)
	cs, wg := weigh(p, cs, model, provider.Anthropic)
	for _, c := range cs {
		pl.order = append(pl.order, weighed(c, p, wg, false, provider.Anthropic))
	}
	pl.order = append(pl.order, asideOf(aside, p, false, provider.Anthropic)...)
	cs = append(cs, aside...)
	for _, c := range left {
		w := weighed(c, p, weighing{}, false, provider.Anthropic)
		w.Unlisted = true
		pl.left = append(pl.left, w)
	}
	if effort != "" && len(pl.order) == len(cs) {
		// stable: those that take the effort keep routing's order, and the
		// trace's order follows
		type pair struct {
			c candidate
			w Weighed
		}
		ps := make([]pair, len(cs))
		for i := range cs {
			ps[i] = pair{cs[i], pl.order[i]}
		}
		slices.SortStableFunc(ps, func(a, b pair) int {
			ta, tb := takesEffort(a.c, effort), takesEffort(b.c, effort)
			switch {
			case ta && !tb:
				return -1
			case tb && !ta:
				return 1
			}
			return 0
		})
		for i := range ps {
			cs[i], pl.order[i] = ps[i].c, ps[i].w
		}
	}
	return restLast(cs, pl)
}

// cmpOr is a, or b when a is empty.
func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
