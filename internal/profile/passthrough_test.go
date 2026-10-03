package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// A profile keeps whether Claude Code passes its own requests through, and
// applying it brings that back, after the model; a profile from before
// leaves it as it is. Its details name it only where it is on.
func TestProfilePassthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{}`), 0o644)
	claude := func() *agent.Agent {
		a, err := agent.Find("claude")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if err := claude().UsePassthrough(true); err != nil {
		t.Fatal(err)
	}
	p, err := Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if p.Fields["claude."+PassthroughKey] != "on" {
		t.Fatalf("snapshot: %v", p.Fields)
	}
	if err := claude().UsePassthrough(false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if !claude().Passthrough() {
		b, _ := os.ReadFile(path)
		t.Fatalf("not passing through after applying:\n%s", b)
	}
	old := Profile{Fields: map[string]string{"claude.model": p.Fields["claude.model"]}}
	if _, err := Apply(old); err != nil {
		t.Fatal(err)
	}
	if !claude().Passthrough() {
		t.Fatal("a profile from before took passthrough out")
	}
	var shown bool
	for _, g := range Details(p) {
		for _, it := range g.Fields {
			if it.Key == PassthroughKey {
				shown = it.Label == "Subscription passthrough" && it.Value == "on"
			}
		}
	}
	if !shown {
		t.Fatalf("details: %+v", Details(p))
	}
	off := Profile{Fields: map[string]string{"claude." + PassthroughKey: "off"}}
	for _, g := range Details(off) {
		for _, it := range g.Fields {
			if it.Key == PassthroughKey {
				t.Fatalf("off shown: %+v", it)
			}
		}
	}
}
