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
	AgentProfileCoSuper    = "co-super"
	AgentProfileResearcher = "researcher"
	AgentProfileVText      = "vtext"
)

const (
	runMetadataAgentProfile = "agent_profile"
	runMetadataChannelID    = "channel_id"
	runMetadataAgentRole    = "agent_role"
	runMetadataAgentID      = "agent_id"
	runMetadataModel        = "model"
)

type toolContextKey string

const (
	toolCtxRunID    toolContextKey = "run_id"
	toolCtxAgentID  toolContextKey = "agent_id"
	toolCtxOwnerID  toolContextKey = "owner_id"
	toolCtxProfile  toolContextKey = "agent_profile"
	toolCtxRole     toolContextKey = "agent_role"
	toolCtxChannelID toolContextKey = "channel_id"
	toolCtxSandboxID toolContextKey = "sandbox_id"
)

func WithToolExecutionContext(ctx context.Context, rec *types.RunRecord) context.Context {
	ctx = context.WithValue(ctx, toolCtxRunID, rec.RunID)
	ctx = context.WithValue(ctx, toolCtxAgentID, agentIDForRun(rec))
	ctx = context.WithValue(ctx, toolCtxOwnerID, rec.OwnerID)
	ctx = context.WithValue(ctx, toolCtxProfile, agentProfileForRun(rec))
	ctx = context.WithValue(ctx, toolCtxRole, agentRoleForRun(rec))
	ctx = context.WithValue(ctx, toolCtxChannelID, channelIDForRun(rec))
	ctx = context.WithValue(ctx, toolCtxSandboxID, rec.SandboxID)
	return ctx
}

func stringFromToolContext(ctx context.Context, key toolContextKey) string {
	value, _ := ctx.Value(key).(string)
	return strings.TrimSpace(value)
}

func agentProfileForRun(rec *types.RunRecord) string {
	if rec == nil {
		return AgentProfileSuper
	}
	if strings.TrimSpace(rec.AgentProfile) != "" {
		return strings.TrimSpace(rec.AgentProfile)
	}
	if rec.Metadata != nil {
		if profile, _ := rec.Metadata[runMetadataAgentProfile].(string); strings.TrimSpace(profile) != "" {
			return strings.TrimSpace(profile)
		}
	}
	if taskType, _ := rec.Metadata["type"].(string); taskType == "vtext_agent_revision" {
		return AgentProfileVText
	}
	return AgentProfileSuper
}

func agentRoleForRun(rec *types.RunRecord) string {
	if rec == nil {
		return AgentProfileSuper
	}
	if strings.TrimSpace(rec.AgentRole) != "" {
		return strings.TrimSpace(rec.AgentRole)
	}
	if rec.Metadata != nil {
		if role, _ := rec.Metadata[runMetadataAgentRole].(string); strings.TrimSpace(role) != "" {
			return strings.TrimSpace(role)
		}
	}
	return agentProfileForRun(rec)
}

func agentIDForRun(rec *types.RunRecord) string {
	if rec == nil {
		return ""
	}
	if strings.TrimSpace(rec.AgentID) != "" {
		return strings.TrimSpace(rec.AgentID)
	}
	if rec.Metadata != nil {
		if agentID, _ := rec.Metadata[runMetadataAgentID].(string); strings.TrimSpace(agentID) != "" {
			return strings.TrimSpace(agentID)
		}
	}
	return strings.TrimSpace(rec.RunID)
}

func channelIDForRun(rec *types.RunRecord) string {
	if rec == nil {
		return ""
	}
	if strings.TrimSpace(rec.ChannelID) != "" {
		return strings.TrimSpace(rec.ChannelID)
	}
	if rec.Metadata != nil {
		if channelID, _ := rec.Metadata[runMetadataChannelID].(string); strings.TrimSpace(channelID) != "" {
			return strings.TrimSpace(channelID)
		}
		if legacyWorkID, _ := rec.Metadata["work_id"].(string); strings.TrimSpace(legacyWorkID) != "" {
			return strings.TrimSpace(legacyWorkID)
		}
	}
	if strings.TrimSpace(rec.AgentID) != "" {
		return strings.TrimSpace(rec.AgentID)
	}
	return strings.TrimSpace(rec.RunID)
}

type AgentRoleSpec struct {
	Profile                string
	AllowReadOnlyFiles     bool
	AllowWritableFiles     bool
	AllowResearchTools     bool
	AllowEvidenceTools     bool
	AllowCodingTools       bool
	AllowCoAgentTools      bool
	AllowedDelegateTargets []string
}

func roleSpec(profile string) AgentRoleSpec {
	switch strings.TrimSpace(profile) {
	case AgentProfileConductor:
		return AgentRoleSpec{
			Profile:                AgentProfileConductor,
			AllowCoAgentTools:      true,
			AllowedDelegateTargets: []string{AgentProfileVText, AgentProfileResearcher},
		}
	case AgentProfileResearcher:
		return AgentRoleSpec{
			Profile:                AgentProfileResearcher,
			AllowReadOnlyFiles:     true,
			AllowResearchTools:     true,
			AllowEvidenceTools:     true,
			AllowCoAgentTools:      true,
			AllowedDelegateTargets: nil,
		}
	case AgentProfileVText:
		return AgentRoleSpec{
			Profile:                AgentProfileVText,
			AllowEvidenceTools:     true,
			AllowCoAgentTools:      true,
			AllowedDelegateTargets: []string{AgentProfileResearcher, AgentProfileSuper},
		}
	case AgentProfileCoSuper:
		return AgentRoleSpec{
			Profile:                AgentProfileCoSuper,
			AllowWritableFiles:     true,
			AllowResearchTools:     true,
			AllowEvidenceTools:     true,
			AllowCodingTools:       true,
			AllowCoAgentTools:      true,
			AllowedDelegateTargets: []string{AgentProfileResearcher},
		}
	case AgentProfileSuper:
		fallthrough
	default:
		return AgentRoleSpec{
			Profile:                AgentProfileSuper,
			AllowWritableFiles:     true,
			AllowResearchTools:     true,
			AllowEvidenceTools:     true,
			AllowCodingTools:       true,
			AllowCoAgentTools:      true,
			AllowedDelegateTargets: []string{AgentProfileResearcher, AgentProfileCoSuper},
		}
	}
}

func canDelegateTo(callerProfile, targetProfile string) bool {
	spec := roleSpec(callerProfile)
	targetProfile = strings.TrimSpace(targetProfile)
	for _, allowed := range spec.AllowedDelegateTargets {
		if targetProfile == allowed {
			return true
		}
	}
	return false
}

func (rt *Runtime) systemPromptForRun(rec *types.RunRecord) (string, error) {
	profile := agentProfileForRun(rec)
	channelID := channelIDForRun(rec)
	ownerID := ""
	if rec != nil {
		ownerID = rec.OwnerID
	}
	base := "You are a helpful assistant running inside the Choir sandbox runtime."
	if rt != nil && rt.promptStore != nil {
		prompt, err := rt.promptStore.Load(ownerID, profile)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(prompt.Content) != "" {
			base = prompt.Content
		}
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
	if channelID != "" {
		b.WriteString("\n\nCurrent shared channel: ")
		b.WriteString(channelID)
		b.WriteString(".")
	}
	b.WriteString("\nUse shared channels to coordinate with peer agents and keep messages concise and actionable.")
	return b.String(), nil
}

func (rt *Runtime) providerPromptForRun(rec *types.RunRecord) (string, error) {
	systemPrompt, err := rt.systemPromptForRun(rec)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return rec.Prompt, nil
	}
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\nUser request:\n")
	b.WriteString(rec.Prompt)
	return b.String(), nil
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

func (rt *Runtime) buildRegistryForRole(spec AgentRoleSpec, cwd string, searchClient *search.SearchClient, httpClient *http.Client) (*ToolRegistry, error) {
	registry := MustNewToolRegistry()
	if spec.AllowWritableFiles {
		if err := RegisterFileTools(registry, cwd); err != nil {
			return nil, err
		}
	} else if spec.AllowReadOnlyFiles {
		if err := RegisterReadOnlyFileTools(registry, cwd); err != nil {
			return nil, err
		}
	}
	if spec.AllowCodingTools {
		if err := RegisterCodingTools(registry, cwd); err != nil {
			return nil, err
		}
	}
	if spec.AllowResearchTools {
		if err := RegisterResearchTools(registry, searchClient, httpClient); err != nil {
			return nil, err
		}
	}
	if spec.AllowEvidenceTools {
		if err := RegisterEvidenceTools(registry, rt); err != nil {
			return nil, err
		}
	}
	if spec.AllowCoAgentTools {
		if err := RegisterCoAgentTools(registry, rt); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// InstallDefaultAgentTools installs the default profile registries used by the
// local MAS. Capabilities are enforced by role spec, not by prompt warnings.
// Super is the privileged execution root, co-super is its supervised helper,
// researcher gets read-only local files plus research/evidence tools, and
// conductor/vtext get lighter coordination-oriented registries.
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

	superRegistry, err := rt.buildRegistryForRole(roleSpec(AgentProfileSuper), cwd, searchClient, httpClient)
	if err != nil {
		return err
	}
	coSuperRegistry, err := rt.buildRegistryForRole(roleSpec(AgentProfileCoSuper), cwd, searchClient, httpClient)
	if err != nil {
		return err
	}
	researcherRegistry, err := rt.buildRegistryForRole(roleSpec(AgentProfileResearcher), cwd, searchClient, httpClient)
	if err != nil {
		return err
	}
	conductorRegistry, err := rt.buildRegistryForRole(roleSpec(AgentProfileConductor), cwd, searchClient, httpClient)
	if err != nil {
		return err
	}
	vtextRegistry, err := rt.buildRegistryForRole(roleSpec(AgentProfileVText), cwd, searchClient, httpClient)
	if err != nil {
		return err
	}

	rt.toolRegistry = superRegistry
	if rt.toolProfiles == nil {
		rt.toolProfiles = make(map[string]*ToolRegistry)
	}
	rt.toolProfiles[AgentProfileConductor] = conductorRegistry
	rt.toolProfiles[AgentProfileSuper] = superRegistry
	rt.toolProfiles[AgentProfileCoSuper] = coSuperRegistry
	rt.toolProfiles[AgentProfileResearcher] = researcherRegistry
	rt.toolProfiles[AgentProfileVText] = vtextRegistry
	return nil
}

func (rt *Runtime) toolRegistryForRun(rec *types.RunRecord) *ToolRegistry {
	profile := agentProfileForRun(rec)
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
