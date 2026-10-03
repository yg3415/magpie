package gateway

// A saved Claude account runs Claude Code in a config directory of its own
// (CLAUDE_CONFIG_DIR): only there do its sign-in and the account its
// requests name agree — Claude Code reads the account it says it is from
// the .claude.json beside its config, not from where the sign-in is kept.
// Started through the launcher, Claude Code there is the user's all the
// same: their settings, instructions, agents, commands, skills, hooks and
// plugins, their sessions and history, are linked in from their own
// config, and their own .claude.json's MCP servers and folders are copied
// into the account's, beside the account it is. The bridge's runs in the
// same directory read none of it: they start with no settings sources and
// strict MCP config.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeShared are what a saved account's config directory takes from the
// user's own Claude Code config, linked: everything but the sign-in, the
// account's .claude.json and Claude Code's caches of its own.
var claudeShared = []string{"settings.json", "settings.local.json", "CLAUDE.md", "agents", "commands", "skills", "hooks",
	"plugins", "output-styles", "keybindings.json", "projects", "history.jsonl", "todos", "plans", "file-history"}

// claudeProfileShared are the user's own .claude.json's settings a saved
// account's takes: MCP servers, folders (trusted, and their own servers and
// tools), and how Claude Code looks. projects merges, the user's own
// winning for a folder both have.
var claudeProfileShared = []string{"mcpServers", "projects", "theme", "editorMode", "verbose", "preferredNotifChannel",
	"hasCompletedOnboarding", "lastOnboardingVersion"}

// claudeProfileOf is where Claude Code keeps .claude.json for config
// directory dir: beside it, or in the home folder for the user's own.
func claudeProfileOf(dir string) string {
	if dir == claudeConfigDir() && os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(dir, ".claude.json")
}

// shareClaudeConfig makes a saved account's config directory the user's
// own but for the account: claudeShared linked in from the user's config,
// claudeProfileShared copied into its .claude.json. What the directory had
// in a linked one's place goes aside — the bridge's own leftover sessions
// are dropped, as sweepBridgeProjects drops them from the user's.
func shareClaudeConfig(dir string) error {
	own := claudeConfigDir()
	if dir == "" || filepath.Clean(dir) == filepath.Clean(own) {
		return nil
	}
	var errs []error
	for _, name := range claudeShared {
		if err := linkShared(filepath.Join(own, name), filepath.Join(dir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := mergeClaudeProfile(claudeProfileOf(own), claudeProfileOf(dir)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// linkShared makes link a symlink to target, the user's own, when target
// is there.
func linkShared(target, link string) error {
	if _, err := os.Stat(target); err != nil {
		// not in the user's config: a link the account's directory has to
		// it is dropped, whatever else is there kept
		if cur, err := os.Readlink(link); err == nil && cur == target {
			return os.Remove(link)
		}
		return nil
	}
	fi, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		if cur, _ := os.Readlink(link); cur == target {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	default:
		if err := setAside(link, fi); err != nil {
			return err
		}
	}
	return os.Symlink(target, link)
}

// setAside moves what is at path out of a link's way: an empty folder is
// removed; the bridge's leftover sessions in a projects folder are
// dropped, and what is left is kept beside it, named for when.
func setAside(path string, fi fs.FileInfo) error {
	if fi.IsDir() {
		if filepath.Base(path) == "projects" {
			sweepBridgeProjects(filepath.Dir(path), os.TempDir())
		}
		if entries, err := os.ReadDir(path); err == nil && len(entries) == 0 {
			return os.Remove(path)
		}
	}
	return os.Rename(path, path+".before-shared-"+time.Now().Format("20060102-150405"))
}

// mergeClaudeProfile copies claudeProfileShared from the user's own
// .claude.json into an account's, the rest of the account's kept as it is.
func mergeClaudeProfile(ownPath, acctPath string) error {
	own, err := readJSONObject(ownPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	acct, err := readJSONObject(acctPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if acct == nil {
		acct = map[string]any{}
	}
	before, _ := json.Marshal(acct)
	for _, k := range claudeProfileShared {
		v, ok := own[k]
		if !ok {
			continue
		}
		if k == "projects" {
			merged := map[string]any{}
			if m, ok := acct[k].(map[string]any); ok {
				for p, e := range m {
					merged[p] = e
				}
			}
			if m, ok := v.(map[string]any); ok {
				for p, e := range m {
					merged[p] = e
				}
			}
			v = merged
		}
		acct[k] = v
	}
	after, err := json.Marshal(acct)
	if err != nil || bytes.Equal(before, after) {
		return err
	}
	b, err := json.MarshalIndent(acct, "", "  ")
	if err != nil {
		return err
	}
	tmp := acctPath + ".magpie-tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, acctPath)
}

// readJSONObject is the JSON object in path, its numbers kept as written.
func readJSONObject(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}
