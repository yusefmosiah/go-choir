---
name: infra-worker
description: Builds deploy, NixOS, Caddy, VM-image, and Node B operational configuration
---

# Infra Worker

NOTE: Startup and cleanup are handled by `worker-base`. This skill defines the WORK PROCEDURE.

## When to Use This Skill

Use this skill for:
- NixOS configuration updates for Node B
- Caddy configuration changes
- VM image (guest) updates
- CI/CD pipeline changes (GitHub Actions)
- Deployment credential management
- Provider API key deployment to Node B

## Required Skills

- **agent-browser** (for deployment verification): Use to verify deployed services work end-to-end after deployment.

## Work Procedure

### 1. Understand the Deployment Change
Read the feature. Identify:
- Which NixOS service configs need updates
- What environment variables/secrets need deployment
- What health checks verify success

### 2. Test Locally First
- Build Nix configuration: `nix build .#nixosConfigurations.node-b.config.system.build.toplevel`
- Check for syntax errors
- Verify secrets file structure (don't commit secrets)

### 3. Update Configuration
- Modify `nix/node-b.nix` for service changes
- Update `nix/sandbox-vm.nix` for guest image changes
- Update GitHub Actions workflow if CI changes needed
- Document any new secrets required

### 4. Deploy to Node B
- SSH to Node B (credentials in user's private notes)
- Git pull, nix build, nixos-rebuild switch
- Verify health endpoints
- Check service logs: `journalctl -u go-choir-auth -f`

### 5. Verify Deployment
- Test health endpoints: `curl https://draft.choir-ip.com/auth/health`
- Run smoke tests
- Invoke `agent-browser` to verify full flows work on deployed instance

### 6. Monitor for Issues
- Check logs for errors
- Verify all services running: `systemctl status go-choir-*`
- Rollback if critical issues

## Example Handoff

```json
{
  "salientSummary": "Deployed Fireworks and Z.AI API keys to Node B. Updated nix/node-b.nix to inject keys via environment variables. Verified both providers respond correctly through gateway. All health checks passing.",
  "whatWasImplemented": "Added FIREWORKS_API_KEY and ZAI_API_KEY to node-b.nix environment variables for gateway service. Keys sourced from /var/lib/go-choir/secrets/ directory. Updated deploy script to set proper permissions. Ran nixos-rebuild switch on Node B.",
  "whatWasLeftUndone": "",
  "verification": {
    "commandsRun": [
      {"command": "ssh root@147.135.70.196 'systemctl status go-choir-gateway'", "exitCode": 0, "observation": "Gateway service active and running"},
      {"command": "curl -s https://draft.choir-ip.com/provider/v1/health", "exitCode": 0, "observation": "Gateway health endpoint returns 200"},
      {"command": "ssh root@147.135.70.196 'journalctl -u go-choir-gateway -n 20'", "exitCode": 0, "observation": "No errors, keys loaded successfully"}
    ],
    "interactiveChecks": [
      {
        "action": "agent-browser: Navigate to draft.choir-ip.com, login, submit etext prompt with Fireworks model",
        "observed": "Streaming response received, content generated successfully"
      }
    ]
  },
  "tests": {
    "added": []
  },
  "discoveredIssues": [
    {
      "severity": "medium",
      "description": "Secret file permissions were 644, changed to 600",
      "suggestedFix": "Added chmod 600 to deploy script"
    }
  ]
}
```

## When to Return to Orchestrator

Return if:
- SSH credentials not available or don't work
- NixOS build fails with complex errors
- Service fails to start after deployment (needs investigation)
- User needs to provide secrets or approve credential changes
- Rollback needed due to deployment failure
