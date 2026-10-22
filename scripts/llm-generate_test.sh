#!/usr/bin/env bash
# Tests scripts/llm-generate.sh: the generated files must pass `models validate`
# for every combination of sources, keep the priority order, equal the committed
# files when no source is given, and never contain a secret.
# Run: bash scripts/llm-generate_test.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
(cd "$root/assistant" && go build -o "$work/assistant" .)

failures=0
fail() { echo "FAIL $1"; failures=$((failures + 1)); }
gen() { # NAME SOURCES...: generates into $work/NAME with a secret in the environment
    local name="$1"; shift
    OPENROUTER_API_KEY=sk-or-secret-value LLM_LAN_OLLAMA_URL=http://gpu.lan:11434 \
        LLM_VERTEX_PROJECT=my-proj bash "$root/scripts/llm-generate.sh" "$work/$name" "$@"
}
priorities() { awk '/^    priority:/ {print $2}' "$1" | paste -sd' ' -; }

check_combo() { # NAME EXPECTED_ASSISTANT_PRIORITIES EXPECTED_PHRASEFORGE_PRIORITIES EXPECTED_KNOWLEDGE_PRIORITIES SOURCES...
    local name="$1" want_a="$2" want_p="$3" want_k="$4"; shift 4
    gen "$name" "$@"
    for app in assistant phraseforge knowledge; do
        local file="$work/$name/$app-models.yaml"
        "$work/assistant" models validate "$file" >/dev/null 2>"$work/err" || { fail "$name $app: models validate: $(cat "$work/err")"; return; }
        grep -q 'sk-or-secret-value' "$file" && fail "$name $app: secret in file"
    done
    [ "$(priorities "$work/$name/assistant-models.yaml")" = "$want_a" ] || fail "$name assistant priorities: $(priorities "$work/$name/assistant-models.yaml"), want $want_a"
    [ "$(priorities "$work/$name/phraseforge-models.yaml")" = "$want_p" ] || fail "$name phraseforge priorities: $(priorities "$work/$name/phraseforge-models.yaml"), want $want_p"
    [ "$(priorities "$work/$name/knowledge-models.yaml")" = "$want_k" ] || fail "$name knowledge priorities: $(priorities "$work/$name/knowledge-models.yaml"), want $want_k"
    echo "ok   $name"
}

# Committed assistant models have priorities 10 10 1, phraseforge's 10, knowledge's 10 10 (a chat model and the embedding model).
check_combo all "100 90 80 10 10 1" "100 90 80 10" "100 90 80 10 10" lan-ollama openrouter vertex
check_combo none "10 10 1" "10" "10 10"
check_combo lan-only "100 10 10 1" "100 10" "100 10 10" lan-ollama
check_combo lan-with-embed "100 10 10 1" "100 10" "100 100 10 10" lan-ollama lan-embed
check_combo vertex-only "80 10 10 1" "80 10" "80 10 10" vertex
check_combo openrouter-only "90 10 10 1" "90 10" "90 10 10" openrouter

# No source: byte-identical to the committed files (what llm-disable restores).
cmp -s "$work/none/assistant-models.yaml" "$root/assistant/models.dev.yaml" || fail "none: assistant file differs from the committed one"
cmp -s "$work/none/phraseforge-models.yaml" "$root/phraseforge/models.dev.yaml" || fail "none: phraseforge file differs from the committed one"
cmp -s "$work/none/knowledge-models.yaml" "$root/knowledge/models.dev.yaml" || fail "none: knowledge file differs from the committed one"

# Tags: the assistant's LAN model also serves orchestrator and ui; remote models have no cheap.
grep -A4 'id: gemma4-31b-lan' "$work/all/assistant-models.yaml" | grep -q 'orchestrator, ui' || fail "assistant LAN model lacks orchestrator and ui"
grep -A4 'id: glm-flash' "$work/all/assistant-models.yaml" | grep -q 'cheap' && fail "assistant OpenRouter model has the cheap tag"
grep -A4 'id: gemini-flash-lite' "$work/all/assistant-models.yaml" | grep -q 'cheap' && fail "assistant Vertex model has the cheap tag"
grep -q 'api_key_from: file:/etc/assistant-vertex/token' "$work/all/assistant-models.yaml" || fail "assistant vertex token path"
grep -q 'api_key_from: file:/etc/phraseforge-vertex/token' "$work/all/phraseforge-models.yaml" || fail "phraseforge vertex token path"
# Knowledge: chat and default on every added model, and a context window that fits chat.
for id in gemma4-31b-lan glm-flash gemini-flash-lite; do
    grep -A4 "id: $id" "$work/all/knowledge-models.yaml" | grep -q 'tags: \[default, chat\]' || fail "knowledge $id lacks the default and chat tags"
done
grep -A8 'id: gemma4-31b-lan' "$work/all/knowledge-models.yaml" | grep -q 'num_ctx: 8192' || fail "knowledge LAN model num_ctx is not 8192"
grep -q 'api_key_from: file:/etc/knowledge-vertex/token' "$work/all/knowledge-models.yaml" || fail "knowledge vertex token path"
# The LAN embedding model is knowledge-only and in the same space and dimension as the committed one.
grep -q 'id: bge-m3-lan' "$work/lan-with-embed/knowledge-models.yaml" || fail "knowledge lacks the LAN embedding model"
grep -A8 'id: bge-m3-lan' "$work/lan-with-embed/knowledge-models.yaml" | grep -q 'embedding_space: bge-m3' || fail "LAN embedding model is not in the space bge-m3"
grep -A8 'id: bge-m3-lan' "$work/lan-with-embed/knowledge-models.yaml" | grep -q 'dimension: 1024' || fail "LAN embedding model dimension"
grep -q 'bge-m3-lan' "$work/lan-with-embed/assistant-models.yaml" "$work/lan-with-embed/phraseforge-models.yaml" && fail "the LAN embedding model leaked into another app"
grep -q 'bge-m3-lan' "$work/lan-only/knowledge-models.yaml" && fail "the LAN embedding model is there without lan-embed"
echo "ok   tags and token paths"

# Bad input is refused with a message and writes nothing.
refuse() { # NAME EXPECTED_TEXT ENV... -- SOURCES...
    local name="$1" text="$2"; shift 2
    local envs=(); while [ "$1" != "--" ]; do envs+=("$1"); shift; done; shift
    if env ${envs[@]+"${envs[@]}"} bash "$root/scripts/llm-generate.sh" "$work/bad-$name" "$@" >/dev/null 2>"$work/err"; then
        fail "$name: accepted"
    elif ! grep -q "$text" "$work/err"; then
        fail "$name: message lacks '$text': $(cat "$work/err")"
    elif [ -e "$work/bad-$name" ]; then
        fail "$name: wrote output"
    else
        echo "ok   refuses $name"
    fi
}
refuse lan-without-url "LLM_LAN_OLLAMA_URL" LLM_LAN_OLLAMA_URL= -- lan-ollama
refuse lan-bad-url "LLM_LAN_OLLAMA_URL" 'LLM_LAN_OLLAMA_URL=ftp://x"y' -- lan-ollama
refuse vertex-without-project "LLM_VERTEX_PROJECT" LLM_VERTEX_PROJECT= -- vertex
refuse unknown-source "unknown source" -- gpt
refuse embed-without-lan "lan-embed needs lan-ollama" LLM_LAN_OLLAMA_URL=http://x:1 -- lan-embed

if [ "$failures" -ne 0 ]; then echo "$failures failure(s)"; exit 1; fi
echo "all llm-generate tests passed"
