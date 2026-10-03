package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings' Searcher names the provider, and the model, that searches for
// a model that can't; one that is gone, off or can't search gives way to
// magpie's own pick, and says why.
func TestSearcherChosen(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir()) // no signed-in agent searches
	lists := func(ids ...string) string {
		var b strings.Builder
		for i, id := range ids {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"id":"` + id + `"}`)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"data":[`+b.String()+`]}`)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	ant, oai := lists("claude-haiku-4-5", "claude-opus-4-5"), lists("gpt-5-mini", "gpt-5.5")
	for proto, url := range map[provider.Protocol]string{provider.Anthropic: ant, provider.Responses: oai} {
		hosts := searchHosts[proto]
		searchHosts[proto] = append(slices.Clone(hosts), provider.HostOf(url))
		t.Cleanup(func() { searchHosts[proto] = hosts })
	}
	for _, p := range []provider.Provider{
		{ID: "ant", Name: "Anthropic", Key: "k", Anthropic: ant},
		{ID: "oai", Name: "OpenAI", Key: "k", Responses: oai + "/v1"},
		{ID: "relay", Name: "Relay", Key: "k", Searches: true, Anthropic: lists("claude-haiku-4-5")},
		{ID: "plain", Name: "Plain", Key: "k", Chat: lists("m1") + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	choose := func(v string) {
		t.Helper()
		st := settings.Load()
		st.Searcher = v
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	want := func(id, model, why string) {
		t.Helper()
		p, m, ok := searcher()
		if !ok || p.ID != id || m != model {
			t.Errorf("searcher = %s %s %v, want %s %s", p.ID, m, ok, id, model)
		}
		if got := SearcherUnused(); got != why {
			t.Errorf("unused = %q, want %q", got, why)
		}
	}

	// automatic: Anthropic's API comes before OpenAI's
	want("ant", "claude-haiku-4-5", "")
	if got := AutoSearcher(); got != "Anthropic · claude-haiku-4-5" {
		t.Errorf("auto = %q", got)
	}
	var ids []string
	for _, c := range Searchers() {
		ids = append(ids, c.Provider.ID)
	}
	// the relay said to search is never offered (#359), nor one that can't
	if len(ids) != 2 || ids[0] != "ant" || ids[1] != "oai" {
		t.Errorf("searchers = %v", ids)
	}
	if rs := RelaysSaidToSearch(); len(rs) != 1 || rs[0].ID != "relay" {
		t.Errorf("relays = %v", rs)
	}

	// a provider, with its small model
	choose("oai")
	want("oai", "gpt-5-mini", "")
	if got := Searcher(); got != "OpenAI · gpt-5-mini" {
		t.Errorf("Searcher() = %q", got)
	}
	// a provider and a model of it
	choose("oai/gpt-5.5")
	want("oai", "gpt-5.5", "")
	// a model it no longer lists: its small model
	choose("oai/gpt-4")
	want("oai", "gpt-5-mini", "")

	// one that can't search, a relay said to, one gone: magpie's pick
	choose("plain")
	want("ant", "claude-haiku-4-5", SearcherCant)
	choose("relay/claude-haiku-4-5")
	want("ant", "claude-haiku-4-5", SearcherCant)
	choose("nobody/x")
	want("ant", "claude-haiku-4-5", SearcherGone)

	// one turned off
	choose("oai")
	if err := provider.SetOff("oai", true); err != nil {
		t.Fatal(err)
	}
	want("ant", "claude-haiku-4-5", SearcherOff)
	if err := provider.SetOff("oai", false); err != nil {
		t.Fatal(err)
	}
	want("oai", "gpt-5-mini", "")

	// automatic again
	choose("")
	want("ant", "claude-haiku-4-5", "")
}
