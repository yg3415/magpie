package gateway

// Subscription passthrough: Claude Code signed in with its own Claude
// subscription, its base URL set to magpie, sends magpie what it would send
// Anthropic. It goes on to Anthropic as it came — method, path, query,
// headers and body — and the reply comes back as Anthropic sent it. magpie
// only reads on the side what it cost, what the account has left (the
// anthropic-ratelimit-unified-* headers Claude Code itself reads) and a
// refusal, for the usage log, the accounts' allowances and routing's rests.
// Nothing that rewrites a request applies: not masking, stand-ins, groups,
// effort, fallback or vision. The account is the one Claude Code runs as,
// chosen when it started (claude_launch.go); the request names it in its
// metadata.
//
// Two headers are left to the connection, as they are to any proxy:
// Accept-Encoding, so the reply comes decoded and can be read on the side,
// and the hop-by-hop ones.

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// claudeAPI is where Claude Code's own requests go on to.
var claudeAPI = "https://api.anthropic.com"

// claudeOAuthBeta is the beta Anthropic takes a claude.ai sign-in with.
const claudeOAuthBeta = "oauth-2025-04-20"

// claudeOwn says r is Claude Code's own: sent from this machine by Claude
// Code, signed with its Claude subscription (a claude.ai OAuth token, which
// Anthropic takes only with its oauth beta). One carrying magpie's key, or
// from another machine, is not.
func claudeOwn(r *http.Request) bool {
	if !local(r) || !fromClaudeCode(r.Header) || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-ant-oat") {
		return false
	}
	for _, v := range r.Header.Values("anthropic-beta") {
		for _, b := range strings.Split(v, ",") {
			if strings.TrimSpace(b) == claudeOAuthBeta {
				return true
			}
		}
	}
	return false
}

// claudeDirect hands Claude Code's own requests, whatever their path, to
// passthrough, and every other one to next.
func (s *Server) claudeDirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claudeOwn(r) {
			s.passClaude(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hopHeaders are the connection's, never passed on (RFC 9110 §7.6.1), with
// the length the body carries itself and the encoding left to the client.
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length"}

// claudeProvider is Claude Code's subscription, the provider its accounts
// are; false when magpie has none.
func claudeProvider() (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.Account != nil && p.Account.Agent == "claude" {
			return p, true
		}
	}
	return provider.Provider{}, false
}

// claudeCandidate is the account of Claude Code's subscription a request
// ran as, among those routing goes over: for its proxy, its slots and its
// rests. False when it is none of them (not saved, or not ticked), and the
// request goes on all the same, by the subscription's proxy.
func claudeCandidate(p provider.Provider, user, model string) (candidate, bool) {
	if user == "" {
		return candidate{}, false
	}
	for _, c := range perKey(p, model, provider.Anthropic) {
		if c.p.Account != nil && strings.EqualFold(c.p.Account.User, user) {
			return c, true
		}
	}
	return candidate{}, false
}

// passClaude forwards one of Claude Code's own requests. A turn
// (POST /v1/messages) is recorded as any call is; anything else Claude
// Code asks its base URL for (count_tokens) only goes through.
func (s *Server) passClaude(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, ok := s.requestBody(w, r, provider.Anthropic)
	if !ok {
		return
	}
	model := gjson.GetBytes(body, "model").String()
	user := provider.ClaudeUserOf(provider.ClaudeAccountOf(gjson.GetBytes(body, "metadata.user_id").String()))
	p, hasP := claudeProvider()
	c, hasC := candidate{}, false
	if hasP {
		c, hasC = claudeCandidate(p, user, model)
	}
	via := p
	if hasC {
		via = c.p
	}
	if r.Method != http.MethodPost || strings.TrimSuffix(r.URL.Path, "/") != "/v1/messages" {
		s.relayClaude(w, r, via, body, nil)
		return
	}

	requestBody, requestTruncated := captureRequestBody(body)
	wholeBodies := usage.OTelWhole()
	var otelIn []byte
	if wholeBodies {
		otelIn = body
	}
	capture := &captureResponseWriter{ResponseWriter: w}
	if wholeBodies {
		capture.otel = &spool{limit: wholeBodyLimit, pattern: "magpie-otel-*"}
	}
	who := callerOf(r)
	metadata := requestSessionMetadata(r.Header, body)
	call := Call{Time: start, From: provider.Anthropic, Model: model, Agent: who.agent, Via: who.via, Kind: requestCallKind(r.Header, metadata),
		Passthrough: true, Provider: p.ID, RequestBody: requestBody, RequestTruncated: requestTruncated, otelIn: otelIn,
		wire: archiving(r, capture, start, body)}
	// the archive keeps the call, but its id is magpie's: the reply goes
	// back with Anthropic's headers alone
	capture.Header().Del(ArchiveHeader)
	defer discardArchive(capture)
	r, telemetry := beginOTelRequest(r, call.Kind, body)
	defer func() { telemetry.end(call, time.Since(start).Milliseconds()) }()
	usage.Saw(agentOf(r))

	id := user
	if hasC {
		id = c.rest
	}
	tr := s.trace.begin(Route{Passthrough: true, Pinned: user, Time: start, Agent: call.Agent, Session: sessionOf(r.Header),
		ParentSession: titleParentSession(r.Header, metadata, call.Kind), Kind: call.Kind, Model: model,
		Effort: requestEffort(provider.Anthropic, body), Provider: p.ID})
	if telemetry != nil {
		telemetry.routeID = tr.ID
	}
	began := time.Now()
	s.trace.update(tr, func(t *Route) { t.Tries = append(t.Tries, Try{ID: id, Model: model, Start: began}) })
	try := Try{ID: id, Model: model, Start: began}

	// an account with a MaxConcurrency is asked once one of its slots is
	// free, as any request to it is
	release := func() {}
	if hasC {
		waited := time.Now()
		rel, ok := s.lanes.acquire(r.Context(), c.who(), c.p.Concurrency())
		if !ok {
			call.Status, call.Error = 499, "the agent went away while its request waited for a slot"
			s.finishClaude(r, &call, capture, tr, try, start)
			return
		}
		release = rel
		if q := time.Since(waited).Milliseconds(); q > 0 {
			try.Queued = q
		}
	}
	res := &passResult{}
	func() {
		defer release()
		s.relayClaude(capture, r, via, body, res)
	}()

	call.Status, call.To, call.Usage = res.status, provider.Anthropic, res.usage
	call.TTFT = res.ttft
	if res.status >= 400 || res.err != "" {
		call.Error = cmp.Or(res.err, provider.APIError(res.errBody, http.StatusText(res.status)))
	}
	if user != "" {
		if ls := claudeHeaderLimits(res.header); len(ls) > 0 {
			provider.NoteClaudeLimits(user, ls)
		}
	}
	try.Done, try.Status, try.Millis = true, call.Status, time.Since(began).Milliseconds()
	try.TTFT, try.Served = sinceStart(0, time.Duration(res.ttft)*time.Millisecond), res.usage.Served
	if call.Status >= 400 {
		try.Fail, try.Error = failure(call.Status, res.errBody), call.Error
		if hasC {
			rest := s.restAfter(c, call.Status, res.header, res.errBody)
			try.Rest = &rest
		}
	} else if hasC {
		servedCandidate(c, call.Usage.Input+call.Usage.Output+call.Usage.CacheRead+call.Usage.CacheWrite)
	}
	s.finishClaude(r, &call, capture, tr, try, start)
	where := p.Where()
	rec := usage.Record{RouteID: tr.ID, Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: where, Model: model,
		ProviderAccount: user, Requested: model, Served: call.Usage.Served, Passthrough: true,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead, CacheWrite: call.Usage.CacheWrite,
		Reasoning: call.Usage.Reasoning, Effort: requestEffort(provider.Anthropic, body), Millis: call.Millis, Status: call.Status,
		TTFT: call.TTFT, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Kind: call.Kind,
		RequestID: call.Usage.RequestID, Endpoint: endpointOf(r, provider.Anthropic, provider.Anthropic), Archive: call.archiveName()}
	failedWith(&rec, call.Status, call.Error, call.Usage.ErrType)
	withBodies(&rec, &call)
	appendUsage(r, rec)
}

// finishClaude records a passthrough call in Recent calls and closes its
// route.
func (s *Server) finishClaude(r *http.Request, call *Call, capture *captureResponseWriter, tr *Route, try Try, start time.Time) {
	call.Millis = time.Since(start).Milliseconds()
	call.ResponseBody = capture.body.text()
	call.ResponseTruncated = capture.body.truncated
	if capture.otel != nil {
		call.otelOutCut = capture.otel.cut()
		if b, err := capture.otel.read(); err == nil {
			call.otelOut = b
		}
	}
	s.trace.update(tr, func(t *Route) {
		if n := len(t.Tries); n > 0 {
			t.Tries[n-1] = try
		}
		t.Done, t.Status, t.Error, t.Millis = true, call.Status, call.Error, call.Millis
		if call.Status < 400 && call.To != "" {
			t.Usage = append(t.Usage, routeUsage(call.Provider, call.Model, call.Usage)...)
			t.Served = try.Served
		}
		t.Tokens = call.Usage.Input + call.Usage.Output + call.Usage.CacheRead + call.Usage.CacheWrite
		t.Output, t.TTFT = call.Usage.Output, call.TTFT
	})
	s.record(*call)
}

// passResult is what relayClaude read on the side of a reply.
type passResult struct {
	status  int
	header  http.Header
	usage   Usage
	errBody []byte // the start of a refusal's body
	err     string // the request never reached Anthropic, or its reply broke off
	ttft    int64  // ms to the reply's first byte
}

// relayClaude sends r's request on to Anthropic by p's proxy, as it came,
// and writes the reply back as it came; res, when set, is told what it
// cost and how it went.
func (s *Server) relayClaude(w http.ResponseWriter, r *http.Request, p provider.Provider, body []byte, res *passResult) {
	if res == nil {
		res = &passResult{}
	}
	start := time.Now()
	ctx := p.Via(r.Context())
	req, err := http.NewRequestWithContext(ctx, r.Method, claudeAPI+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		res.status, res.err = http.StatusBadGateway, err.Error()
		writeError(w, provider.Anthropic, res.status, "magpie couldn't forward Claude Code's request: "+err.Error())
		return
	}
	req.Header = r.Header.Clone()
	for _, h := range append(hopHeaders, "Accept-Encoding") {
		req.Header.Del(h)
	}
	req.ContentLength = int64(len(body))
	up, err := s.client.Do(req)
	if err != nil {
		res.status, res.err = http.StatusBadGateway, err.Error()
		if ctx.Err() == context.Canceled {
			res.status = 499
			return
		}
		writeError(w, provider.Anthropic, res.status, "magpie couldn't reach Anthropic for Claude Code: "+err.Error())
		return
	}
	defer up.Body.Close()
	res.status, res.header = up.StatusCode, up.Header
	h := w.Header()
	for k, vs := range up.Header {
		h[k] = vs
	}
	for _, k := range append(hopHeaders, "Content-Encoding") {
		h.Del(k)
	}
	w.WriteHeader(up.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(provider.Anthropic, up.Header.Get("Content-Type"))
	buf := make([]byte, 32<<10)
	for {
		n, rerr := up.Body.Read(buf)
		if n > 0 {
			if res.ttft == 0 {
				res.ttft = max(1, time.Since(start).Milliseconds())
			}
			if up.StatusCode >= 400 && len(res.errBody) < 1<<20 {
				res.errBody = append(res.errBody, buf[:n]...)
			} else {
				sniff.write(buf[:n])
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				res.err = "Claude Code went away mid-reply"
				break
			}
			if f != nil {
				f.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF && res.err == "" {
				res.err = cutMidReply("Anthropic", rerr)
			}
			break
		}
	}
	if up.StatusCode < 400 {
		sniff.drain()
		res.usage = sniff.usage()
		if res.err == "" && sniff.failed != "" {
			res.err = sniff.failed
		}
	}
	res.usage.RequestID = requestID(up.Header)
}

// claudeWindows are Anthropic's names for an account's windows in its
// anthropic-ratelimit-unified-<name>-* headers, as Claude Code reads them,
// with the kind each is.
var claudeWindows = [][2]string{{"5h", "five_hour"}, {"7d", "seven_day"}, {"7d_oi", "seven_day_overage_included"}, {"overage", "overage"}}

// claudeHeaderLimits is what a reply's headers say the account has left:
// each window's share used and when it renews, and the window a refusal
// was for, used up.
func claudeHeaderLimits(h http.Header) []provider.ClaudeLimit {
	if h == nil {
		return nil
	}
	num := func(k string) (float64, bool) {
		v, err := strconv.ParseFloat(strings.TrimSpace(h.Get("anthropic-ratelimit-unified-"+k)), 64)
		return v, err == nil
	}
	var out []provider.ClaudeLimit
	told := map[string]bool{}
	for _, w := range claudeWindows {
		used, ok := num(w[0] + "-utilization")
		if !ok {
			continue
		}
		reset, _ := num(w[0] + "-reset")
		out = append(out, provider.ClaudeLimit{Kind: w[1], Used: used, ResetsAt: int64(reset)})
		told[w[1]] = true
	}
	if claim := h.Get("anthropic-ratelimit-unified-representative-claim"); claim != "" && !told[claim] &&
		h.Get("anthropic-ratelimit-unified-status") == "rejected" {
		reset, _ := num("reset")
		out = append(out, provider.ClaudeLimit{Kind: claim, Used: 1, ResetsAt: int64(reset)})
	}
	return out
}
