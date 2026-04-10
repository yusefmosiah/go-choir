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

if [ ! -f /tmp/go-choir-m2/auth-jwt-ed25519 ]; then
  ssh-keygen -q -t ed25519 -N "" -f /tmp/go-choir-m2/auth-jwt-ed25519 >/dev/null
fi

if [ ! -f /tmp/go-choir-m2/auth-jwt-ed25519.pub ]; then
  ssh-keygen -y -f /tmp/go-choir-m2/auth-jwt-ed25519 > /tmp/go-choir-m2/auth-jwt-ed25519.pub
fi

echo "init.sh complete"
