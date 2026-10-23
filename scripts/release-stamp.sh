#!/usr/bin/env bash
# Stamps <app>/CHANGELOG.md with the release version, commits it, tags that
# commit, and pushes both to origin's main. The release workflows call it after
# the tests passed and before the image is built.
#
# Usage: scripts/release-stamp.sh <app> <version>      (e.g. assistant 2026.10.4)
#   The tag is <app>-v<version>. Runs against whatever `origin` is, so it can be
#   tried against a local bare repo. It sets the bot identity in the repository's
#   local git config, as the workflows always did.
#
# Pushes made with GITHUB_TOKEN do not trigger new workflow runs, and "[skip ci]"
# in the commit message skips push-triggered runs for any other token: both are
# relied on so the changelog commit never starts another release.
#
# Other apps release concurrently and push to main too (they touch other files),
# so a rejected push is retried after `git pull --rebase`; the tag is created
# after the commit is on origin, so it names the commit as it exists there.
set -euo pipefail

app="${1:-}"
version="${2:-}"
if [ -z "$app" ] || [ -z "$version" ]; then
    echo "Usage: release-stamp.sh <app> <version>" >&2
    exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

tag="${app}-v${version}"
changelog="${app}/CHANGELOG.md"
max_attempts=3

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"

if git rev-parse --verify --quiet "refs/tags/${tag}" >/dev/null; then
    echo "release-stamp: tag ${tag} already exists locally; refusing to release it again" >&2
    exit 1
fi
if git ls-remote --exit-code --tags origin "refs/tags/${tag}" >/dev/null 2>&1; then
    echo "release-stamp: tag ${tag} already exists on origin; refusing to release it again" >&2
    exit 1
fi

node scripts/stamp-changelog.mjs "$changelog" "$version"
git add "$changelog"
git commit -m "Release ${tag} [skip ci]" -- "$changelog"

pushed=0
for attempt in $(seq 1 "$max_attempts"); do
    if git push origin HEAD:refs/heads/main; then
        pushed=1
        break
    fi
    echo "release-stamp: push rejected (attempt ${attempt}/${max_attempts}); rebasing on origin/main" >&2
    if ! git pull --rebase --autostash origin main; then
        git rebase --abort 2>/dev/null || true
        echo "release-stamp: rebase failed; the release commit is local only. Nothing was tagged or pushed." >&2
        exit 1
    fi
done
if [ "$pushed" -ne 1 ]; then
    echo "release-stamp: could not push the release commit after ${max_attempts} attempts. Nothing was tagged or pushed." >&2
    exit 1
fi

git tag -a "$tag" -m "$tag" HEAD
if ! git push origin "refs/tags/${tag}"; then
    cat >&2 <<MSG
release-stamp: the release commit is on origin/main but the tag push FAILED.
Left to do, from a clone of the repository:
  git tag -a ${tag} -m ${tag} $(git rev-parse HEAD)
  git push origin refs/tags/${tag}
The image for ${tag} must not be built until the tag exists.
MSG
    exit 1
fi
echo "release-stamp: released ${tag} at $(git rev-parse --short HEAD)"
