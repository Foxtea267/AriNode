#!/usr/bin/env bash
# Called only after the release workflow's tests and official builds pass.
set -euo pipefail

: "${VERSION:?VERSION is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"
[[ "$VERSION" =~ ^[0-9]{8}-[a-f0-9]{8}$ ]] || { echo 'Invalid release version' >&2; exit 1; }
[[ "$GITHUB_SHA" =~ ^[a-f0-9]{40}$ ]] || { echo 'Invalid release commit' >&2; exit 1; }

# gh --target does not change a pre-existing tag. Refuse to attach these
# binaries to a tag that identifies a different commit, including annotated tags.
remote_tag=$(git ls-remote --tags origin "refs/tags/$VERSION")
if [[ -n "$remote_tag" ]]; then
  git fetch --no-tags origin "refs/tags/$VERSION"
  tag_commit=$(git rev-parse 'FETCH_HEAD^{commit}')
  [[ "$tag_commit" == "$GITHUB_SHA" ]] || { echo "Tag $VERSION refers to a different commit" >&2; exit 1; }
fi

# main push, an explicit tag and a manual rerun can refer to the same version.
# A complete stable release is already the desired outcome; incomplete or
# conflicting records must remain visible as failures instead of being replaced.
if metadata=$(gh release view "$VERSION" --json isDraft,isPrerelease,assets 2>/dev/null); then
  [[ -n "$remote_tag" ]] || { echo 'Existing release has no matching tag' >&2; exit 1; }
  python3 -c '
import json, sys
release = json.load(sys.stdin)
expected = {"arinode-linux-amd64.tar.gz", "arinode-linux-arm64.tar.gz",
            "arinode-xboard-plugin.zip", "arinode-komari-plugin.zip", "SHA256SUMS"}
assets = release.get("assets", [])
valid = (release.get("isDraft") is False and release.get("isPrerelease") is False
         and len(assets) == len(expected)
         and {asset.get("name") for asset in assets} == expected
         and all(isinstance(asset.get("size"), int) and asset["size"] > 0 for asset in assets))
sys.exit(0 if valid else 1)
  ' <<< "$metadata" || { echo "Release $VERSION is incomplete or not stable" >&2; exit 1; }
  echo "Stable release $VERSION already published with all official assets"
  exit 0
fi

# Avoid promoting an older commit when main has already advanced.
git fetch origin main
latest=false
if [[ "$(git rev-parse origin/main)" == "$GITHUB_SHA" ]]; then
  latest=true
fi
gh release create "$VERSION" dist-release/*.tar.gz dist-release/*.zip dist-release/SHA256SUMS \
  --title "$VERSION" --target "$GITHUB_SHA" --generate-notes "--latest=$latest"
