package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// magpie claude passthrough on|off turns Claude Code's subscription
// passthrough on and off; anything else is refused.
func TestPassthroughCmd(t *testing.T) {
	groupsHome(t)
	path := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{}`), 0o644)
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := passthroughCmd(a, []string{"on"}); err != nil || !a.Passthrough() {
		t.Fatal(err, a.Passthrough())
	}
	if err := passthroughCmd(a, nil); err != nil {
		t.Fatal(err)
	}
	if err := passthroughCmd(a, []string{"off"}); err != nil || a.Passthrough() {
		t.Fatal(err, a.Passthrough())
	}
	if err := passthroughCmd(a, []string{"maybe"}); err == nil {
		t.Fatal("took maybe")
	}
	cx, err := agent.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := passthroughCmd(cx, []string{"on"}); err == nil {
		t.Fatal("Codex took passthrough")
	}
}
