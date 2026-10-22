#!/usr/bin/env bash
# Writes each app's (assistant, phraseforge, knowledge) models.yaml with the given
# remote sources added to the committed dev file, so the router tries them first (highest priority wins):
#   lan-ollama 100, openrouter 90, vertex 80; the committed local Ollama is 10.
#
# usage: llm-generate.sh OUTDIR [lan-ollama] [lan-embed] [openrouter] [vertex]
#   writes OUTDIR/assistant-models.yaml, OUTDIR/phraseforge-models.yaml and
#   OUTDIR/knowledge-models.yaml
#   (with no source: copies of the committed files)
# lan-embed (it needs lan-ollama) adds, to knowledge only, bge-m3 on the LAN machine
# as a second copy of the embedding model, in the same embedding space as the
# committed one.
# Reads LLM_LAN_OLLAMA_URL, LLM_VERTEX_PROJECT and LLM_VERTEX_LOCATION (default
# us-central1). The files hold no secret: only env:/file: references. Check them
# with `go run . models validate <file>` in assistant/ (phraseforge shares the schema).
set -euo pipefail

if [ "$#" -lt 1 ]; then echo "usage: $0 OUTDIR [lan-ollama] [openrouter] [vertex]" >&2; exit 2; fi
root="$(cd "$(dirname "$0")/.." && pwd)"
outdir="$1"; shift
sources=("$@")

has() { local s; for s in ${sources[@]+"${sources[@]}"}; do [ "$s" = "$1" ] && return 0; done; return 1; }
for s in ${sources[@]+"${sources[@]}"}; do
    case "$s" in lan-ollama | lan-embed | openrouter | vertex) ;; *) echo "unknown source '$s'" >&2; exit 2 ;; esac
done
if has lan-embed && ! has lan-ollama; then
    echo "the source lan-embed needs lan-ollama (the provider of the LAN machine)" >&2; exit 2
fi

lan_url="${LLM_LAN_OLLAMA_URL:-}"
vertex_project="${LLM_VERTEX_PROJECT:-}"
vertex_location="${LLM_VERTEX_LOCATION:-us-central1}"
if has lan-ollama && ! [[ "$lan_url" =~ ^https?://[^[:space:]\"]+$ ]]; then
    echo "LLM_LAN_OLLAMA_URL must be an http(s) address such as http://gpu-host.lan:11434" >&2; exit 2
fi
if has vertex && { ! [[ "$vertex_project" =~ ^[A-Za-z0-9._:-]+$ ]] || ! [[ "$vertex_location" =~ ^[a-z][a-z0-9-]*$ ]]; }; then
    echo "LLM_VERTEX_PROJECT and LLM_VERTEX_LOCATION must be plain names (letters, digits, - . : _)" >&2; exit 2
fi

# providers_for APP: the provider entries to add.
providers_for() {
    if has lan-ollama; then
        cat <<YAML
  - id: ollama-lan
    type: ollama
    base_url: "$lan_url"
    api_key_from: ""
YAML
    fi
    if has openrouter; then
        cat <<'YAML'
  - id: openrouter
    type: openai
    base_url: https://openrouter.ai/api/v1
    api_key_from: env:OPENROUTER_API_KEY
YAML
    fi
    if has vertex; then
        cat <<YAML
  - id: vertex
    type: vertex
    project: "$vertex_project"
    location: $vertex_location
    api_key_from: file:/etc/$1-vertex/token
YAML
    fi
}

# models_for APP: the model entries to add. The assistant's workflows ask for
# chat, review, orchestrator and ui; phraseforge's purposes ask for default;
# knowledge's ask for chat (chat) and default (title, summary, translate).
models_for() {
    local app="$1" lan_tags="[default]" remote_tags="[default]" lan_options
    if [ "$app" = assistant ]; then
        lan_tags="[default, chat, review, cheap, orchestrator, ui]"
        remote_tags="[default, chat, review]"
    fi
    # num_ctx must stay at or above what phraseforge's purposes ask for (the
    # committed local model has 16384); num_predict suits the assistant's ui tag.
    lan_options="      num_ctx: 16384
      num_predict: 2048
      think: false
      first_token_timeout_seconds: 120
      idle_timeout_seconds: 60
      overall_timeout_seconds: 600"
    if [ "$app" = knowledge ]; then
        lan_tags="[default, chat]"
        remote_tags="[default, chat]"
        # chat sizes its input to an 8192-token window: num_ctx must be at least that.
        lan_options="      num_ctx: 8192
      think: false
      first_token_timeout_seconds: 120
      idle_timeout_seconds: 60"
    fi
    if [ "$app" = phraseforge ]; then
        lan_options="      num_ctx: 16384
      think: false
      first_token_timeout_seconds: 120
      idle_timeout_seconds: 60"
    fi
    if has lan-ollama; then
        printf '  - id: gemma4-31b-lan\n    provider: ollama-lan\n    model: gemma4:31b\n    tags: %s\n    priority: 100\n    options:\n%s\n' "$lan_tags" "$lan_options"
    fi
    if [ "$app" = knowledge ] && has lan-embed; then
        # The same embedding space and dimension as knowledge/models.dev.yaml and
        # config.yaml: only copies of one model may stand in for each other.
        printf '  - id: bge-m3-lan\n    provider: ollama-lan\n    model: bge-m3\n    tags: [embed]\n    priority: 100\n    embedding_space: bge-m3\n    dimension: 1024\n'
    fi
    if has openrouter; then
        printf '  - id: glm-flash\n    provider: openrouter\n    model: z-ai/glm-5.3-flash\n    tags: %s\n    priority: 90\n    options:\n      overall_timeout_seconds: 120\n' "$remote_tags"
    fi
    if has vertex; then
        printf '  - id: gemini-flash-lite\n    provider: vertex\n    model: gemini-2.5-flash-lite\n    tags: %s\n    priority: 80\n    options:\n      overall_timeout_seconds: 120\n' "$remote_tags"
    fi
}

# generate APP BASE OUT: BASE with the fragments inserted right after its two
# top-level `providers:` and `models:` lines.
generate() {
    local app="$1" base="$2" out="$3" tmp
    for anchor in providers models; do
        if [ "$(grep -c "^$anchor:\$" "$base")" -ne 1 ]; then
            echo "$base must have exactly one top-level '$anchor:' line" >&2; exit 1
        fi
    done
    tmp="$(mktemp -d)"
    providers_for "$app" > "$tmp/providers"
    models_for "$app" > "$tmp/models"
    awk -v pf="$tmp/providers" -v mf="$tmp/models" '
        function dump(f,   l) { while ((getline l < f) > 0) print l; close(f) }
        { print }
        /^providers:$/ { dump(pf) }
        /^models:$/ { dump(mf) }' "$base" > "$out"
    rm -rf "$tmp"
}

mkdir -p "$outdir"
generate assistant "$root/assistant/models.dev.yaml" "$outdir/assistant-models.yaml"
generate phraseforge "$root/phraseforge/models.dev.yaml" "$outdir/phraseforge-models.yaml"
generate knowledge "$root/knowledge/models.dev.yaml" "$outdir/knowledge-models.yaml"
