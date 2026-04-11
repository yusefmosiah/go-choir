# Provider Credential Injection on Node B

## How it works

Provider credentials (Z.AI, Fireworks, Bedrock) are injected into the `go-choir-gateway` systemd service on Node B via an **EnvironmentFile** at `/var/lib/go-choir/gateway-provider.env`. This file:

- Is **never committed to git** (listed in `.gitignore`)
- Is **never placed in the Nix store** (it's a writable runtime file)
- Is **created/updated** by running `./nix/deploy-provider-creds.sh`
- Is loaded by systemd with the `-` prefix (missing file is non-fatal)

## Deploy flow

1. The deploy helper `nix/deploy-provider-creds.sh` reads API keys from `~/.factory/settings.json` (customModels entries)
2. It detects provider type by apiKey prefix and baseUrl:
   - `z.ai` in baseUrl → ZAI_API_KEY
   - `fw_` prefix or `fireworks` in baseUrl → FIREWORKS_API_KEY
   - `bedrock` in baseUrl/provider → AWS_BEARER_TOKEN_BEDROCK
3. It writes the env file to Node B via SSH and restarts the gateway service
4. The gateway picks up credentials via `provider.ResolveProvider()` on restart

## NixOS configuration (node-b.nix)

```nix
systemd.services.go-choir-gateway = {
  serviceConfig = {
    EnvironmentFile = "-/var/lib/go-choir/gateway-provider.env";
    ReadWritePaths = [ "/var/lib/go-choir" ];
  };
};
```

## Provider resolution order

1. Bedrock (if `AWS_BEARER_TOKEN_BEDROCK` is set)
2. Z.AI (if `ZAI_API_KEY` is set)
3. Fireworks (if `FIREWORKS_API_KEY` is set)
4. Stub provider (no real credentials)

## Adding a new provider

1. Add the provider type to `internal/provider/provider.go`
2. Add env vars to `.factory/library/environment.md`
3. Update `ResolveProvider()` with the new fallback
4. Add the env var name to the vmmanager forbidden patterns test (VAL-VM-011)
5. Update `deploy-provider-creds.sh` to detect the new provider type

## Verification on Node B

After deploying credentials:
- `ssh node-b "systemctl status go-choir-gateway"` — check service is active
- `ssh node-b "curl -sf http://127.0.0.1:8084/health"` — should show provider name (not "none")
- `ssh node-b "systemctl show go-choir-gateway --property=Environment"` — should NOT contain plaintext credentials (they're in EnvironmentFile, not Environment)
