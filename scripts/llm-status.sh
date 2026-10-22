#!/usr/bin/env bash
# Prints which LLM sources the dev cluster's apps could use, in priority order:
#   1 lan-ollama   Ollama on the local network   (LLM_LAN_OLLAMA_URL)
#   2 openrouter   OpenRouter                    (OPENROUTER_API_KEY)
#   3 vertex       Vertex AI                     (LLM_VERTEX_ACCOUNT, LLM_VERTEX_PROJECT)
#   4 local-ollama the Ollama on this machine    (http://127.0.0.1:11434)
# Read-only. Exits 1 when no source is available. Secrets are never printed.
#
# With --ids the table goes to stderr and stdout gets only the ids of the
# available remote sources (lan-ollama, openrouter, vertex), one per line, for
# the llm-* tasks; the local Ollama is the apps' committed default, not an id.
#
# The settings come from .env only (see llm-env.sh); the shell's own variables of
# the same names are ignored. The LAN machine must have gemma4:31b pulled (the
# model llm-generate.sh configures; it may serve others too); when it also has
# bge-m3, the id lan-embed is printed too, so knowledge's embeddings can use that
# copy of the model as well. LLM_LOCAL_OLLAMA_URL is the address of the Ollama on
# this machine (default http://127.0.0.1:11434).
set -euo pipefail

# shellcheck source=llm-env.sh
. "$(dirname "${BASH_SOURCE[0]}")/llm-env.sh"

lan_model=gemma4:31b # keep in step with llm-generate.sh
lan_embed_models=(bge-m3 bge-m3:latest) # the embedding model of knowledge/models.dev.yaml, with Ollama's default tag
local_url="${LLM_LOCAL_OLLAMA_URL:-http://127.0.0.1:11434}"
available=0
ids=()
out=/dev/stdout
if [ "${1:-}" = "--ids" ]; then out=/dev/stderr; fi

report() { # priority id state reason
    printf '%s %-13s %-12s %s\n' "$1" "$2" "$3" "$4" > "$out"
    if [ "$3" = available ]; then
        available=$((available + 1))
        if [ "$2" != local-ollama ]; then ids+=("$2"); fi
    fi
}

# ollama_has_model URL NAME...: reachable and lists a model with one of the names.
ollama_has_model() {
    local url="$1"; shift
    curl -fsS --max-time 3 "$url/api/tags" 2>/dev/null \
        | jq -e --args '[.models[]?.name] as $have | any($ARGS.positional[]; . as $m | $have | index($m) != null)' "$@" >/dev/null 2>&1
}

# 1) LAN Ollama
if [ -z "${LLM_LAN_OLLAMA_URL:-}" ]; then
    report 1 lan-ollama unavailable "LLM_LAN_OLLAMA_URL is not set in .env"
elif ollama_has_model "$LLM_LAN_OLLAMA_URL" "$lan_model"; then
    report 1 lan-ollama available "$lan_model listed"
    # Not a source of its own (it needs lan-ollama): the same machine also serving
    # the embedding model knowledge uses.
    if ollama_has_model "$LLM_LAN_OLLAMA_URL" "${lan_embed_models[@]}"; then
        printf '%s %-13s %-12s %s\n' 1 lan-embed available "bge-m3 listed too (knowledge embeddings)" > "$out"
        ids+=(lan-embed)
    else
        printf '%s %-13s %-12s %s\n' 1 lan-embed unavailable "bge-m3 is not pulled there; knowledge embeddings stay on the local Ollama" > "$out"
    fi
else
    report 1 lan-ollama unavailable "not reachable within 3 s, or $lan_model is not pulled"
fi

# 2) OpenRouter: the key is set (no network call; the router's breaker handles a bad key)
if [ -n "${OPENROUTER_API_KEY:-}" ]; then
    report 2 openrouter available "OPENROUTER_API_KEY is set in .env"
else
    report 2 openrouter unavailable "OPENROUTER_API_KEY is not set in .env"
fi

# 3) Vertex AI: a token can be minted for the account and the project is named
if [ -z "${LLM_VERTEX_ACCOUNT:-}" ] || [ -z "${LLM_VERTEX_PROJECT:-}" ]; then
    report 3 vertex unavailable "LLM_VERTEX_ACCOUNT and LLM_VERTEX_PROJECT must both be set in .env"
elif timeout 20 gcloud auth print-access-token --account="$LLM_VERTEX_ACCOUNT" >/dev/null 2>&1; then
    report 3 vertex available "token minted for $LLM_VERTEX_ACCOUNT, project $LLM_VERTEX_PROJECT"
else
    report 3 vertex unavailable "gcloud cannot mint a token for $LLM_VERTEX_ACCOUNT (run gcloud auth login)"
fi

# 4) Ollama on this machine (what the in-cluster Service `ollama` forwards to)
if curl -fsS --max-time 3 "$local_url/api/tags" >/dev/null 2>&1; then
    report 4 local-ollama available "$local_url answers"
else
    report 4 local-ollama unavailable "$local_url does not answer within 3 s"
fi

if [ "$available" -eq 0 ]; then
    echo "No LLM source is available. Set LLM_LAN_OLLAMA_URL, OPENROUTER_API_KEY or LLM_VERTEX_ACCOUNT with LLM_VERTEX_PROJECT in .env (copy .env.example), or start Ollama on this machine." >&2
    exit 1
fi

if [ "${1:-}" = "--ids" ] && [ "${#ids[@]}" -gt 0 ]; then printf '%s\n' "${ids[@]}"; fi
