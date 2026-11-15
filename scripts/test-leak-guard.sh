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
hit asia "$tok" "k=ASIA$(rep 16 A)"
hit xoxc "$tok" "t=xo""xc-1234567890-$a"
hit xoxe "$tok" "t=xo""xe-1234567890-$a"
hit privkey "$tok" "-----BEGIN RSA PRIV""ATE KEY-----"
hit users "$path" "/Us""ers/someone/project"
hit users-lower "$path" "/us""ers/someone/project"
hit home "$path" "cd /ho""me/someone/project"
hit home-upper "$path" "cd /ho""me/Someone/project"
hit tilde "$path" "cd ~""/project"
hit tilde-quoted-dot "$path" 'open("~''/.ssh/id_rsa")'
hit tilde-quoted-dir "$path" 'p = "~''/src/some/checkout"'
hit winpath "$path" 'C:\Us''ers\someone'
hit winpath-lower "$path" 'C:\us''ers\name\project'
miss aspnet "$path" 'return Redirect("~/Home/Index");'
miss aspnet-lower "$path" 'return Redirect("~/home/index");'
miss aspnet-import "$path" 'Url.Content("~/import/x")'
miss url "$path" 'see https://example.org/home/page'
miss shortnpm "$tok" "npm_install_all"

# A pattern git grep cannot compile must fail the check closed and stay out of the log.
fn=$(awk '/^ *check\(\) \{/{f=1} f{print} f&&/^          \}$/{exit}' "$wf")
[ -n "$fn" ] || { echo "check() not found in $wf" >&2; fail=1; }
printf 'x\n' >"$tmp/f.txt"
git -C "$tmp" add f.txt
out=$(cd "$tmp" && failed=0 && eval "$fn" && check "a bad pattern" -e 'secret-(' && echo "failed=$failed" 2>&1)
case $out in *failed=1*) ;; *) echo "FAIL: an invalid pattern did not fail closed" >&2; fail=1 ;; esac
case $out in *secret*) echo "FAIL: an invalid pattern reached the log" >&2; fail=1 ;; esac
exit $fail
