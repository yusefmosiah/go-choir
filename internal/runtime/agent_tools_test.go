package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

	for _, name := range []string{"bash", "read_file", "web_search", "spawn_agent", "post_message", "save_evidence"} {
		if _, ok := super.Lookup(name); !ok {
			t.Fatalf("super missing tool %q", name)
		}
	}
	for _, name := range []string{"bash", "read_file", "web_search", "spawn_agent", "post_message", "save_evidence"} {
		if _, ok := coSuper.Lookup(name); !ok {
			t.Fatalf("co-super missing tool %q", name)
		}
	}
	for _, name := range []string{"spawn_agent", "post_message", "read_messages", "wait_for_message", "close_agent"} {
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
	for _, name := range []string{"read_file", "web_search", "spawn_agent", "post_message", "wait_for_message", "save_evidence"} {
		if _, ok := researcher.Lookup(name); !ok {
			t.Fatalf("researcher missing tool %q", name)
		}
	}
	for _, name := range []string{"spawn_agent", "post_message", "read_messages", "wait_for_message", "close_agent", "save_evidence", "read_evidence"} {
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
}

func TestCoagentToolsSupportSharedMessagingAcrossProfiles(t *testing.T) {
	rt, s, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	parent, err := rt.SubmitTaskWithMetadata(context.Background(), "coordinate work", "user-alice", map[string]any{
		taskMetadataAgentProfile: AgentProfileSuper,
		taskMetadataAgentRole:    AgentProfileSuper,
	})
	if err != nil {
		t.Fatalf("submit parent task: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	superRegistry := rt.ToolRegistryForProfile(AgentProfileSuper)
	spawnRaw, err := superRegistry.Execute(WithToolExecutionContext(context.Background(), parent), "spawn_agent", json.RawMessage(`{
		"objective":"research the codebase and report back",
		"role":"researcher",
		"work_id":"shared-work"
	}`))
	if err != nil {
		t.Fatalf("spawn_agent: %v", err)
	}

	var spawnResp struct {
		TaskID  string `json:"task_id"`
		WorkID  string `json:"work_id"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(spawnRaw), &spawnResp); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}
	if spawnResp.Profile != AgentProfileResearcher {
		t.Fatalf("spawned profile = %q, want %q", spawnResp.Profile, AgentProfileResearcher)
	}
	if spawnResp.WorkID != "shared-work" {
		t.Fatalf("spawned work_id = %q, want shared-work", spawnResp.WorkID)
	}

	child, err := s.GetTask(context.Background(), spawnResp.TaskID)
	if err != nil {
		t.Fatalf("get child task: %v", err)
	}
	if got := child.Metadata[taskMetadataAgentProfile]; got != AgentProfileResearcher {
		t.Fatalf("child agent_profile = %v, want %q", got, AgentProfileResearcher)
	}
	if got := child.Metadata[taskMetadataWorkID]; got != "shared-work" {
		t.Fatalf("child work_id = %v, want shared-work", got)
	}

	postRaw, err := superRegistry.Execute(WithToolExecutionContext(context.Background(), parent), "post_message", json.RawMessage(`{
		"work_id":"shared-work",
		"content":"please inspect the runtime tool wiring"
	}`))
	if err != nil {
		t.Fatalf("post_message: %v", err)
	}
	var postResp struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(postRaw), &postResp); err != nil {
		t.Fatalf("decode post response: %v", err)
	}

	researchRegistry := rt.ToolRegistryForProfile(AgentProfileResearcher)
	readRaw, err := researchRegistry.Execute(WithToolExecutionContext(context.Background(), &child), "read_messages", json.RawMessage(`{
		"work_id":"shared-work"
	}`))
	if err != nil {
		t.Fatalf("read_messages: %v", err)
	}
	var readResp struct {
		Messages []ChannelMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(readRaw), &readResp); err != nil {
		t.Fatalf("decode read response: %v", err)
	}
	if len(readResp.Messages) != 1 || readResp.Messages[0].Content != "please inspect the runtime tool wiring" {
		t.Fatalf("unexpected messages: %+v", readResp.Messages)
	}

	if _, err := researchRegistry.Execute(WithToolExecutionContext(context.Background(), &child), "post_message", json.RawMessage(`{
		"work_id":"shared-work",
		"content":"runtime looks good; proceeding with structured findings"
	}`)); err != nil {
		t.Fatalf("researcher post_message: %v", err)
	}

	waitRaw, err := superRegistry.Execute(WithToolExecutionContext(context.Background(), parent), "wait_for_message", json.RawMessage(`{
		"work_id":"shared-work",
		"cursor":1,
		"timeout_ms":50
	}`))
	if err != nil {
		t.Fatalf("wait_for_message: %v", err)
	}
	var waitResp struct {
		Messages []ChannelMessage `json:"messages"`
		TimedOut bool             `json:"timed_out"`
	}
	if err := json.Unmarshal([]byte(waitRaw), &waitResp); err != nil {
		t.Fatalf("decode wait response: %v", err)
	}
	if waitResp.TimedOut {
		t.Fatalf("wait_for_message unexpectedly timed out")
	}
	if len(waitResp.Messages) != 1 || waitResp.Messages[0].Content != "runtime looks good; proceeding with structured findings" {
		t.Fatalf("unexpected wait messages: %+v", waitResp.Messages)
	}
}

func TestDelegationAllowlistsAndEvidenceTools(t *testing.T) {
	rt, s, cwd := testRuntimeWithTempCWD(t)
	if err := rt.InstallDefaultAgentTools(cwd); err != nil {
		t.Fatalf("install default agent tools: %v", err)
	}

	vtextTask, err := rt.SubmitTaskWithMetadata(context.Background(), "revise document", "user-alice", map[string]any{
		taskMetadataAgentProfile: AgentProfileVText,
		taskMetadataAgentRole:    AgentProfileVText,
	})
	if err != nil {
		t.Fatalf("submit vtext task: %v", err)
	}
	superTask, err := rt.SubmitTaskWithMetadata(context.Background(), "coordinate execution", "user-alice", map[string]any{
		taskMetadataAgentProfile: AgentProfileSuper,
		taskMetadataAgentRole:    AgentProfileSuper,
	})
	if err != nil {
		t.Fatalf("submit super task: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	vtextRegistry := rt.ToolRegistryForProfile(AgentProfileVText)
	if _, err := vtextRegistry.Execute(WithToolExecutionContext(context.Background(), vtextTask), "spawn_agent", json.RawMessage(`{
		"objective":"try to create a privileged executor",
		"role":"super"
	}`)); err == nil {
		t.Fatalf("vtext should not be allowed to spawn super")
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
		TaskID  string `json:"task_id"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(coSuperRaw), &coSuperSpawn); err != nil {
		t.Fatalf("decode co-super spawn: %v", err)
	}
	if coSuperSpawn.Profile != AgentProfileCoSuper {
		t.Fatalf("co-super profile = %q, want %q", coSuperSpawn.Profile, AgentProfileCoSuper)
	}

	child, err := s.GetTask(context.Background(), coSuperSpawn.TaskID)
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

	researcherTask, err := rt.SubmitTaskWithMetadata(context.Background(), "gather evidence", "user-alice", map[string]any{
		taskMetadataAgentProfile: AgentProfileResearcher,
		taskMetadataAgentRole:    AgentProfileResearcher,
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

	conductorTask, err := rt.SubmitTaskWithMetadata(context.Background(), "route this request", "user-alice", map[string]any{
		taskMetadataAgentProfile: AgentProfileConductor,
		taskMetadataAgentRole:    AgentProfileConductor,
	})
	if err != nil {
		t.Fatalf("submit conductor task: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	conductorRegistry := rt.ToolRegistryForProfile(AgentProfileConductor)
	vtextSpawnRaw, err := conductorRegistry.Execute(WithToolExecutionContext(context.Background(), conductorTask), "spawn_agent", json.RawMessage(`{
		"objective":"create v0 and own the document",
		"role":"vtext",
		"work_id":"doc-work"
	}`))
	if err != nil {
		t.Fatalf("conductor spawn vtext: %v", err)
	}
	var vtextSpawn struct {
		TaskID  string `json:"task_id"`
		Profile string `json:"profile"`
		WorkID  string `json:"work_id"`
	}
	if err := json.Unmarshal([]byte(vtextSpawnRaw), &vtextSpawn); err != nil {
		t.Fatalf("decode vtext spawn: %v", err)
	}
	if vtextSpawn.Profile != AgentProfileVText {
		t.Fatalf("vtext spawn profile = %q, want %q", vtextSpawn.Profile, AgentProfileVText)
	}
	vtextTask, err := s.GetTask(context.Background(), vtextSpawn.TaskID)
	if err != nil {
		t.Fatalf("get vtext task: %v", err)
	}

	vtextRegistry := rt.ToolRegistryForProfile(AgentProfileVText)
	researchSpawnRaw, err := vtextRegistry.Execute(WithToolExecutionContext(context.Background(), &vtextTask), "spawn_agent", json.RawMessage(`{
		"objective":"research background facts for the document",
		"role":"researcher",
		"work_id":"doc-work"
	}`))
	if err != nil {
		t.Fatalf("vtext spawn researcher: %v", err)
	}
	var researchSpawn struct {
		TaskID  string `json:"task_id"`
		Profile string `json:"profile"`
		WorkID  string `json:"work_id"`
	}
	if err := json.Unmarshal([]byte(researchSpawnRaw), &researchSpawn); err != nil {
		t.Fatalf("decode researcher spawn: %v", err)
	}
	if researchSpawn.Profile != AgentProfileResearcher {
		t.Fatalf("research spawn profile = %q, want %q", researchSpawn.Profile, AgentProfileResearcher)
	}
	if researchSpawn.WorkID != "doc-work" {
		t.Fatalf("research spawn work_id = %q, want doc-work", researchSpawn.WorkID)
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
