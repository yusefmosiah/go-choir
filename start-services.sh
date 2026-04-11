#!/bin/bash
export AUTH_PORT=8081 AUTH_RP_ID="localhost" AUTH_RP_ORIGINS="http://localhost:4173" AUTH_ACCESS_TOKEN_TTL="5m" AUTH_REFRESH_TOKEN_TTL="720h" AUTH_COOKIE_SECURE="false"
go run ./cmd/auth > auth.log 2>&1 &
export SANDBOX_PORT=8085 SANDBOX_ID="sandbox-dev"
go run ./cmd/sandbox > sandbox.log 2>&1 &
sleep 5
curl -sf http://127.0.0.1:8081/health || { echo "auth failed"; exit 1; }
curl -sf http://127.0.0.1:8085/health || { echo "sandbox failed"; exit 1; }

export PROXY_PORT=8082 PROXY_SANDBOX_URL="http://127.0.0.1:8085"
go run ./cmd/proxy > proxy.log 2>&1 &
sleep 5
curl -sf http://127.0.0.1:8082/health || { echo "proxy failed"; exit 1; }

cd frontend && pnpm dev --host localhost --port 4173 > frontend.log 2>&1 &
sleep 10
curl -sf http://localhost:4173 || { echo "frontend failed"; exit 1; }
echo "Services started successfully"
