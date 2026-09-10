#!/usr/bin/env bash
# Deploy provider credentials to Node B for the go-choir-gateway service.
#
# ChatGPT OAuth is refreshed locally, outside this script. The operator flow is:
#   1. Refresh/login with the local Codex/ChatGPT client.
#   2. Confirm the refreshed file is ~/.codex/auth.json, or set
#      CODEX_AUTH_PATH=/path/to/auth.json.
#   3. Run: ./nix/deploy-provider-creds.sh node-b
#   4. Verify the local and remote auth-file SHA-256 digests match, then
#      inspect the gateway journal for a successful provider=chatgpt call.
#
# This script copies the local OAuth JSON to
# /var/lib/go-choir/codex-auth.json, sets CHATGPT_AUTH_PATH in the gateway
# EnvironmentFile, and restarts only go-choir-gateway. It does not refresh the
# OAuth token itself. Never print or commit the auth JSON or its token values.
#
# The full helper also regenerates gateway-provider.env from .env and optional
# provider-settings.json. Use it only when that broader credential deployment
# is intended; the OAuth copy is not an isolated API.
#
# This script reads provider credentials from
# ${CHOIR_PROVIDER_ENV_FILE:-./.env}, plus optional custom model settings from
# ${CHOIR_PROVIDER_SETTINGS:-$HOME/.config/go-choir/provider-settings.json}, and
# writes them to /var/lib/go-choir/gateway-provider.env on Node B via SSH. The
# gateway systemd service loads this file via EnvironmentFile.
#
# IMPORTANT: This script never commits credentials to the repo or Nix store.
# The EnvironmentFile is a writable runtime location on Node B only.
#
# Usage:
#   ./nix/deploy-provider-creds.sh           # deploy to node-b (default)
#   CODEX_AUTH_PATH=~/.codex/auth.json ./nix/deploy-provider-creds.sh node-b
#
# Required: jq, ssh access to Node B
set -euo pipefail

HOST="${1:-node-b}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${CHOIR_PROVIDER_ENV_FILE:-${REPO_ROOT}/.env}"
SETTINGS="${CHOIR_PROVIDER_SETTINGS:-${HOME}/.config/go-choir/provider-settings.json}"
CODEX_AUTH="${CODEX_AUTH_PATH:-${HOME}/.codex/auth.json}"
REMOTE_ENV_FILE="/var/lib/go-choir/gateway-provider.env"
REMOTE_CODEX_AUTH="/var/lib/go-choir/codex-auth.json"
DEFAULT_GATEWAY_FIREWORKS_MODELS="accounts/fireworks/models/kimi-k2p6"
DEFAULT_GATEWAY_FIREWORKS_REASONING_EFFORT="medium"
DEFAULT_GATEWAY_DEEPSEEK_MODELS="deepseek-v4-flash,deepseek-v4-pro"
DEFAULT_GATEWAY_XIAOMI_MODELS="mimo-v2.5,mimo-v2.5-pro"
DEFAULT_GATEWAY_ZAI_MODELS="glm-5.2,glm-5.1,glm-5-turbo"
DEFAULT_ZAI_CODING_BASE_URL="https://api.z.ai/api/anthropic"

# Load local deployment credentials when present. This keeps the common
# operator path safe: running this script from the repo should deploy the
# private .env values without requiring every key to be exported first.
if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "warning: $ENV_FILE not found; relying on exported environment variables" >&2
fi

if [ -n "${xiaomi_api_key:-}" ] && [ -z "${XIAOMI_API_KEY:-}" ]; then
  XIAOMI_API_KEY="${xiaomi_api_key}"
fi

# Build the env file content from customModels entries.
# We detect the provider type by apiKey prefix and baseUrl.
ENVS=()

add_env_once() {
  local key="$1"
  local value="${!key:-}"
  if [ -z "$value" ]; then
    return
  fi
  if [ ${#ENVS[@]} -eq 0 ] || ! printf '%s\n' "${ENVS[@]}" | grep -q "^${key}="; then
    ENVS+=("${key}=${value}")
  fi
}

has_env_key() {
  local key="$1"
  [ ${#ENVS[@]} -gt 0 ] && printf '%s\n' "${ENVS[@]}" | grep -q "^${key}="
}

if [ -f "$SETTINGS" ]; then
  # Extract all customModels entries with their provider info
  NUM_MODELS=$(jq '.customModels | length' "$SETTINGS")

  for i in $(seq 0 $((NUM_MODELS - 1))); do
    API_KEY=$(jq -r ".customModels[$i].apiKey" "$SETTINGS")
    BASE_URL=$(jq -r ".customModels[$i].baseUrl // empty" "$SETTINGS")
    PROVIDER=$(jq -r ".customModels[$i].provider // empty" "$SETTINGS")
    MODEL=$(jq -r ".customModels[$i].model // empty" "$SETTINGS")

    if [ -z "$API_KEY" ]; then
      continue
    fi

    # Detect Z.AI keys (baseUrl contains z.ai)
    if echo "$BASE_URL" | grep -q "z\.ai"; then
      if ! printf '%s\n' "${ENVS[@]}" | grep -q "^ZAI_API_KEY="; then
        ENVS+=("ZAI_API_KEY=${API_KEY}")
        [ -n "$MODEL" ] && ENVS+=("RUNTIME_ZAI_MODEL=${MODEL}")
      fi
      continue
    fi

    # Detect Fireworks keys (apiKey starts with fw_ or baseUrl contains fireworks)
    if echo "$API_KEY" | grep -q "^fw_" || echo "$BASE_URL" | grep -q "fireworks"; then
      if ! printf '%s\n' "${ENVS[@]}" | grep -q "^FIREWORKS_API_KEY="; then
        ENVS+=("FIREWORKS_API_KEY=${API_KEY}")
        [ -n "$MODEL" ] && ENVS+=("RUNTIME_FIREWORKS_MODEL=${MODEL}")
        [ -n "$BASE_URL" ] && ENVS+=("FIREWORKS_BASE_URL=${BASE_URL}")
      fi
      continue
    fi

    # Detect DeepSeek keys (apiKey starts with sk- or baseUrl contains deepseek).
    # Provider settings can distinguish generic sk-* keys through baseUrl/provider.
    if echo "$BASE_URL" | grep -q "deepseek" || echo "$PROVIDER" | grep -qi "deepseek"; then
      if ! printf '%s\n' "${ENVS[@]}" | grep -q "^DEEPSEEK_API_KEY="; then
        ENVS+=("DEEPSEEK_API_KEY=${API_KEY}")
        [ -n "$MODEL" ] && ENVS+=("RUNTIME_DEEPSEEK_MODEL=${MODEL}")
        [ -n "$BASE_URL" ] && ENVS+=("DEEPSEEK_BASE_URL=${BASE_URL}")
      fi
      continue
    fi

    # Detect Xiaomi MiMo keys (baseUrl contains xiaomimimo or provider mentions xiaomi/mimo).
    if echo "$BASE_URL" | grep -q "xiaomimimo" || echo "$PROVIDER" | grep -Eqi "xiaomi|mimo"; then
      if ! printf '%s\n' "${ENVS[@]}" | grep -q "^XIAOMI_API_KEY="; then
        ENVS+=("XIAOMI_API_KEY=${API_KEY}")
        [ -n "$MODEL" ] && ENVS+=("RUNTIME_XIAOMI_MODEL=${MODEL}")
        [ -n "$BASE_URL" ] && ENVS+=("XIAOMI_BASE_URL=${BASE_URL}")
      fi
      continue
    fi

    # Detect Bedrock keys (baseUrl contains bedrock or provider mentions bedrock)
    if echo "$BASE_URL" | grep -q "bedrock" || echo "$PROVIDER" | grep -qi "bedrock"; then
      if ! printf '%s\n' "${ENVS[@]}" | grep -q "^AWS_BEARER_TOKEN_BEDROCK="; then
        ENVS+=("AWS_BEARER_TOKEN_BEDROCK=${API_KEY}")
        [ -n "$MODEL" ] && ENVS+=("RUNTIME_BEDROCK_MODEL=${MODEL}")
      fi
      continue
    fi
  done
else
  echo "warning: $SETTINGS not found; skipping API-key provider credentials" >&2
  echo "         set CHOIR_PROVIDER_SETTINGS to use another provider settings file" >&2
fi

if [ ${#ENVS[@]} -eq 0 ]; then
  echo "warning: no API-key provider credentials found in $SETTINGS" >&2
fi

for key in AWS_BEARER_TOKEN_BEDROCK AWS_REGION ZAI_API_KEY ZAI_BASE_URL DEEPSEEK_API_KEY DEEPSEEK_BASE_URL XIAOMI_API_KEY XIAOMI_BASE_URL FIREWORKS_API_KEY FIREWORKS_BASE_URL; do
  add_env_once "$key"
done

if [ -n "${ZAI_API_KEY:-}" ] || has_env_key "ZAI_API_KEY"; then
  ENVS+=("GATEWAY_ZAI_MODELS=${GATEWAY_ZAI_MODELS:-$DEFAULT_GATEWAY_ZAI_MODELS}")
  if ! has_env_key "ZAI_BASE_URL"; then
    ENVS+=("ZAI_BASE_URL=${DEFAULT_ZAI_CODING_BASE_URL}")
  fi
fi

if [ -n "${DEEPSEEK_API_KEY:-}" ] || has_env_key "DEEPSEEK_API_KEY"; then
  ENVS+=("GATEWAY_DEEPSEEK_MODELS=${GATEWAY_DEEPSEEK_MODELS:-$DEFAULT_GATEWAY_DEEPSEEK_MODELS}")
  if [ -n "${GATEWAY_DEEPSEEK_REASONING_EFFORT:-}" ]; then
    ENVS+=("GATEWAY_DEEPSEEK_REASONING_EFFORT=${GATEWAY_DEEPSEEK_REASONING_EFFORT}")
  fi
fi

if [ -n "${XIAOMI_API_KEY:-}" ] || has_env_key "XIAOMI_API_KEY"; then
  ENVS+=("GATEWAY_XIAOMI_MODELS=${GATEWAY_XIAOMI_MODELS:-$DEFAULT_GATEWAY_XIAOMI_MODELS}")
  if [ -n "${GATEWAY_XIAOMI_REASONING_EFFORT:-}" ]; then
    ENVS+=("GATEWAY_XIAOMI_REASONING_EFFORT=${GATEWAY_XIAOMI_REASONING_EFFORT}")
  fi
fi

if [ -n "${FIREWORKS_API_KEY:-}" ]; then
  ENVS+=("GATEWAY_FIREWORKS_MODELS=${GATEWAY_FIREWORKS_MODELS:-$DEFAULT_GATEWAY_FIREWORKS_MODELS}")
  ENVS+=("GATEWAY_FIREWORKS_REASONING_EFFORT=${GATEWAY_FIREWORKS_REASONING_EFFORT:-$DEFAULT_GATEWAY_FIREWORKS_REASONING_EFFORT}")
fi

if [ -f "$CODEX_AUTH" ]; then
  ENVS+=("CHATGPT_AUTH_PATH=${REMOTE_CODEX_AUTH}")
  ENVS+=("GATEWAY_CHATGPT_MODELS=gpt-5.5,gpt-5.4,gpt-5.4-mini")
  ENVS+=("GATEWAY_CHATGPT_REASONING_EFFORT=low")
  ENVS+=("CHATGPT_SEARCH_MODEL=${CHATGPT_SEARCH_MODEL:-gpt-5.5}")
  ENVS+=("CHATGPT_SEARCH_REASONING_EFFORT=${CHATGPT_SEARCH_REASONING_EFFORT:-low}")
  ENVS+=("CHATGPT_SEARCH_CONTEXT_SIZE=${CHATGPT_SEARCH_CONTEXT_SIZE:-low}")
else
  echo "warning: $CODEX_AUTH not found; ChatGPT provider auth will not be deployed" >&2
fi

for key in TAVILY_API_KEY BRAVE_API_KEY EXA_API_KEY SERPER_API_KEY PARALLEL_API_KEY SERPAPI_API_KEY; do
  add_env_once "$key"
done

if [ ${#ENVS[@]} -eq 0 ]; then
  echo "error: no provider credentials found" >&2
  exit 1
fi

# Build the env file content
ENV_CONTENT=$(printf '%s\n' "${ENVS[@]}")
ENV_CONTENT="${ENV_CONTENT}
# Auto-generated by deploy-provider-creds.sh — DO NOT COMMIT
# Regenerate with: ./nix/deploy-provider-creds.sh"

echo "Deploying ${#ENVS[@]} credential entries to ${HOST}:${REMOTE_ENV_FILE}"

# Copy Codex OAuth auth when present. This uses scp rather than embedding the
# token JSON in a shell heredoc so the auth content does not appear in logs or
# process arguments.
if [ -f "$CODEX_AUTH" ]; then
  tmp_remote="${REMOTE_CODEX_AUTH}.tmp"
  scp -q "$CODEX_AUTH" "${HOST}:${tmp_remote}"
  ssh "$HOST" "mv ${tmp_remote} ${REMOTE_CODEX_AUTH} && chmod 600 ${REMOTE_CODEX_AUTH}"
fi

# Write the env file on Node B and restart the gateway service.
# Use a temp file and atomic move to avoid partial writes.
ssh "$HOST" "cat > ${REMOTE_ENV_FILE}.tmp << 'PROVIDER_ENV_EOF'
${ENV_CONTENT}
PROVIDER_ENV_EOF
mv ${REMOTE_ENV_FILE}.tmp ${REMOTE_ENV_FILE}
chmod 600 ${REMOTE_ENV_FILE}
systemctl restart go-choir-gateway
echo 'Provider credentials deployed and gateway restarted.'
systemctl is-active go-choir-gateway"

echo "Done."
