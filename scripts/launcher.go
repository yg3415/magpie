// Package scripts holds the scripts magpie installs for the user: the
// launcher Claude Code starts through for subscription passthrough.
package scripts

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
)

// ClaudeLauncher is claude-launcher.sh, magpie's launcher for Claude Code.
//
//go:embed claude-launcher.sh
var ClaudeLauncher []byte

// LauncherDir is where magpie puts its launcher, as `claude`, for the user
// to put ahead of Claude Code in PATH.
func LauncherDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "magpie", "bin")
}

// InstallClaudeLauncher writes the launcher to LauncherDir as `claude`,
// when it isn't there as it is now, and answers its path.
func InstallClaudeLauncher() (string, error) {
	path := filepath.Join(LauncherDir(), "claude")
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, ClaudeLauncher) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".magpie-tmp"
	if err := os.WriteFile(tmp, ClaudeLauncher, 0o755); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
