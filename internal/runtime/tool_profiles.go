package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/search"
	"github.com/yusefmosiah/go-choir/internal/types"
)

const (
	AgentProfileConductor  = "conductor"
	AgentProfileSuper      = "super"
	AgentProfileResearcher = "researcher"
	AgentProfileVText      = "vtext"
)

const (
	taskMetadataAgentProfile = "agent_profile"
	taskMetadataWorkID       = "work_id"
	taskMetadataAgentRole    = "agent_role"
	taskMetadataAgentID      = "agent_id"
	taskMetadataModel        = "model"
)

type toolContextKey string

const (
	toolCtxTaskID    toolContextKey = "task_id"
	toolCtxOwnerID   toolContextKey = "owner_id"
	toolCtxProfile   toolContextKey = "agent_profile"
	toolCtxRole      toolContextKey = "agent_role"
	toolCtxWorkID    toolContextKey = "work_id"
	toolCtxSandboxID toolContextKey = "sandbox_id"
)

func WithToolExecutionContext(ctx context.Context, rec *types.TaskRecord) context.Context {
	ctx = context.WithValue(ctx, toolCtxTaskID, rec.TaskID)
	ctx = context.WithValue(ctx, toolCtxOwnerID, rec.OwnerID)
	ctx = context.WithValue(ctx, toolCtxProfile, agentProfileForTask(rec))
	ctx = context.WithValue(ctx, toolCtxRole, agentRoleForTask(rec))
	ctx = context.WithValue(ctx, toolCtxWorkID, workIDForTask(rec))
	ctx = context.WithValue(ctx, toolCtxSandboxID, rec.SandboxID)
	return ctx
}

func stringFromToolContext(ctx context.Context, key toolContextKey) string {
	value, _ := ctx.Value(key).(string)
	return strings.TrimSpace(value)
}

func agentProfileForTask(rec *types.TaskRecord) string {
	if rec == nil {
		return AgentProfileSuper
	}
	if rec.Metadata != nil {
		if profile, _ := rec.Metadata[taskMetadataAgentProfile].(string); strings.TrimSpace(profile) != "" {
			return strings.TrimSpace(profile)
		}
	}
	if taskType, _ := rec.Metadata["type"].(string); taskType == "vtext_agent_revision" {
		return AgentProfileVText
	}
	return AgentProfileSuper
}

func agentRoleForTask(rec *types.TaskRecord) string {
	if rec == nil {
		return AgentProfileSuper
	}
	if rec.Metadata != nil {
		if role, _ := rec.Metadata[taskMetadataAgentRole].(string); strings.TrimSpace(role) != "" {
			return strings.TrimSpace(role)
		}
	}
	return agentProfileForTask(rec)
}

func workIDForTask(rec *types.TaskRecord) string {
	if rec == nil {
		return ""
	}
	if rec.Metadata != nil {
		if workID, _ := rec.Metadata[taskMetadataWorkID].(string); strings.TrimSpace(workID) != "" {
			return strings.TrimSpace(workID)
		}
	}
	return strings.TrimSpace(rec.TaskID)
}

func systemPromptForTask(rec *types.TaskRecord) string {
	profile := agentProfileForTask(rec)
	workID := workIDForTask(rec)

	base := "You are a helpful assistant running inside the ChoirOS sandbox runtime."
	switch profile {
	case AgentProfileConductor:
		base = "You are the ChoirOS conductor agent. You receive incoming user or connector requests, decide which appagent should own the work, and coordinate handoffs over shared channels. Spawn appagents like vtext rather than trying to do their job yourself."
	case AgentProfileResearcher:
		base = "You are a ChoirOS researcher agent. Gather evidence, inspect local files, use web tools for external information, and communicate findings over channels. Do not use shell execution."
	case AgentProfileVText:
		base = "You are the ChoirOS vtext agent. You own the canonical document state, coordinate workers over channels, and rewrite document versions from messages and user edits. Do not perform shell execution or direct research yourself when a worker can do it."
	case AgentProfileSuper:
		base = "You are the ChoirOS super agent. You have the full tool surface for local execution, research, and coagent coordination. Delegate aggressively when other agents are a better fit."
	}

	var b strings.Builder
	b.WriteString(base)
	if profile == AgentProfileConductor {
		requestedApp, _ := rec.Metadata["requested_app"].(string)
		seedPrompt, _ := rec.Metadata["seed_prompt"].(string)
		if requestedApp == "" {
			requestedApp = AgentProfileVText
		}
		b.WriteString("\n\nReturn only a single JSON object with one of these shapes:")
		b.WriteString(` {"action":"open_app","app":"vtext","title":"...","seed_prompt":"...","initial_content":"...","create_initial_version":true}`)
		b.WriteString(` or {"action":"toast","message":"..."}.`)
		b.WriteString("\nDefault to opening vtext unless there is a strong reason to do otherwise.")
		if requestedApp != "" {
			b.WriteString("\nRequested default app: ")
			b.WriteString(requestedApp)
			b.WriteString(".")
		}
		if strings.TrimSpace(seedPrompt) != "" {
			b.WriteString("\nSeed prompt: ")
			b.WriteString(strings.TrimSpace(seedPrompt))
			b.WriteString(".")
		}
	}
	if workID != "" {
		b.WriteString("\n\nCurrent shared work channel: ")
		b.WriteString(workID)
		b.WriteString(".")
	}
	b.WriteString("\nUse shared work channels to coordinate with peer agents and keep messages concise and actionable.")
	return b.String()
}

// WithToolProfileRegistry registers a profile-specific tool registry on the runtime.
func WithToolProfileRegistry(profile string, registry *ToolRegistry) RuntimeOption {
	return func(rt *Runtime) {
		if strings.TrimSpace(profile) == "" || registry == nil {
			return
		}
		if rt.toolProfiles == nil {
			rt.toolProfiles = make(map[string]*ToolRegistry)
		}
		rt.toolProfiles[strings.TrimSpace(profile)] = registry
	}
}

// InstallDefaultAgentTools installs the default profile registries used by the
// local MAS. Super gets the full tool surface; researcher gets research, file,
// and coagent tools; conductor and vtext get coagent tools only.
func (rt *Runtime) InstallDefaultAgentTools(cwd string) error {
	if strings.TrimSpace(cwd) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve tool cwd: %w", err)
		}
		cwd = wd
	}

	searchClient := search.NewSearchClient()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	superRegistry := MustNewToolRegistry()
	if err := RegisterFileTools(superRegistry, cwd); err != nil {
		return err
	}
	if err := RegisterCodingTools(superRegistry, cwd); err != nil {
		return err
	}
	if err := RegisterResearchTools(superRegistry, searchClient, httpClient); err != nil {
		return err
	}
	if err := RegisterCoAgentTools(superRegistry, rt); err != nil {
		return err
	}

	researcherRegistry := MustNewToolRegistry()
	if err := RegisterFileTools(researcherRegistry, cwd); err != nil {
		return err
	}
	if err := RegisterResearchTools(researcherRegistry, searchClient, httpClient); err != nil {
		return err
	}
	if err := RegisterCoAgentTools(researcherRegistry, rt); err != nil {
		return err
	}

	conductorRegistry := MustNewToolRegistry()
	if err := RegisterCoAgentTools(conductorRegistry, rt); err != nil {
		return err
	}

	vtextRegistry := MustNewToolRegistry()
	if err := RegisterCoAgentTools(vtextRegistry, rt); err != nil {
		return err
	}

	rt.toolRegistry = superRegistry
	if rt.toolProfiles == nil {
		rt.toolProfiles = make(map[string]*ToolRegistry)
	}
	rt.toolProfiles[AgentProfileConductor] = conductorRegistry
	rt.toolProfiles[AgentProfileSuper] = superRegistry
	rt.toolProfiles[AgentProfileResearcher] = researcherRegistry
	rt.toolProfiles[AgentProfileVText] = vtextRegistry
	return nil
}

func (rt *Runtime) toolRegistryForTask(rec *types.TaskRecord) *ToolRegistry {
	profile := agentProfileForTask(rec)
	if rt.toolProfiles != nil {
		if registry, ok := rt.toolProfiles[profile]; ok && registry != nil {
			return registry
		}
	}
	return rt.toolRegistry
}

func (rt *Runtime) ToolRegistryForProfile(profile string) *ToolRegistry {
	if rt.toolProfiles == nil {
		return nil
	}
	return rt.toolProfiles[strings.TrimSpace(profile)]
}

func toolResultJSON(v map[string]any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
