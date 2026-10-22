#!/usr/bin/env bash
# Tests scripts/llm-status.sh and the .env loader (scripts/llm-env.sh) against a
# fake Ollama server and a fake gcloud, in a temporary copy of scripts/ with its
# own .env. No real LLM, cloud or cluster is touched. Run: bash scripts/llm-status_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
server_pid=""
cleanup() {
    if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
    rm -rf "$work"
}
trap cleanup EXIT

# The scripts find .env next to their own directory, so test in a copy.
mkdir -p "$work/root/scripts"
cp "$here"/llm-status.sh "$here"/llm-env.sh "$work/root/scripts/"

# Fake Ollama: answers GET /api/tags with the model list in $work/tags.json.
cat > "$work/server.py" <<'PY'
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/api/tags":
            self.send_response(404); self.end_headers(); return
        body = open(sys.argv[1], "rb").read()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
print(s.server_address[1], flush=True)
s.serve_forever()
PY
echo '{"models":[{"name":"gemma4:31b"},{"name":"bge-m3:latest"}]}' > "$work/tags.json"
python3 -I "$work/server.py" "$work/tags.json" > "$work/port" &
server_pid=$!
for _ in $(seq 1 50); do [ -s "$work/port" ] && break; sleep 0.1; done
fake="http://127.0.0.1:$(cat "$work/port")"
dead="http://127.0.0.1:1" # nothing listens

# Fake gcloud: succeeds for good@example.com and prints a token that must never reach the output.
mkdir "$work/bin"
cat > "$work/bin/gcloud" <<'SH'
#!/bin/sh
case "$*" in *--account=good@example.com*) echo "SECRET-TOKEN-VALUE"; exit 0 ;; esac
exit 1
SH
chmod +x "$work/bin/gcloud"

failures=0
# run NAME EXPECTED_EXIT ENV_FILE_TEXT [SHELL_VAR=VALUE...]: the text becomes .env;
# the shell variables are set in the environment of the script (and must not count).
run() {
    local name="$1" want="$2" text="$3"; shift 3
    local out status=0
    printf '%s' "$text" > "$work/root/.env"
    out="$(env -i PATH="$work/bin:$PATH" HOME="$work" "$@" bash "$work/root/scripts/llm-status.sh" 2>&1)" || status=$?
    RUN_OUT="$out"
    if [ "$status" -ne "$want" ]; then
        echo "FAIL $name: exit $status, want $want"; echo "$out"; failures=$((failures + 1)); return
    fi
    if grep -q 'SECRET-TOKEN-VALUE\|sk-or-secret' <<<"$out"; then
        echo "FAIL $name: a secret was printed"; failures=$((failures + 1)); return
    fi
    echo "ok   $name"
}
# expect LINE_PREFIX STATE: the output has that source line in that state.
expect() {
    if ! grep -Eq "^$1 +$2" <<<"$RUN_OUT"; then
        echo "FAIL   expected '$1' $2 in:"; echo "$RUN_OUT"; failures=$((failures + 1))
    fi
}
nl=$'\n'

run "all available" 0 "LLM_LAN_OLLAMA_URL=$fake${nl}OPENROUTER_API_KEY=sk-or-secret${nl}LLM_VERTEX_ACCOUNT=good@example.com${nl}LLM_VERTEX_PROJECT=p${nl}LLM_LOCAL_OLLAMA_URL=$fake${nl}"
expect '1 lan-ollama' available; expect '2 openrouter' available
expect '3 vertex' available; expect '4 local-ollama' available

run "none available" 1 "LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '1 lan-ollama' unavailable; expect '2 openrouter' unavailable
expect '3 vertex' unavailable; expect '4 local-ollama' unavailable
grep -q 'No LLM source is available' <<<"$RUN_OUT" || { echo "FAIL none: no actionable message"; failures=$((failures + 1)); }

# No .env file: every setting is "not set" (the exit code depends on whether this machine runs Ollama).
rm -f "$work/root/.env"
out="$(env -i PATH="$PATH" HOME="$work" bash "$work/root/scripts/llm-status.sh" 2>&1 || true)"
grep -q 'not set in .env' <<<"$out" || { echo "FAIL missing .env: no message"; failures=$((failures + 1)); }

run "LAN only" 0 "LLM_LAN_OLLAMA_URL=$fake${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '1 lan-ollama' available; expect '4 local-ollama' unavailable

echo '{"models":[{"name":"bge-m3:latest"}]}' > "$work/tags.json" # the fake serves this file on every request
run "LAN up but gemma4:31b is not pulled" 1 "LLM_LAN_OLLAMA_URL=$fake${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '1 lan-ollama' unavailable
echo '{"models":[{"name":"gemma4:31b"},{"name":"bge-m3:latest"}]}' > "$work/tags.json"

run "LAN unreachable" 1 "LLM_LAN_OLLAMA_URL=$dead${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '1 lan-ollama' unavailable

run "OpenRouter only" 0 "OPENROUTER_API_KEY=sk-or-secret${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '2 openrouter' available

run "Vertex only" 0 "LLM_VERTEX_ACCOUNT=good@example.com${nl}LLM_VERTEX_PROJECT=p${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '3 vertex' available

run "Vertex without project" 1 "LLM_VERTEX_ACCOUNT=good@example.com${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '3 vertex' unavailable

run "Vertex bad account" 1 "LLM_VERTEX_ACCOUNT=bad@example.com${nl}LLM_VERTEX_PROJECT=p${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '3 vertex' unavailable

run "local only" 0 "LLM_LOCAL_OLLAMA_URL=$fake${nl}"
expect '4 local-ollama' available

# --- the .env is the only source of these settings ---
run "shell variables are ignored" 1 "LLM_LOCAL_OLLAMA_URL=$dead${nl}" \
    OPENROUTER_API_KEY=sk-or-secret LLM_LAN_OLLAMA_URL="$fake" LLM_VERTEX_ACCOUNT=good@example.com LLM_VERTEX_PROJECT=p
expect '1 lan-ollama' unavailable; expect '2 openrouter' unavailable; expect '3 vertex' unavailable

run ".env wins over a different shell value" 0 "LLM_LAN_OLLAMA_URL=$fake${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}" LLM_LAN_OLLAMA_URL="$dead"
expect '1 lan-ollama' available

run "a commented-out line is off" 1 "# OPENROUTER_API_KEY=sk-or-secret${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '2 openrouter' unavailable

run "an empty value is off" 1 "OPENROUTER_API_KEY=${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '2 openrouter' unavailable

run "quotes, spaces, CRLF and last assignment" 0 "OPENROUTER_API_KEY=old${nl}  OPENROUTER_API_KEY = \"sk-or-secret\"${nl}LLM_LOCAL_OLLAMA_URL=\"$dead\"${nl}OPENROUTER_API_KEY=\"sk-or-secret\"$'\r'${nl}"
expect '2 openrouter' available

run "unlisted names are ignored" 1 "PATH=/nonexistent${nl}FOO=bar${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
expect '4 local-ollama' unavailable

# The file is parsed, never executed.
rm -f "$work/ran"
run "the file is not executed" 0 "\$(touch $work/ran)${nl}OPENROUTER_API_KEY=\$(touch $work/ran)${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}"
[ ! -e "$work/ran" ] || { echo "FAIL .env content was executed"; failures=$((failures + 1)); }

# --export (used by Taskfile steps, which run in Task's own shell, not bash): the
# lines survive `eval` in a plain POSIX shell with awkward values, and a shell
# value of a setting that is absent from .env is unset.
printf '%s' "OPENROUTER_API_KEY=\"a'b \$c \`d\` e\"${nl}LLM_VERTEX_PROJECT=p${nl}" > "$work/root/.env"
got="$(env -i PATH="$PATH" HOME="$work" LLM_LAN_OLLAMA_URL=from-shell sh -c 'eval "$(bash "$1" --export)"; printf "%s|%s|%s" "$OPENROUTER_API_KEY" "$LLM_VERTEX_PROJECT" "${LLM_LAN_OLLAMA_URL-unset}"' sh "$work/root/scripts/llm-env.sh")"
[ "$got" = "a'b \$c \`d\` e|p|unset" ] || { echo "FAIL --export: '$got'"; failures=$((failures + 1)); }
echo "ok   --export through eval in sh"

# lan-embed: the LAN machine that also lists bge-m3 (with or without the tag) is reported and its id printed;
# one without it is not, and says so.
printf '%s' "LLM_LAN_OLLAMA_URL=$fake${nl}LLM_LOCAL_OLLAMA_URL=$dead${nl}" > "$work/root/.env"
ids="$(env -i PATH="$work/bin:$PATH" HOME="$work" bash "$work/root/scripts/llm-status.sh" --ids 2>/dev/null)"
[ "$ids" = "$(printf 'lan-ollama\nlan-embed')" ] || { echo "FAIL lan-embed ids with bge-m3:latest: '$ids'"; failures=$((failures + 1)); }
echo '{"models":[{"name":"gemma4:31b"},{"name":"bge-m3"}]}' > "$work/tags.json"
ids="$(env -i PATH="$work/bin:$PATH" HOME="$work" bash "$work/root/scripts/llm-status.sh" --ids 2>/dev/null)"
[ "$ids" = "$(printf 'lan-ollama\nlan-embed')" ] || { echo "FAIL lan-embed ids with bge-m3 untagged: '$ids'"; failures=$((failures + 1)); }
echo '{"models":[{"name":"gemma4:31b"}]}' > "$work/tags.json"
out="$(env -i PATH="$work/bin:$PATH" HOME="$work" bash "$work/root/scripts/llm-status.sh" 2>&1)"
ids="$(env -i PATH="$work/bin:$PATH" HOME="$work" bash "$work/root/scripts/llm-status.sh" --ids 2>/dev/null)"
[ "$ids" = "lan-ollama" ] && grep -Eq '^1 lan-embed +unavailable' <<<"$out" || { echo "FAIL lan-embed without bge-m3: ids '$ids'"; failures=$((failures + 1)); }
echo '{"models":[{"name":"gemma4:31b"},{"name":"bge-m3:latest"}]}' > "$work/tags.json"
echo "ok   lan-embed"

# --ids prints only the available remote sources on stdout.
printf '%s' "LLM_LAN_OLLAMA_URL=$fake${nl}OPENROUTER_API_KEY=sk-or-secret${nl}LLM_LOCAL_OLLAMA_URL=$fake${nl}" > "$work/root/.env"
ids="$(env -i PATH="$work/bin:$PATH" HOME="$work" bash "$work/root/scripts/llm-status.sh" --ids 2>/dev/null)"
[ "$ids" = "$(printf 'lan-ollama\nlan-embed\nopenrouter')" ] || { echo "FAIL --ids: '$ids'"; failures=$((failures + 1)); }
echo "ok   --ids"

if [ "$failures" -ne 0 ]; then echo "$failures failure(s)"; exit 1; fi
echo "all llm-status tests passed"
