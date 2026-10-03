package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/scripts"
)

// passthroughSettings puts settings.json before in a home of the test's own,
// with a provider of magpie's, and gives the path and a reader of it, an
// object emptied out read as none.
func passthroughSettings(t *testing.T, before string) (home, path string, parse func() map[string]any) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	parse = func() map[string]any {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for k, v := range m {
			if o, ok := v.(map[string]any); ok && len(o) == 0 {
				delete(m, k)
			}
		}
		return m
	}
	return home, path, parse
}

// Subscription passthrough leaves Claude Code on its own sign-in with the
// gateway its endpoint and nothing else of magpie's: no token, no model
// names — from nothing set, from the user's own relay, and from magpie's
// models alike. It counts as wired; disconnecting puts back what the user
// had before magpie.
func TestClaudePassthrough(t *testing.T) {
	for name, tc := range map[string]struct {
		before string
		magpie bool // on magpie's models first
	}{
		"nothing set":      {before: `{"theme":"dark"}`},
		"own relay":        {before: `{"theme":"dark","model":"opus","env":{"ANTHROPIC_BASE_URL":"https://relay.example","ANTHROPIC_AUTH_TOKEN":"sk-relay","DISABLE_TELEMETRY":"1"}}`},
		"own api key":      {before: `{"env":{"ANTHROPIC_API_KEY":"sk-ant-api"}}`},
		"on magpie models": {before: `{"theme":"dark","model":"sonnet"}`, magpie: true},
	} {
		t.Run(name, func(t *testing.T) {
			home, path, parse := passthroughSettings(t, tc.before)
			want := parse()
			a := claude(home)
			if tc.magpie {
				if err := a.Apply("model", "deepseek/pro"); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.UsePassthrough(true); err != nil {
				t.Fatal(err)
			}
			env, _ := parse()["env"].(map[string]any)
			if env["ANTHROPIC_BASE_URL"] != gateway.URL() {
				t.Fatalf("endpoint: %v", env)
			}
			for _, k := range append(claudeEnv[1:], "ANTHROPIC_API_KEY", claudeCapsEnv, claudeContextEnv) {
				if v, ok := env[k]; ok {
					t.Errorf("%s left in: %v", k, v)
				}
			}
			if m, _ := edit.GetJSON(path, "model"); isMagpie(m) {
				t.Errorf("magpie's model left as Claude Code's: %s", m)
			}
			if theme, _ := edit.GetJSON(path, "theme"); tc.before != `{"env":{"ANTHROPIC_API_KEY":"sk-ant-api"}}` && theme != "dark" {
				t.Errorf("the user's own setting lost: theme %q", theme)
			}
			if !a.Passthrough() || !a.PassthroughSet() || !a.Wired() {
				t.Fatalf("passthrough %v, set %v, wired %v", a.Passthrough(), a.PassthroughSet(), a.Wired())
			}
			if d := a.Drift(); d != nil {
				t.Fatalf("drift right after: %+v", d)
			}
			// the launcher is there to start Claude Code through, and the
			// command to start it so is offered
			if b, err := os.ReadFile(filepath.Join(scripts.LauncherDir(), "claude")); err != nil || !bytes.Equal(b, scripts.ClaudeLauncher) {
				t.Fatalf("launcher: %v", err)
			}
			if l := a.Launch(); !strings.Contains(l, scripts.LauncherDir()) || !strings.HasSuffix(l, " claude") {
				t.Fatalf("launch: %q", l)
			}
			if err := a.Disconnect(); err != nil {
				t.Fatal(err)
			}
			if got := parse(); !reflect.DeepEqual(got, want) {
				b, _ := os.ReadFile(path)
				t.Fatalf("after disconnect:\n%s\nwant %v", b, want)
			}
			if a.Passthrough() || a.PassthroughSet() || a.Wired() {
				t.Fatal("still passing through")
			}
		})
	}
}

// Through passthrough Claude Code picks Anthropic's models as its own and
// stays there; its effort is its own too. Its default takes magpie out
// altogether; one of magpie's models moves it onto magpie's models, and
// disconnecting from there still puts back what was there before either.
func TestClaudePassthroughPicks(t *testing.T) {
	home, path, parse := passthroughSettings(t, `{"env":{"ANTHROPIC_BASE_URL":"https://relay.example","ANTHROPIC_AUTH_TOKEN":"sk-relay"}}`)
	want := parse()
	a := claude(home)
	if err := a.UsePassthrough(true); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "opus"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("effort", "high"); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(path, "model"); m != "opus" || !a.Passthrough() {
		t.Fatalf("model %q, passthrough %v", m, a.Passthrough())
	}
	if err := a.Apply("model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if a.Passthrough() || a.PassthroughSet() || !a.Wired() {
		t.Fatalf("on magpie's models: passthrough %v, set %v, wired %v", a.Passthrough(), a.PassthroughSet(), a.Wired())
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	got := parse()
	delete(got, "effortLevel")
	delete(got, "modelSettings")
	delete(got, "model")
	if !reflect.DeepEqual(got, want) {
		b, _ := os.ReadFile(path)
		t.Fatalf("after disconnect:\n%s\nwant %v", b, want)
	}

	// the default: Claude Code as installed
	if err := a.UsePassthrough(true); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); u != "" || a.Passthrough() || a.PassthroughSet() {
		t.Fatalf("after its default: endpoint %q, passthrough %v", u, a.Passthrough())
	}
}

// Something else changing Claude Code's settings so that it no longer
// passes its own requests through is drift, and applying again wires it
// back.
func TestClaudePassthroughDrift(t *testing.T) {
	for name, change := range map[string]func(path string) error{
		"endpoint gone": func(path string) error { return edit.DelJSON(path, "env.ANTHROPIC_BASE_URL") },
		"token added": func(path string) error {
			return edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: "sk-x"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			home, path, _ := passthroughSettings(t, `{}`)
			a := claude(home)
			if err := a.UsePassthrough(true); err != nil {
				t.Fatal(err)
			}
			if err := change(path); err != nil {
				t.Fatal(err)
			}
			d := a.Drift()
			if d == nil || d.Kind != "unwired" {
				t.Fatalf("drift: %+v", d)
			}
			if err := a.Reapply(); err != nil {
				t.Fatal(err)
			}
			if !a.Passthrough() || a.Drift() != nil {
				t.Fatalf("after applying again: passthrough %v, drift %+v", a.Passthrough(), a.Drift())
			}
		})
	}
}
