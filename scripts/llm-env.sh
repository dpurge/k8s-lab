#!/usr/bin/env bash
# Loads the LLM settings from the .env file next to scripts/ and from nowhere else.
#   - the llm-* scripts source it (bash);
#   - Taskfile steps, which run in Task's own shell, use
#       eval "$(bash scripts/llm-env.sh --export)"
#     which prints quoted `export NAME='value'` and `unset NAME` lines for
#     `eval` and writes nothing else.
#
# A variable of these names that is already in the shell is DISCARDED first, so
# a key exported in your shell for other work can never win over the one in
# .env. The file is parsed, never sourced, so it cannot run code. Rules: one
# NAME=value per line; blank lines and lines starting with # are ignored; no
# inline comments; one pair of surrounding quotes is removed; the last
# assignment wins; an empty value means not set; names not listed here are
# ignored. Values are never printed.

llm_env_names="LLM_LAN_OLLAMA_URL LLM_LOCAL_OLLAMA_URL OPENROUTER_API_KEY LLM_VERTEX_ACCOUNT LLM_VERTEX_PROJECT LLM_VERTEX_LOCATION"

llm_env_load() {
    local root file name line key value
    root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
    file="$root/.env"
    for name in $llm_env_names; do unset "$name"; done
    [ -f "$file" ] || return 0
    while IFS= read -r line || [ -n "$line" ]; do
        line="${line%$'\r'}"
        [[ "$line" =~ ^[[:space:]]*([A-Z][A-Z0-9_]*)=(.*)$ ]] || continue
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        case " $llm_env_names " in *" $key "*) ;; *) continue ;; esac
        # trim surrounding whitespace, then one pair of matching quotes
        value="${value#"${value%%[![:space:]]*}"}"
        value="${value%"${value##*[![:space:]]}"}"
        if [[ "$value" =~ ^\"(.*)\"$ ]] || [[ "$value" =~ ^\'(.*)\'$ ]]; then value="${BASH_REMATCH[1]}"; fi
        if [ -n "$value" ]; then export "$key=$value"; else unset "$key"; fi
    done < "$file"
}

llm_env_load

# Executed with --export (not sourced): print the settings for `eval`.
if [ "${BASH_SOURCE[0]}" = "$0" ] && [ "${1:-}" = "--export" ]; then
    for name in $llm_env_names; do
        if [ -n "${!name:-}" ]; then
            value="${!name}"
            # single-quote the value; an embedded ' becomes '\''
            sq="'"; escaped_sq="'\\''"
            escaped=${value//"$sq"/"$escaped_sq"}
            printf "export %s='%s'\n" "$name" "$escaped"
        else
            printf 'unset %s\n' "$name"
        fi
    done
fi
