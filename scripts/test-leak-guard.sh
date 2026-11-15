#!/usr/bin/env bash
# Plants one hit per token and path shape in a scratch repository and checks that the
# patterns in .github/workflows/leak-guard.yml find it, and that the usual lookalikes
# (ASP.NET ~/ routes, URLs with /home/) pass. The planted strings are built from parts so
# this file does not match the patterns itself.
set -euo pipefail
cd "$(dirname "$0")/.."
wf=$PWD/.github/workflows/leak-guard.yml
pattern() { sed -n "s/^ *check \"$1\" -e '\\(.*\\)'\$/\\1/p" "$wf"; }
tok=$(pattern 'key material or a live token')
path=$(pattern 'a local path')
[ -n "$tok" ] && [ -n "$path" ] || { echo "patterns not found in $wf" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git -C "$tmp" init -q
rep() { printf "$2%.0s" $(seq "$1"); }
a=$(rep 24 x)
fail=0
found() { # pattern, text
  printf '%s\n' "$2" >"$tmp/f.txt"
  git -C "$tmp" add f.txt
  git -C "$tmp" grep -I -n -o -P -e "$1" >/dev/null
}
hit() { found "$2" "$3" || { echo "FAIL: not found: $1" >&2; fail=1; }; }
miss() { ! found "$2" "$3" || { echo "FAIL: false hit: $1" >&2; fail=1; }; }

hit ghp "$tok" "t=ghp_$a"
hit gho "$tok" "t=gho_$a"
hit ghs "$tok" "t=ghs_$a"
hit ghu "$tok" "t=ghu_$a"
hit ghr "$tok" "t=ghr_$a"
hit github_pat "$tok" "t=github_pat_$a"
hit sk_live "$tok" "k=sk_live_$a"
hit sk_test "$tok" "k=sk_test_$a"
hit glpat "$tok" "t=glpat-$a"
hit xoxb "$tok" "t=xo""xb-1234567890-$a"
hit xoxp "$tok" "t=xo""xp-1234567890-$a"
hit npm "$tok" "t=npm_$(rep 36 x)"
hit akia "$tok" "k=AKIA$(rep 16 A)"
hit privkey "$tok" "-----BEGIN RSA PRIV""ATE KEY-----"
hit users "$path" "/Us""ers/someone/project"
hit home "$path" "cd /ho""me/someone/project"
hit tilde "$path" "cd ~""/project"
hit winpath "$path" 'C:\Us''ers\someone'
miss aspnet "$path" 'return Redirect("~/Home/Index");'
miss url "$path" 'see https://example.org/home/page'
miss shortnpm "$tok" "npm_install_all"
exit $fail
