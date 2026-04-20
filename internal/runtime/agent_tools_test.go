package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
)

func TestInstallDefaultAgentToolsProfiles(t *testing.T) {
	rt, _, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	super := rt.ToolRegistryForProfile(AgentProfileSuper)
	coSuper := rt.ToolRegistryForProfile(AgentProfileCoSuper)
	conductor := rt.ToolRegistryForProfile(AgentProfileConductor)
	researcher := rt.ToolRegistryForProfile(AgentProfileResearcher)
	vtext := rt.ToolRegistryForProfile(AgentProfileVText)

	for _, name := range []string{"bash", "read_file", "web_search", "spawn_agent", "cast_agent", "save_evidence"} {
		if _, ok := super.Lookup(name); !ok {
			t.Fatalf("super missing tool %q", name)
		}
	}
	for _, name := range []string{"bash", "read_file", "web_search", "spawn_agent", "cast_agent", "save_evidence"} {
		if _, ok := coSuper.Lookup(name); !ok {
			t.Fatalf("co-super missing tool %q", name)
		}
	}
	for _, name := range []string{"spawn_agent", "cast_agent", "cancel_agent"} {
		if _, ok := conductor.Lookup(name); !ok {
			t.Fatalf("conductor missing tool %q", name)
		}
	}
	if _, ok := conductor.Lookup("bash"); ok {
		t.Fatalf("conductor should not have bash")
	}
	if _, ok := conductor.Lookup("web_search"); ok {
		t.Fatalf("conductor should not have web_search")
	}

	if _, ok := researcher.Lookup("bash"); ok {
		t.Fatalf("researcher should not have bash")
	}
	if _, ok := researcher.Lookup("write_file"); ok {
		t.Fatalf("researcher should not have write_file")
	}
	if _, ok := researcher.Lookup("edit_file"); ok {
		t.Fatalf("researcher should not have edit_file")
	}
	for _, name := range []string{"read_file", "web_search", "spawn_agent", "cast_agent", "save_evidence", "submit_research_findings"} {
		if _, ok := researcher.Lookup(name); !ok {
			t.Fatalf("researcher missing tool %q", name)
		}
	}
	for _, name := range []string{"spawn_agent", "cast_agent", "cancel_agent", "save_evidence", "read_evidence"} {
		if _, ok := vtext.Lookup(name); !ok {
			t.Fatalf("vtext missing tool %q", name)
		}
	}
	if _, ok := vtext.Lookup("bash"); ok {
		t.Fatalf("vtext should not have bash")
	}
	if _, ok := vtext.Lookup("web_search"); ok {
		t.Fatalf("vtext should not have web_search")
	}
	if _, ok := vtext.Lookup("submit_research_findings"); ok {
		t.Fatalf("vtext should not have submit_research_findings")
	}
}

func TestCoagentToolsSupportAddressedCastAcrossProfiles(t *testing.T) {
	rt, s, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	parent, err := rt.StartRunWithMetadata(context.Background(), "coordinate work", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileSuper,
		runMetadataAgentRole:    AgentProfileSuper,
	})
	if err != nil {
		t.Fatalf("submit parent task: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	superRegistry := rt.ToolRegistryForProfile(AgentProfileSuper)
	spawnRaw, err := superRegistry.Execute(WithToolExecutionContext(context.Background(), parent), "spawn_agent", json.RawMessage(`{
		"objective":"research the codebase and report back",
		"role":"researcher",
		"channel_id":"shared-work"
	}`))
	if err != nil {
		t.Fatalf("spawn_agent: %v", err)
	}

	var spawnResp struct {
		RunID     string `json:"loop_id"`
		ChannelID string `json:"channel_id"`
		Profile   string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(spawnRaw), &spawnResp); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}
	if spawnResp.Profile != AgentProfileResearcher {
		t.Fatalf("spawned profile = %q, want %q", spawnResp.Profile, AgentProfileResearcher)
	}
	if spawnResp.ChannelID != "shared-work" {
		t.Fatalf("spawned channel_id = %q, want shared-work", spawnResp.ChannelID)
	}

	child, err := s.GetRun(context.Background(), spawnResp.RunID)
	if err != nil {
		t.Fatalf("get child task: %v", err)
	}
	if got := child.Metadata[runMetadataAgentProfile]; got != AgentProfileResearcher {
		t.Fatalf("child agent_profile = %v, want %q", got, AgentProfileResearcher)
	}
	if child.ChannelID != "shared-work" {
		t.Fatalf("child channel_id = %q, want shared-work", child.ChannelID)
	}

	postRaw, err := superRegistry.Execute(
		WithToolExecutionContext(context.Background(), parent),
		"cast_agent",
		json.RawMessage(fmt.Sprintf(`{
		"agent_id":"%s",
		"channel_id":"shared-work",
		"content":"please inspect the runtime tool wiring"
	}`, child.AgentID)),
	)
	if err != nil {
		t.Fatalf("cast_agent: %v", err)
	}
	var postResp struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(postRaw), &postResp); err != nil {
		t.Fatalf("decode post response: %v", err)
	}

	msgs, _, err := rt.ChannelRead("shared-work", 0)
	if err != nil {
		t.Fatalf("channel read: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "please inspect the runtime tool wiring" {
		t.Fatalf("unexpected channel messages: %+v", msgs)
	}
	if msgs[0].ToAgentID != child.AgentID {
		t.Fatalf("channel message to_agent_id = %q, want %q", msgs[0].ToAgentID, child.AgentID)
	}
	deliveries, err := s.ListPendingInboxDeliveries(context.Background(), "user-alice", child.AgentID, 10)
	if err != nil {
		t.Fatalf("list inbox deliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Content != "please inspect the runtime tool wiring" {
		t.Fatalf("unexpected deliveries: %+v", deliveries)
	}
}

func TestDelegationAllowlistsAndEvidenceTools(t *testing.T) {
	rt, s, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	vtextTask, err := rt.StartRunWithMetadata(context.Background(), "revise document", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileVText,
		runMetadataAgentRole:    AgentProfileVText,
	})
	if err != nil {
		t.Fatalf("submit vtext task: %v", err)
	}
	superTask, err := rt.StartRunWithMetadata(context.Background(), "coordinate execution", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileSuper,
		runMetadataAgentRole:    AgentProfileSuper,
	})
	if err != nil {
		t.Fatalf("submit super task: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	vtextRegistry := rt.ToolRegistryForProfile(AgentProfileVText)
	superSpawnRaw, err := vtextRegistry.Execute(WithToolExecutionContext(context.Background(), vtextTask), "spawn_agent", json.RawMessage(`{
		"objective":"handle execution-heavy follow-up",
		"role":"super",
		"channel_id":"doc-exec-work"
	}`))
	if err != nil {
		t.Fatalf("vtext spawn super: %v", err)
	}
	var superSpawn struct {
		RunID     string `json:"loop_id"`
		Profile   string `json:"profile"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(superSpawnRaw), &superSpawn); err != nil {
		t.Fatalf("decode super spawn: %v", err)
	}
	if superSpawn.Profile != AgentProfileSuper {
		t.Fatalf("super spawn profile = %q, want %q", superSpawn.Profile, AgentProfileSuper)
	}
	if superSpawn.ChannelID != "doc-exec-work" {
		t.Fatalf("super spawn channel_id = %q, want doc-exec-work", superSpawn.ChannelID)
	}

	superRegistry := rt.ToolRegistryForProfile(AgentProfileSuper)
	coSuperRaw, err := superRegistry.Execute(WithToolExecutionContext(context.Background(), superTask), "spawn_agent", json.RawMessage(`{
		"objective":"handle execution subtree",
		"role":"co-super"
	}`))
	if err != nil {
		t.Fatalf("super spawn co-super: %v", err)
	}
	var coSuperSpawn struct {
		RunID   string `json:"loop_id"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(coSuperRaw), &coSuperSpawn); err != nil {
		t.Fatalf("decode co-super spawn: %v", err)
	}
	if coSuperSpawn.Profile != AgentProfileCoSuper {
		t.Fatalf("co-super profile = %q, want %q", coSuperSpawn.Profile, AgentProfileCoSuper)
	}

	child, err := s.GetRun(context.Background(), coSuperSpawn.RunID)
	if err != nil {
		t.Fatalf("get co-super task: %v", err)
	}
	coSuperRegistry := rt.ToolRegistryForProfile(AgentProfileCoSuper)
	if _, err := coSuperRegistry.Execute(WithToolExecutionContext(context.Background(), &child), "spawn_agent", json.RawMessage(`{
		"objective":"try to escape supervision",
		"role":"super"
	}`)); err == nil {
		t.Fatalf("co-super should not be allowed to spawn super")
	}

	researcherTask, err := rt.StartRunWithMetadata(context.Background(), "gather evidence", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileResearcher,
		runMetadataAgentRole:    AgentProfileResearcher,
	})
	if err != nil {
		t.Fatalf("submit researcher task: %v", err)
	}
	researcherRegistry := rt.ToolRegistryForProfile(AgentProfileResearcher)
	saveRaw, err := researcherRegistry.Execute(WithToolExecutionContext(context.Background(), researcherTask), "save_evidence", json.RawMessage(`{
		"kind":"web_page",
		"source_uri":"https://example.com",
		"title":"Example",
		"content":"captured evidence"
	}`))
	if err != nil {
		t.Fatalf("save_evidence: %v", err)
	}
	var saveResp struct {
		EvidenceID string `json:"evidence_id"`
		AgentID    string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(saveRaw), &saveResp); err != nil {
		t.Fatalf("decode save_evidence: %v", err)
	}
	if saveResp.AgentID == "" || saveResp.EvidenceID == "" {
		t.Fatalf("unexpected save response: %+v", saveResp)
	}
	evidence, err := s.GetEvidence(context.Background(), saveResp.EvidenceID, "user-alice")
	if err != nil {
		t.Fatalf("get evidence: %v", err)
	}
	if evidence.Content != "captured evidence" {
		t.Fatalf("evidence content = %q, want %q", evidence.Content, "captured evidence")
	}
}

func TestConductorCanSpawnVTextAndVTextCanSpawnResearcher(t *testing.T) {
	rt, s, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	conductorTask, err := rt.StartRunWithMetadata(context.Background(), "route this request", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileConductor,
		runMetadataAgentRole:    AgentProfileConductor,
	})
	if err != nil {
		t.Fatalf("submit conductor task: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	conductorRegistry := rt.ToolRegistryForProfile(AgentProfileConductor)
	vtextSpawnRaw, err := conductorRegistry.Execute(WithToolExecutionContext(context.Background(), conductorTask), "spawn_agent", json.RawMessage(`{
		"objective":"create v0 and own the document",
		"role":"vtext",
		"channel_id":"doc-work"
	}`))
	if err != nil {
		t.Fatalf("conductor spawn vtext: %v", err)
	}
	var vtextSpawn struct {
		RunID     string `json:"loop_id"`
		Profile   string `json:"profile"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(vtextSpawnRaw), &vtextSpawn); err != nil {
		t.Fatalf("decode vtext spawn: %v", err)
	}
	if vtextSpawn.Profile != AgentProfileVText {
		t.Fatalf("vtext spawn profile = %q, want %q", vtextSpawn.Profile, AgentProfileVText)
	}
	if vtextSpawn.ChannelID == "" {
		t.Fatal("vtext spawn channel_id should not be empty")
	}
	vtextTask, err := s.GetRun(context.Background(), vtextSpawn.RunID)
	if err != nil {
		t.Fatalf("get vtext task: %v", err)
	}
	if vtextTask.Metadata["doc_id"] != vtextSpawn.ChannelID {
		t.Fatalf("vtext task doc_id = %v, want %q", vtextTask.Metadata["doc_id"], vtextSpawn.ChannelID)
	}
	parentAfterSpawn, err := s.GetRun(context.Background(), conductorTask.RunID)
	if err != nil {
		t.Fatalf("get conductor task: %v", err)
	}
	if parentAfterSpawn.Metadata["doc_id"] != vtextSpawn.ChannelID {
		t.Fatalf("conductor metadata doc_id = %v, want %q", parentAfterSpawn.Metadata["doc_id"], vtextSpawn.ChannelID)
	}
	if strings.TrimSpace(parentAfterSpawn.Result) == "" {
		t.Fatal("conductor result should be populated as soon as vtext is opened")
	}
	var parentDecision struct {
		Action            string `json:"action"`
		App               string `json:"app"`
		DocID             string `json:"doc_id"`
		InitialRunID      string `json:"initial_loop_id"`
		InitialRevisionID string `json:"initial_revision_id"`
	}
	if err := json.Unmarshal([]byte(parentAfterSpawn.Result), &parentDecision); err != nil {
		t.Fatalf("decode conductor result: %v", err)
	}
	if parentDecision.Action != "open_app" || parentDecision.App != AgentProfileVText {
		t.Fatalf("unexpected conductor decision: %+v", parentDecision)
	}
	if parentDecision.DocID != vtextSpawn.ChannelID {
		t.Fatalf("conductor result doc_id = %q, want %q", parentDecision.DocID, vtextSpawn.ChannelID)
	}
	if parentDecision.InitialRunID != vtextTask.RunID {
		t.Fatalf("conductor result initial_loop_id = %q, want %q", parentDecision.InitialRunID, vtextTask.RunID)
	}
	if parentDecision.InitialRevisionID == "" {
		t.Fatal("conductor result initial_revision_id should not be empty")
	}

	vtextRegistry := rt.ToolRegistryForProfile(AgentProfileVText)
	researchSpawnRaw, err := vtextRegistry.Execute(WithToolExecutionContext(context.Background(), &vtextTask), "spawn_agent", json.RawMessage(`{
		"objective":"research background facts for the document",
		"role":"researcher",
		"channel_id":"doc-work"
	}`))
	if err != nil {
		t.Fatalf("vtext spawn researcher: %v", err)
	}
	var researchSpawn struct {
		RunID     string `json:"loop_id"`
		Profile   string `json:"profile"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(researchSpawnRaw), &researchSpawn); err != nil {
		t.Fatalf("decode researcher spawn: %v", err)
	}
	if researchSpawn.Profile != AgentProfileResearcher {
		t.Fatalf("research spawn profile = %q, want %q", researchSpawn.Profile, AgentProfileResearcher)
	}
	if researchSpawn.ChannelID != "doc-work" {
		t.Fatalf("research spawn channel_id = %q, want doc-work", researchSpawn.ChannelID)
	}
}

func TestResearcherSubmitResearchFindingsPersistsEvidenceAndDedupes(t *testing.T) {
	rt, s, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	vtextTask, err := rt.StartRunWithMetadata(context.Background(), "own the draft", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileVText,
		runMetadataAgentRole:    AgentProfileVText,
		runMetadataChannelID:    "doc-1",
		runMetadataAgentID:      "vtext:doc-1",
	})
	if err != nil {
		t.Fatalf("submit vtext task: %v", err)
	}
	researcherTask, err := rt.StartChildRun(context.Background(), vtextTask.RunID, "research the claim", "user-alice", map[string]any{
		runMetadataAgentProfile: AgentProfileResearcher,
		runMetadataAgentRole:    AgentProfileResearcher,
		runMetadataChannelID:    "doc-1",
	})
	if err != nil {
		t.Fatalf("submit researcher task: %v", err)
	}

	researcherRegistry := rt.ToolRegistryForProfile(AgentProfileResearcher)
	raw, err := researcherRegistry.Execute(WithToolExecutionContext(context.Background(), researcherTask), "submit_research_findings", json.RawMessage(`{
		"finding_id":"finding-001",
		"findings":["Model releases this week improved reasoning and tool use."],
		"evidence":[
			{
				"kind":"web_page",
				"source_uri":"https://example.com/release",
				"title":"Release notes",
				"content":"Release notes describing stronger reasoning and tool use."
			}
		],
		"notes":["The claim is recent enough that priors alone are weak."],
		"questions":["Should we mention the release date explicitly?"]
	}`))
	if err != nil {
		t.Fatalf("submit_research_findings: %v", err)
	}

	var resp struct {
		FindingID   string   `json:"finding_id"`
		AgentID     string   `json:"agent_id"`
		ChannelID   string   `json:"channel_id"`
		Cursor      int64    `json:"cursor"`
		EvidenceIDs []string `json:"evidence_ids"`
		Status      string   `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode submit_research_findings: %v", err)
	}
	if resp.Status != "submitted" {
		t.Fatalf("status = %q, want submitted", resp.Status)
	}
	if resp.AgentID != "vtext:doc-1" {
		t.Fatalf("agent_id = %q, want %q", resp.AgentID, "vtext:doc-1")
	}
	if len(resp.EvidenceIDs) != 1 {
		t.Fatalf("evidence_ids = %+v, want 1 id", resp.EvidenceIDs)
	}

	evidence, err := s.GetEvidence(context.Background(), resp.EvidenceIDs[0], "user-alice")
	if err != nil {
		t.Fatalf("get evidence: %v", err)
	}
	if evidence.Title != "Release notes" {
		t.Fatalf("evidence title = %q, want %q", evidence.Title, "Release notes")
	}

	finding, err := s.GetResearchFinding(context.Background(), "user-alice", "finding-001")
	if err != nil {
		t.Fatalf("get research finding: %v", err)
	}
	if finding.MessageSeq != resp.Cursor {
		t.Fatalf("finding message_seq = %d, want %d", finding.MessageSeq, resp.Cursor)
	}

	rawAgain, err := researcherRegistry.Execute(WithToolExecutionContext(context.Background(), researcherTask), "submit_research_findings", json.RawMessage(`{
		"finding_id":"finding-001",
		"findings":["Model releases this week improved reasoning and tool use."],
		"evidence":[
			{
				"kind":"web_page",
				"source_uri":"https://example.com/release",
				"title":"Release notes",
				"content":"Release notes describing stronger reasoning and tool use."
			}
		],
		"notes":["The claim is recent enough that priors alone are weak."],
		"questions":["Should we mention the release date explicitly?"]
	}`))
	if err != nil {
		t.Fatalf("repeat submit_research_findings: %v", err)
	}
	var respAgain struct {
		Cursor int64  `json:"cursor"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(rawAgain), &respAgain); err != nil {
		t.Fatalf("decode repeated submit_research_findings: %v", err)
	}
	if respAgain.Status != "existing" {
		t.Fatalf("repeat status = %q, want existing", respAgain.Status)
	}
	if respAgain.Cursor != resp.Cursor {
		t.Fatalf("repeat cursor = %d, want %d", respAgain.Cursor, resp.Cursor)
	}

	messages, err := s.ListChannelMessages(context.Background(), "user-alice", "doc-1", 0, 10)
	if err != nil {
		t.Fatalf("list channel messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("channel messages = %d, want 1", len(messages))
	}
	if messages[0].ToAgentID != "vtext:doc-1" {
		t.Fatalf("channel message to_agent_id = %q, want %q", messages[0].ToAgentID, "vtext:doc-1")
	}
	if !strings.Contains(messages[0].Content, resp.EvidenceIDs[0]) {
		t.Fatalf("channel message content missing evidence id: %q", messages[0].Content)
	}
}

func testRuntimeWithTempCWD(t *testing.T) (*Runtime, *store.Store, string) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "go-choir-m3-agent-tools-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, t.Name()+".db")
	cwd := filepath.Join(dir, t.Name()+"-cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("create tool cwd: %v", err)
	}
	_ = os.Remove(dbPath)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	rt := New(Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     5 * time.Second,
		SupervisionInterval: time.Hour,
	}, s, events.NewEventBus(), NewStubProvider(10*time.Millisecond))

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
		_ = os.Remove(dbPath)
	})

	return rt, s, cwd
}
