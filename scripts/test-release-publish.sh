#!/usr/bin/env bash
# Exercise release decisions with isolated git/gh fixtures; no network or upload.
set -euo pipefail
script=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/publish-release.sh
export VERSION=20261113-12345678
export GITHUB_SHA=1234567812345678123456781234567812345678
export GITHUB_REF=refs/heads/main

git() {
  case "$*" in
    'ls-remote --tags origin '*)
      if [[ "${SCENARIO}" != fresh* ]]; then printf '%s\trefs/tags/%s\n' "$GITHUB_SHA" "$VERSION"; fi ;;
    'fetch '*) return 0 ;;
    "rev-parse FETCH_HEAD^{commit}")
      if [[ "$SCENARIO" == tag-conflict ]]; then printf '%040d\n' 0; else echo "$GITHUB_SHA"; fi ;;
    'rev-parse origin/main')
      if [[ "$SCENARIO" == fresh-old ]]; then printf '%040d\n' 0; else echo "$GITHUB_SHA"; fi ;;
    *) echo "Unexpected git command: $*" >&2; return 2 ;;
  esac
}
gh() {
  case "$1 $2" in
    'release view')
      if [[ "$SCENARIO" == fresh* ]]; then return 1; fi
      FIXTURE_SCENARIO="$SCENARIO" python3 -c '
import json, os
scenario = os.environ["FIXTURE_SCENARIO"]
names = ["arinode-linux-amd64.tar.gz", "arinode-linux-arm64.tar.gz",
         "arinode-xboard-plugin.zip", "arinode-komari-plugin.zip", "SHA256SUMS"]
assets = [{"name": name, "size": 10} for name in names]
if scenario == "incomplete": assets.pop()
if scenario == "empty-asset": assets[0]["size"] = 0
print(json.dumps({"isDraft": scenario == "draft", "isPrerelease": scenario == "prerelease", "assets": assets}))
' ;;
    'release create')
      local expected=--latest=true
      if [[ "$SCENARIO" == fresh-old ]]; then expected=--latest=false; fi
      [[ "$*" == *"$expected"* ]] || { echo "Wrong latest policy: $*" >&2; return 2; }
      echo "Created fixture release $VERSION $expected" ;;
    *) echo "Unexpected gh command: $*" >&2; return 2 ;;
  esac
}
for scenario in fresh fresh-old existing incomplete empty-asset draft prerelease tag-conflict; do
  export SCENARIO="$scenario"
  if (source "$script"); then
    case "$scenario" in
      fresh|fresh-old|existing) ;;
      *) echo "Incorrectly accepted scenario $scenario" >&2; exit 1 ;;
    esac
  else
    case "$scenario" in
      fresh|fresh-old|existing) echo "Rejected valid scenario $scenario" >&2; exit 1 ;;
    esac
  fi
done
echo 'Release publishing tests passed (new, rerun, old commit, incomplete, draft, prerelease, tag conflict)'
