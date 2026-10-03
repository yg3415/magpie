package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A saved account's config directory takes the user's own config but for
// the account: settings, instructions, skills and sessions linked in, the
// user's MCP servers and folders copied into its .claude.json beside the
// account it is. What it had in a linked one's place goes aside, the
// bridge's leftover sessions dropped; a second share changes nothing.
func TestShareClaudeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	own := filepath.Join(home, ".claude")
	os.MkdirAll(filepath.Join(own, "skills", "s"), 0o700)
	os.MkdirAll(filepath.Join(own, "projects", "-Users-me-work"), 0o700)
	os.WriteFile(filepath.Join(own, "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:3425"}}`), 0o600)
	os.WriteFile(filepath.Join(own, "CLAUDE.md"), []byte("be brief"), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"},
		"mcpServers":   map[string]any{"zpilot": map[string]any{"command": "zp"}},
		"projects":     map[string]any{"/Users/me/work": map[string]any{"hasTrustDialogAccepted": true}},
		"theme":        "dark", "userID": "dev-a", "numStartups": 12,
	}), 0o600)

	acct := filepath.Join(home, ".config", "magpie", "claude-accounts", "b")
	tmp := os.TempDir()
	os.MkdirAll(filepath.Join(acct, "projects", claudeProjectName(filepath.Join(tmp, "magpie-claude-123"))), 0o700)
	os.MkdirAll(filepath.Join(acct, "projects", "-Users-me-other"), 0o700)
	os.WriteFile(filepath.Join(acct, "settings.json"), []byte(`{}`), 0o600)
	os.WriteFile(filepath.Join(acct, ".credentials.json"), []byte(`{"claudeAiOauth":{}}`), 0o600)
	os.WriteFile(filepath.Join(acct, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"},
		"projects":     map[string]any{"/Users/me/scratch": map[string]any{"hasTrustDialogAccepted": true}},
		"userID":       "dev-b",
	}), 0o600)

	for range 2 {
		if err := shareClaudeConfig(acct); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"settings.json", "CLAUDE.md", "skills", "projects"} {
		if got, err := os.Readlink(filepath.Join(acct, name)); err != nil || got != filepath.Join(own, name) {
			t.Errorf("%s: linked to %q (%v)", name, got, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(acct, "agents")); err == nil {
		t.Error("agents linked, though the user has none")
	}
	if b, _ := os.ReadFile(filepath.Join(acct, ".credentials.json")); string(b) != `{"claudeAiOauth":{}}` {
		t.Errorf("the account's sign-in: %s", b)
	}
	aside, _ := filepath.Glob(filepath.Join(acct, "*.before-shared-*"))
	var kept []string
	for _, a := range aside {
		kept = append(kept, filepath.Base(a))
	}
	if len(aside) != 2 {
		t.Fatalf("set aside: %v", kept)
	}
	for _, a := range aside {
		if filepath.Base(a)[:8] == "projects" {
			if entries, _ := os.ReadDir(a); len(entries) != 1 || entries[0].Name() != "-Users-me-other" {
				t.Fatalf("projects set aside: %v", entries)
			}
		}
	}

	var prof map[string]any
	b, _ := os.ReadFile(filepath.Join(acct, ".claude.json"))
	json.Unmarshal(b, &prof)
	if prof["oauthAccount"].(map[string]any)["emailAddress"] != "b@example.com" || prof["userID"] != "dev-b" || prof["numStartups"] != nil {
		t.Fatalf("the account's own: %s", b)
	}
	projects, _ := prof["projects"].(map[string]any)
	if prof["theme"] != "dark" || prof["mcpServers"].(map[string]any)["zpilot"] == nil || projects["/Users/me/work"] == nil || projects["/Users/me/scratch"] == nil {
		t.Fatalf("the user's shared: %s", b)
	}
}
