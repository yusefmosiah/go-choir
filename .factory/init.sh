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

if [ ! -f /tmp/go-choir-m2/auth-signing-key ]; then
  ssh-keygen -q -t ed25519 -N "" -f /tmp/go-choir-m2/auth-signing-key >/dev/null
fi

if [ ! -f /tmp/go-choir-m2/auth-signing-key.pub ]; then
  ssh-keygen -y -f /tmp/go-choir-m2/auth-signing-key > /tmp/go-choir-m2/auth-signing-key.pub
fi

echo "init.sh complete"
