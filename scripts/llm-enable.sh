#!/usr/bin/env bash
# The local half of `task llm-enable` (no cluster access): finds the available
# remote sources and writes .k3d/llm/{sources,assistant-models.yaml,phraseforge-models.yaml}.
# Fails, writing nothing, when no source at all is available. With only the local
# Ollama available it removes any earlier generated files, so the apps get their
# committed default.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$root/.k3d/llm"

# The settings the generator needs (the status script loads them itself).
# shellcheck source=llm-env.sh
. "$root/scripts/llm-env.sh"

# Exits 1 (aborting here) when nothing is available; the table goes to stderr.
ids="$(bash "$root/scripts/llm-status.sh" --ids)"

if [ -z "$ids" ]; then
    echo "No remote source is available; the apps keep the committed local Ollama configuration." >&2
    rm -rf "$dir"
    exit 0
fi

tmp="$(mktemp -d "$root/.k3d/llm.new.XXXXXX" 2>/dev/null || { mkdir -p "$root/.k3d" && mktemp -d "$root/.k3d/llm.new.XXXXXX"; })"
trap 'rm -rf "$tmp"' EXIT
# shellcheck disable=SC2086 # ids are a fixed vocabulary, one per line
bash "$root/scripts/llm-generate.sh" "$tmp" $ids
printf '%s\n' "$ids" > "$tmp/sources"
rm -rf "$dir"
mv "$tmp" "$dir"
trap - EXIT
echo "Enabled: $(echo $ids | tr ' ' ',')"
