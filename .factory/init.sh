#!/bin/bash
set -e

# Ensure Go dependencies are up to date
cd "$(git rev-parse --show-toplevel)"

# Install frontend dependencies if frontend directory exists
if [ -d "frontend" ] && [ -f "frontend/package.json" ]; then
  cd frontend
  pnpm install --frozen-lockfile 2>/dev/null || pnpm install
  cd ..
fi

echo "init.sh complete"
