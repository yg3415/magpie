#!/bin/sh
# magpie's launcher for Claude Code (subscription passthrough).
#
# Asks the magpie on this machine which Claude account to run as — the
# Claude subscription's Routing, for the model and effort asked — and runs
# Claude Code as it, its requests sent through magpie, which passes them to
# Anthropic as they are and counts them. Everything else on the command
# line goes to Claude Code as given.
#
# Install: put a link named `claude` to this file ahead of Claude Code in
# PATH, e.g.
#   mkdir -p ~/.local/share/magpie/bin
#   ln -sf "$PWD/scripts/claude-launcher.sh" ~/.local/share/magpie/bin/claude
#   export PATH="$HOME/.local/share/magpie/bin:$PATH"
#
# MAGPIE_GATEWAY   the gateway (default http://127.0.0.1:3425)
# MAGPIE_CLAUDE_ACCOUNT  run as this account (its email) instead of the one
#                  routing picks; it must be ticked in magpie
# MAGPIE_CLAUDE    the Claude Code to run (default: the newest installed)
set -eu

gateway=${MAGPIE_GATEWAY:-http://127.0.0.1:3425}
gateway=${gateway%/}

say() { printf 'magpie launcher: %s\n' "$*" >&2; }

# the real Claude Code: never this launcher, however it is linked
self=$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")
[ -L "$0" ] && self=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$0" 2>/dev/null || echo "$self")
real=${MAGPIE_CLAUDE:-}
if [ -z "$real" ]; then
  versions=$HOME/.local/share/claude/versions
  if [ -d "$versions" ]; then
    real=$(ls "$versions" 2>/dev/null | grep -E '^[0-9]+(\.[0-9]+)+$' | sort -t. -k1,1n -k2,2n -k3,3n -k4,4n | tail -1)
    [ -n "$real" ] && real=$versions/$real
  fi
fi
if [ -z "$real" ]; then
  IFS=:
  for d in $PATH; do
    c=$d/claude
    [ -x "$c" ] || continue
    r=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$c" 2>/dev/null || echo "$c")
    [ "$r" = "$self" ] && continue
    real=$c
    break
  done
  unset IFS
fi
if [ -z "$real" ] || [ ! -x "$real" ]; then
  say "Claude Code isn't installed (set MAGPIE_CLAUDE to it)"
  exit 127
fi

# the model and effort asked, for the account to fit them
model= effort= prev=
for a in "$@"; do
  case $prev in
    --model) model=$a ;;
    --effort) effort=$a ;;
  esac
  case $a in
    --model=*) model=${a#--model=} ;;
    --effort=*) effort=${a#--effort=} ;;
  esac
  prev=$a
done

enc() { python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }
if ! answer=$(curl -sS --max-time 15 -w '\n%{http_code}' "$gateway/v1/magpie/claude-launch?model=$(enc "$model")&effort=$(enc "$effort")$( [ -n "${MAGPIE_CLAUDE_ACCOUNT:-}" ] && printf '&account=%s' "$(enc "$MAGPIE_CLAUDE_ACCOUNT")")" 2>&1); then
  say "magpie isn't running at $gateway ($answer). Start magpie, or run Claude Code itself."
  exit 69
fi
code=$(printf '%s' "$answer" | tail -n 1)
body=$(printf '%s' "$answer" | sed '$d')
field() { printf '%s' "$body" | python3 -c 'import json, sys
d = json.load(sys.stdin)
v = d.get(sys.argv[1])
if v is None and isinstance(d.get("error"), dict): v = d["error"].get(sys.argv[1])
print(v or "")' "$1" 2>/dev/null; }
if [ "$code" != 200 ]; then
  say "magpie couldn't pick a Claude account: $(field message || true) (HTTP $code)"
  exit 69
fi
account=$(field account)
dir=$(field configDir)
if [ -z "$account" ]; then
  say "magpie answered without an account: $body"
  exit 69
fi

# nothing from the shell may send Claude Code elsewhere or as another
unset MAGPIE_CLAUDE_ACCOUNT ANTHROPIC_AUTH_TOKEN ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN CLAUDE_SECURESTORAGE_CONFIG_DIR CLAUDE_CONFIG_DIR
export ANTHROPIC_BASE_URL="$gateway"
if [ -n "$dir" ]; then
  export CLAUDE_CONFIG_DIR="$dir"
fi
[ -n "${MAGPIE_LAUNCHER_QUIET:-}" ] || [ ! -t 2 ] || say "Claude Code runs as $account"
exec "$real" "$@"
