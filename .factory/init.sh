#!/bin/bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

go mod download

if [ -d "frontend" ] && [ -f "frontend/package.json" ]; then
  cd frontend
  pnpm install --frozen-lockfile 2>/dev/null || pnpm install
  cd ..
fi

mkdir -p /tmp/go-choir-m2/auth
mkdir -p /tmp/go-choir-m3/runtime
mkdir -p /tmp/go-choir-m3/dolt

AUTH_SIGNING_KEY_PATH="${CHOIR_AUTH_SIGNING_KEY_PATH:-/tmp/go-choir-m2/auth-signing-key}"
mkdir -p "$(dirname "$AUTH_SIGNING_KEY_PATH")"

if [ ! -f "$AUTH_SIGNING_KEY_PATH" ]; then
  ssh-keygen -q -t ed25519 -N "" -f "$AUTH_SIGNING_KEY_PATH" >/dev/null
fi

if [ ! -f "${AUTH_SIGNING_KEY_PATH}.pub" ]; then
  ssh-keygen -y -f "$AUTH_SIGNING_KEY_PATH" > "${AUTH_SIGNING_KEY_PATH}.pub"
fi

echo "init.sh complete"
