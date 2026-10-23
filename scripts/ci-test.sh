#!/usr/bin/env bash
# The single source of what CI tests per app. The release workflows call it and
# `task ci-local` runs it on a clean copy of the tree, so a local run and CI
# cannot drift apart.
#
# Usage: scripts/ci-test.sh <assistant|knowledge|phraseforge|dictionary>
#
# Apps with a Docker `ui` stage (assistant, knowledge, phraseforge) embed
# shared/ui/dist, a gitignored build output that a clean checkout lacks, so it
# is built first. They also run the shared module's tests. Tests that need a database or model skip without their env
# vars (ASSISTANT_TEST_DATABASE_URL and similar); no test calls a model.
set -euo pipefail

app="${1:-}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$app" in
    assistant | knowledge | phraseforge)
        echo "+ (cd shared/ui && npm ci && npm run build)"
        (cd "$root/shared/ui" && npm ci && npm run build)
        # shared/ is built into every one of these apps, so its own tests run with
        # theirs; the NATS tests skip without NATS_URL.
        echo "+ (cd shared && go vet ./... && go test -count=1 ./...)"
        (cd "$root/shared" && go vet ./... && go test -count=1 ./...)
        ;;
    dictionary) ;;
    *)
        echo "ci-test.sh: unknown app '${app}' (expected assistant, knowledge, phraseforge or dictionary)" >&2
        exit 2
        ;;
esac

echo "+ (cd ${app} && go vet ./... && go test -count=1 ./...)"
(cd "$root/$app" && go vet ./... && go test -count=1 ./...)
