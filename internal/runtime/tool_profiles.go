package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

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
	runMetadataDesktopID    = "desktop_id"
)

const choirCoreSystemPrompt = `You are one agent inside Choir, a multiagent writing, research, and execution system.

All Choir agents share one user-facing product, one runtime, and one standard of truth. You are not an isolated chatbot. You are one participant in an ongoing workflow with durable documents, durable agents, explicit coordination edges, event history, and revision history.

The user cares about getting informed work, not performative agent chatter. Prefer useful progress over narration. Do not bluff about research, evidence, or execution. If outside facts, validation, or real system work are needed, use the right tools or delegate to the right agent role.

The runtime owns delivery. When another agent sends you addressed work, the runtime may thread that delivery into your loop as a normal next user turn. Do not poll for inbox state yourself.

Conductor routes top-level intent. VText owns canonical document versions. Researcher gathers evidence and grounded findings. Super handles execution-heavy work. Co-super is Super's supervised helper.

Protect the user's trust. Keep claims calibrated, preserve provenance, and optimize for the next useful step in the shared workflow.`

type toolContextKey string

const (
	toolCtxRunID     toolContextKey = "loop_id"
	toolCtxAgentID   toolContextKey = "agent_id"
	toolCtxOwnerID   toolContextKey = "owner_id"
	toolCtxProfile   toolContextKey = "agent_profile"
	toolCtxRole      toolContextKey = "agent_role"
	toolCtxChannelID toolContextKey = "channel_id"
	toolCtxSandboxID toolContextKey = "sandbox_id"
	toolCtxDesktopID toolContextKey = "desktop_id"
	toolCtxRunRecord toolContextKey = "run_record"
)

func WithToolExecutionContext(ctx context.Context, rec *types.RunRecord) context.Context {
	ctx = context.WithValue(ctx, toolCtxRunID, rec.RunID)
	ctx = context.WithValue(ctx, toolCtxAgentID, agentIDForRun(rec))
	ctx = context.WithValue(ctx, toolCtxOwnerID, rec.OwnerID)
	ctx = context.WithValue(ctx, toolCtxProfile, configuredAgentProfileForRun(rec))
	ctx = context.WithValue(ctx, toolCtxRole, agentRoleForRun(rec))
	ctx = context.WithValue(ctx, toolCtxChannelID, channelIDForRun(rec))
	ctx = context.WithValue(ctx, toolCtxSandboxID, rec.SandboxID)
	ctx = context.WithValue(ctx, toolCtxDesktopID, desktopIDForRun(rec))
	ctx = context.WithValue(ctx, toolCtxRunRecord, rec)
	return ctx
}

func stringFromToolContext(ctx context.Context, key toolContextKey) string {
	value, _ := ctx.Value(key).(string)
	return strings.TrimSpace(value)
}

func configuredAgentProfileForRun(rec *types.RunRecord) string {
	if rec == nil {
		return ""
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
	return ""
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

func desktopIDForRun(rec *types.RunRecord) string {
	if rec == nil {
		return types.PrimaryDesktopID
	}
	if rec.Metadata != nil {
		if desktopID, _ := rec.Metadata[runMetadataDesktopID].(string); strings.TrimSpace(desktopID) != "" {
			return strings.TrimSpace(desktopID)
		}
	}
	return types.PrimaryDesktopID
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
		return AgentRoleSpec{
			Profile:                AgentProfileSuper,
			AllowWritableFiles:     true,
			AllowResearchTools:     true,
			AllowEvidenceTools:     true,
			AllowCodingTools:       true,
			AllowCoAgentTools:      true,
			AllowedDelegateTargets: []string{AgentProfileResearcher, AgentProfileCoSuper},
		}
	default:
		return AgentRoleSpec{Profile: strings.TrimSpace(profile)}
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
	rolePrompt := fmt.Sprintf("You are Choir %s.", profile)
	if rt != nil && rt.promptStore != nil {
		prompt, err := rt.promptStore.Load(ownerID, profile)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(prompt.Content) != "" {
			rolePrompt = prompt.Content
		}
	}

	var b strings.Builder
	b.WriteString(choirCoreSystemPrompt)
	if strings.TrimSpace(rolePrompt) != "" {
		b.WriteString("\n\nRole-specific instructions:\n")
		b.WriteString(rolePrompt)
	}
	if profile == AgentProfileConductor {
		requestedApp, _ := rec.Metadata["requested_app"].(string)
		seedPrompt, _ := rec.Metadata["seed_prompt"].(string)
		if requestedApp == "" {
			requestedApp = AgentProfileVText
		}
		b.WriteString("\n\nFor substantial work, route by using coagent tools. Prefer spawn_agent with role=vtext so VText becomes the durable owner of the next step.")
		b.WriteString("\nFor lightweight acknowledgements with no app handoff, return one compact JSON object like {\"action\":\"toast\",\"message\":\"...\"}.")
		b.WriteString("\nIf you already opened the next owner with a tool call, you may finish tersely; the runtime will surface the opened app from the routed result.")
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
	if profile == AgentProfileVText {
		b.WriteString("\n\nVText is a durable document owner, not a one-shot answerer.")
		b.WriteString("\nWrite the best current version promptly from the canonical document, current context, and your priors.")
		b.WriteString("\nLater addressed worker deliveries can be threaded into this loop or wake the next VText run and trigger another revision.")
		b.WriteString("\nBuild each revision from the current canonical version, recent worker messages, recent change context, and user-authored diffs.")
		b.WriteString("\nIntermediate appagent revisions are compactable working memory. Keep the current canonical document and user-authored changes authoritative.")
		b.WriteString("\nWhen research is needed, default to one focused researcher first instead of speculative parallel fan-out.")
		b.WriteString("\nOnly spawn multiple researchers when the work has clearly independent branches and the first researcher cannot cover them efficiently.")
		b.WriteString("\nPrefer sequential grounded passes over opening several broad researchers at once.")
		b.WriteString("\nAs soon as one grounded findings packet is enough to improve the document, write the next revision instead of waiting for perfect coverage.")
	}
	if profile == AgentProfileResearcher {
		b.WriteString("\n\nResearcher loops must converge quickly.")
		b.WriteString("\nUse web_search only until you have one useful findings packet for the owning agent, usually one or two focused searches.")
		b.WriteString("\nDo not keep issuing near-duplicate searches once you already have enough grounded material to improve the document.")
		b.WriteString("\nAs soon as you have at least one substantive grounded finding, call submit_research_findings.")
		b.WriteString("\nImmediately after submit_research_findings, stop searching and end the turn unless a later runtime delivery asks for another pass.")
	}
	agentID := agentIDForRun(rec)
	if agentID != "" {
		b.WriteString("\n\nCurrent agent id: ")
		b.WriteString(agentID)
		b.WriteString(".")
	}
	if rec != nil && strings.TrimSpace(rec.ParentRunID) != "" && rt != nil && rt.store != nil {
		if parentRun, err := rt.store.GetRun(context.Background(), strings.TrimSpace(rec.ParentRunID)); err == nil {
			parentAgentID := agentIDForRun(&parentRun)
			if parentAgentID != "" {
				b.WriteString("\nParent agent id: ")
				b.WriteString(parentAgentID)
				b.WriteString(".")
			}
		}
	}
	if channelID != "" {
		b.WriteString("\nCurrent coordination channel: ")
		b.WriteString(channelID)
		b.WriteString(".")
	}
	b.WriteString("\nUse addressed casts for peer coordination and keep messages concise and actionable.")
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

func (rt *Runtime) buildRegistryForRole(spec AgentRoleSpec, cwd string, searchClient webSearchClient, httpClient *http.Client) (*ToolRegistry, error) {
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

	searchClient := newGatewaySearchClientFromEnv()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	superRegistry, err := rt.buildRegistryForRole(roleSpec(AgentProfileSuper), cwd, searchClient, httpClient)
	if err != nil {
		return err
	}
	if err := RegisterVMControlTools(superRegistry, rt); err != nil {
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
	if err := RegisterResearcherTools(researcherRegistry, rt); err != nil {
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
	profile := configuredAgentProfileForRun(rec)
	if profile == "" {
		return nil
	}
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
