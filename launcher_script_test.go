package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// runLauncher runs scripts/claude-launcher.sh with args against a gateway
// answering answer, a stand-in Claude Code writing what it was run with,
// and a shell environment that points elsewhere: it gives the stand-in's
// record, the query the gateway was asked, the exit code and stderr.
func runLauncher(t *testing.T, gateway string, args ...string) (record, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude-real")
	out := filepath.Join(dir, "ran")
	os.WriteFile(fake, []byte(`#!/bin/sh
{ echo "args=$*"; echo "base=${ANTHROPIC_BASE_URL:-}"; echo "token=${ANTHROPIC_AUTH_TOKEN:-}"; echo "key=${ANTHROPIC_API_KEY:-}"
  echo "config=${CLAUDE_CONFIG_DIR:-}"; echo "secure=${CLAUDE_SECURESTORAGE_CONFIG_DIR:-}"; } > `+out+`
`), 0o755)
	cmd := exec.Command("sh", "scripts/claude-launcher.sh")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = append(os.Environ(), "MAGPIE_GATEWAY="+gateway, "MAGPIE_CLAUDE="+fake,
		"ANTHROPIC_AUTH_TOKEN=magpie", "ANTHROPIC_API_KEY=sk-x", "CLAUDE_CONFIG_DIR=/elsewhere", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/slot")
	var errb strings.Builder
	cmd.Stderr = &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	return string(b), errb.String(), code
}

// The launcher asks magpie for the account with the model and effort on
// the command line, and runs Claude Code as it: through the gateway, in
// the account's config directory when magpie names one, nothing from the
// shell sending it elsewhere or as another, the arguments as given.
func TestClaudeLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script")
	}
	var mu sync.Mutex
	var asked string
	configDir := "/accounts/b"
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = r.URL.RequestURI()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"account":"b@example.com","configDir":"` + configDir + `"}`))
	}))
	defer gw.Close()

	ran, stderr, code := runLauncher(t, gw.URL, "-p", "--model", "claude-sonnet-5-5", "--effort=high", "say hi")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if asked != "/v1/magpie/claude-launch?model=claude-sonnet-5-5&effort=high" {
		t.Fatalf("asked %s", asked)
	}
	for _, want := range []string{"args=-p --model claude-sonnet-5-5 --effort=high say hi\n", "base=" + gw.URL + "\n", "token=\n", "key=\n", "config=/accounts/b\n", "secure=\n"} {
		if !strings.Contains(ran, want) {
			t.Errorf("missing %q in\n%s", want, ran)
		}
	}

	// the account Claude Code is signed in to: its own config directory
	configDir = ""
	ran, stderr, code = runLauncher(t, gw.URL)
	if code != 0 || !strings.Contains(ran, "config=\n") || !strings.Contains(asked, "model=&effort=") {
		t.Fatalf("exit %d %s; asked %s; ran\n%s", code, stderr, asked, ran)
	}
}

// With magpie not running, or no account to run as, the launcher says so
// and stops: Claude Code isn't started some other way.
func TestClaudeLauncherStops(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script")
	}
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	ran, stderr, code := runLauncher(t, url)
	if code != 69 || ran != "" || !strings.Contains(stderr, "magpie isn't running") {
		t.Fatalf("magpie down: exit %d, ran %q, said %s", code, ran, stderr)
	}

	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"magpie has no Claude account: add one on the Providers page"}}`))
	}))
	defer none.Close()
	ran, stderr, code = runLauncher(t, none.URL)
	if code != 69 || ran != "" || !strings.Contains(stderr, "magpie has no Claude account") {
		t.Fatalf("no account: exit %d, ran %q, said %s", code, ran, stderr)
	}
}
