package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// upstreamSaw is what the stand-in for Anthropic was sent.
type upstreamSaw struct {
	mu     sync.Mutex
	method string
	uri    string
	header http.Header
	body   []byte
	n      int
}

// claudeAccountsAndAPI signs Claude Code in to a@example.com (account id
// u-a), with b@example.com (u-b) saved beside it and on, and stands an
// upstream in for Anthropic that records what it is sent and answers with
// answer.
func claudeAccountsAndAPI(t *testing.T, answer http.HandlerFunc) *upstreamSaw {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": tok + "-refresh",
			"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth("tok-a")), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "b@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-b"),
			"profile": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"}},
	}), 0o600)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)

	saw := &upstreamSaw{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		saw.mu.Lock()
		saw.method, saw.uri, saw.header, saw.body = r.Method, r.URL.RequestURI(), r.Header.Clone(), b
		saw.n++
		saw.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(up.Close)
	was := claudeAPI
	claudeAPI = up.URL
	t.Cleanup(func() { claudeAPI = was })
	return saw
}

// claudeCodeBody is a turn as Claude Code sends it, as the account with
// id account.
func claudeCodeBody(account, text string) []byte {
	uid, _ := json.Marshal(map[string]string{"device_id": "dev", "account_uuid": account, "session_id": "sess-1"})
	return mustJSON(map[string]any{
		"model": "claude-sonnet-5-5", "max_tokens": 32000, "stream": true,
		"system":   []map[string]any{{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.288.5ea; cc_entrypoint=cli;"}, {"type": "text", "text": "You are Claude Code.", "cache_control": map[string]string{"type": "ephemeral"}}},
		"messages": []map[string]any{{"role": "user", "content": text}},
		"metadata": map[string]string{"user_id": string(uid)},
	})
}

// claudeCodeRequest is Claude Code's request to its base URL, magpie, as
// it signs it with its subscription, from this machine.
func claudeCodeRequest(path string, body []byte) *http.Request {
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Authorization", "Bearer sk-ant-oat01-secret")
	r.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14")
	r.Header.Set("anthropic-version", "2023-06-01")
	r.Header.Set("User-Agent", "claude-cli/2.1.288 (external, cli)")
	r.Header.Set("X-Claude-Code-Session-Id", "sess-1")
	r.Header.Set("X-Stainless-Lang", "js")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	return r
}

const claudeStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-5-5\",\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":1000,\"cache_creation_input_tokens\":20,\"output_tokens\":1}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func answerStream(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Request-Id", "req_1")
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1791038400")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.1")
	h.Set("anthropic-ratelimit-unified-7d-reset", "1791396000")
	h.Set("anthropic-ratelimit-unified-status", "allowed")
	h.Set("X-Vendor-Thing", "kept")
	io.WriteString(w, claudeStream)
}

// Claude Code's own turn goes to Anthropic as it came — method, path with
// its query, every header but the connection's, body — and the reply comes
// back as Anthropic sent it; the call is in the usage log as the account
// the request names, marked passthrough, with what the reply cost.
func TestClaudePassthroughVerbatim(t *testing.T) {
	saw := claudeAccountsAndAPI(t, answerStream)
	s := New()
	body := claudeCodeBody("u-b", "hello")
	in := claudeCodeRequest("/v1/messages?beta=true", body)
	sent := in.Header.Clone()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, in)

	if saw.n != 1 || saw.method != "POST" || saw.uri != "/v1/messages?beta=true" || !bytes.Equal(saw.body, body) {
		t.Fatalf("upstream got %d: %s %s\n%s", saw.n, saw.method, saw.uri, saw.body)
	}
	for k, vs := range sent {
		if k == "Accept-Encoding" {
			continue
		}
		if got := saw.header.Values(k); strings.Join(got, ",") != strings.Join(vs, ",") {
			t.Errorf("header %s: sent %q, upstream got %q", k, vs, got)
		}
	}
	for k := range saw.header {
		if sent.Get(k) == "" && !strings.EqualFold(k, "Accept-Encoding") && !strings.EqualFold(k, "Content-Length") {
			t.Errorf("upstream got a header Claude Code didn't send: %s: %q", k, saw.header.Values(k))
		}
	}
	if rec.Code != 200 || rec.Body.String() != claudeStream {
		t.Fatalf("reply %d:\n%s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Vendor-Thing") != "kept" || rec.Header().Get("Request-Id") != "req_1" || rec.Header().Get(ArchiveHeader) != "" {
		t.Fatalf("reply headers: %v", rec.Header())
	}

	recs := usage.Load(time.Time{})
	if len(recs) != 1 {
		t.Fatalf("usage: %+v", recs)
	}
	u := recs[0]
	if !u.Passthrough || u.Provider != "claude" || u.ProviderAccount != "b@example.com" || u.Model != "claude-sonnet-5-5" ||
		u.Input != 3 || u.CacheRead != 1000 || u.CacheWrite != 20 || u.Output != 7 || u.Status != 200 || u.Agent != usage.AgentOf("claude-cli/2.1.288 (external, cli)") || u.Session != "sess-1" {
		t.Fatalf("usage record: %+v", u)
	}
	calls := s.Recent()
	if len(calls) != 1 || !calls[0].Passthrough || calls[0].Status != 200 {
		t.Fatalf("recent calls: %+v", calls)
	}
	// what the reply's headers said b has left is what routing weighs it
	// by: the week, 10% used, decides
	if used, _ := provider.Allowances("claude")["b@example.com"].For("claude-sonnet-5-5", time.Now()); used != 10 {
		t.Fatalf("b's allowance used: %v", used)
	}
}

// An account's Concurrency holds Claude Code's own requests too: past it
// they wait for a slot, in turn.
func TestClaudePassthroughConcurrency(t *testing.T) {
	var mu sync.Mutex
	in, most := 0, 0
	release := make(chan struct{})
	claudeAccountsAndAPI(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		in++
		most = max(most, in)
		mu.Unlock()
		<-release
		mu.Lock()
		in--
		mu.Unlock()
		answerStream(w, r)
	})
	one := 1
	if err := provider.Save(provider.Provider{ID: "claude", MaxConcurrency: &one}); err != nil {
		t.Fatal(err)
	}
	if p, _ := claudeProvider(); p.Concurrency() != 1 {
		t.Fatalf("claude's concurrency: %d", p.Concurrency())
	}
	s := New()
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Handler().ServeHTTP(httptest.NewRecorder(), claudeCodeRequest("/v1/messages", claudeCodeBody("u-b", "hello")))
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(release)
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d of b's requests were out at once", most)
	}
}

// What Claude Code asks its base URL for besides a turn goes through as
// it came, and back, without a record of its own.
func TestClaudePassthroughOtherPaths(t *testing.T) {
	saw := claudeAccountsAndAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":1234}`)
	})
	s := New()
	body := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, claudeCodeRequest("/v1/messages/count_tokens?beta=true", body))
	if saw.uri != "/v1/messages/count_tokens?beta=true" || !bytes.Equal(saw.body, body) || rec.Body.String() != `{"input_tokens":1234}` {
		t.Fatalf("upstream %s %s; reply %d %s", saw.uri, saw.body, rec.Code, rec.Body)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 0 {
		t.Fatalf("usage: %+v", recs)
	}
}

// A refusal comes back as Anthropic gave it, never retried elsewhere, and
// the account it was for rests until the window it filled renews.
func TestClaudePassthroughRefusal(t *testing.T) {
	refusal := `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`
	reset := time.Now().Add(3 * time.Hour).Unix()
	saw := claudeAccountsAndAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		w.Header().Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		w.Header().Set("anthropic-ratelimit-unified-reset", jsonNum(reset))
		w.WriteHeader(429)
		io.WriteString(w, refusal)
	})
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, claudeCodeRequest("/v1/messages?beta=true", claudeCodeBody("u-b", "hello")))
	if saw.n != 1 || rec.Code != 429 || rec.Body.String() != refusal {
		t.Fatalf("upstream asked %d times; reply %d %s", saw.n, rec.Code, rec.Body)
	}
	p, _ := claudeProvider()
	c, ok := claudeCandidate(p, "b@example.com", "claude-sonnet-5-5")
	if !ok {
		t.Fatal("b is not a candidate")
	}
	if r, resting := restOf(c.rest); !resting || r.Status != 429 {
		t.Fatalf("b's rest: %+v %v", r, resting)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].Status != 429 || !recs[0].Passthrough {
		t.Fatalf("usage: %+v", recs)
	}
}

// Masking doesn't touch Claude Code's own requests: what it sent is what
// Anthropic gets, secrets and all.
func TestClaudePassthroughNotMasked(t *testing.T) {
	saw := claudeAccountsAndAPI(t, answerStream)
	settings.Save(settings.Settings{Redact: true, RedactWords: []string{"hello"}})
	s := New()
	body := claudeCodeBody("u-a", "hello sk-proj-abcdefghijklmnopqrstuvwxyz0123456789")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, claudeCodeRequest("/v1/messages?beta=true", body))
	if !bytes.Equal(saw.body, body) {
		t.Fatalf("upstream got\n%s\nnot\n%s", saw.body, body)
	}
}

// Only Claude Code's own requests, signed with its subscription and from
// this machine, are passed through: one from another machine, one with
// magpie's key or a token of magpie's, or another client's, is served as
// before and never reaches the stand-in for Anthropic.
func TestClaudePassthroughOnlyClaudeCodesOwn(t *testing.T) {
	saw := claudeAccountsAndAPI(t, answerStream)
	s := New()
	for name, change := range map[string]func(r *http.Request){
		"remote":        func(r *http.Request) { r.RemoteAddr = "192.168.1.20:50000" },
		"magpie token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer magpie") },
		"magpie key":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-magpie-key-abc") },
		"no oauth beta": func(r *http.Request) { r.Header.Set("anthropic-beta", "claude-code-20250219") },
		"other client":  func(r *http.Request) { r.Header.Set("User-Agent", "pi/1.0") },
	} {
		in := claudeCodeRequest("/v1/messages", claudeCodeBody("u-a", "hello"))
		change(in)
		if claudeOwn(in) {
			t.Errorf("%s: taken as Claude Code's own", name)
		}
		s.Handler().ServeHTTP(httptest.NewRecorder(), in)
	}
	if saw.n != 0 {
		t.Fatalf("the stand-in for Anthropic was asked %d times", saw.n)
	}
}

// An account magpie doesn't know is passed through all the same, its
// usage under no account.
func TestClaudePassthroughUnknownAccount(t *testing.T) {
	saw := claudeAccountsAndAPI(t, answerStream)
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, claudeCodeRequest("/v1/messages", claudeCodeBody("u-zzz", "hello")))
	if saw.n != 1 || rec.Code != 200 {
		t.Fatalf("upstream asked %d times; reply %d", saw.n, rec.Code)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].ProviderAccount != "" || !recs[0].Passthrough {
		t.Fatalf("usage: %+v", recs)
	}
}

// The windows a reply's headers tell, as Claude Code reads them.
func TestClaudeHeaderLimits(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1791038400")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.02")
	h.Set("anthropic-ratelimit-unified-7d-reset", "1791396000")
	h.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.5")
	h.Set("anthropic-ratelimit-unified-status", "rejected")
	h.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_opus")
	h.Set("anthropic-ratelimit-unified-reset", "1791400000")
	got := claudeHeaderLimits(h)
	want := []provider.ClaudeLimit{{Kind: "five_hour", Used: 0.42, ResetsAt: 1791038400}, {Kind: "seven_day", Used: 0.02, ResetsAt: 1791396000},
		{Kind: "seven_day_overage_included", Used: 0.5}, {Kind: "seven_day_opus", Used: 1, ResetsAt: 1791400000}}
	if len(got) != len(want) {
		t.Fatalf("limits: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("limit %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }
