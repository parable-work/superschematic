#!/usr/bin/env bash
# Cut the release tag for the version the tree carries and push it.
#
#   scripts/tag_release.sh
#
# Run on a clean checkout of main after the release pull request (the one
# opened by .github/workflows/release-pr.yml) has merged. The tag push is what
# starts .github/workflows/release.yml; it has to come from a person or a
# personal token, because a tag pushed with the workflow token would not start
# a workflow. The Go sub-module tags (ir/vX.Y.Z, runtime/schema/go/vX.Y.Z,
# runtime/http/go/vX.Y.Z) are not cut here: go-module-tag.yml cuts them at the
# same commit when this tag lands. See CONTRIBUTING.md, "Releases".
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(dirname "$SCRIPT_DIR")"

VERSION="$(python3 scripts/bump_version.py current)"
TAG="v$VERSION"

if [[ "$VERSION" == "0.0.0" ]]; then
  echo "error: versions.env is 0.0.0 (unreleased); run the release-pr workflow first" >&2
  exit 1
fi
python3 scripts/bump_version.py check --expect "$VERSION"

branch="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$branch" != "main" ]]; then
  echo "error: on branch $branch; release tags are cut from main" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "error: the working tree is not clean" >&2
  exit 1
fi
git fetch --quiet origin main "refs/tags/*:refs/tags/*"
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "error: HEAD is not origin/main; pull first" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  echo "error: tag $TAG already exists" >&2
  exit 1
fi

git tag -a "$TAG" -m "superschematic $TAG"
git push origin "refs/tags/$TAG"
echo "pushed $TAG at $(git rev-parse --short HEAD); release.yml and go-module-tag.yml are now running:"
echo "  https://github.com/parable-work/superschematic/actions/workflows/release.yml"
echo "  https://github.com/parable-work/superschematic/actions/workflows/go-module-tag.yml"
