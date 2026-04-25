package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yusefmosiah/go-choir/internal/types"
)

func seedTraceTrajectory(t *testing.T, rt *Runtime) (*types.RunRecord, *types.RunRecord) {
	t.Helper()

	parent, err := rt.StartRunWithMetadata(context.Background(), "Investigate moss habitats", "user-alice", map[string]any{
		runMetadataAgentProfile: "conductor",
		runMetadataAgentRole:    "conductor",
	})
	if err != nil {
		t.Fatalf("start parent run: %v", err)
	}
	child, err := rt.StartChildRun(context.Background(), parent.RunID, "Research the best conditions for moss", "user-alice", map[string]any{
		runMetadataAgentProfile: "researcher",
		runMetadataAgentRole:    "researcher",
	})
	if err != nil {
		t.Fatalf("start child run: %v", err)
	}

	findingAt := time.Now().UTC()
	message := &types.ChannelMessage{
		ChannelID:    child.ChannelID,
		From:         "researcher",
		FromAgentID:  child.AgentID,
		FromRunID:    child.RunID,
		ToAgentID:    parent.AgentID,
		TrajectoryID: parent.RunID,
		Role:         "researcher",
		Content:      "Finding: moss thrives in damp shade with steady humidity.",
		Timestamp:    findingAt,
	}
	finding := types.ResearchFindingRecord{
		FindingID:     "finding-" + uuid.NewString(),
		OwnerID:       "user-alice",
		AgentID:       child.AgentID,
		TargetAgentID: parent.AgentID,
		ChannelID:     child.ChannelID,
		TrajectoryID:  parent.RunID,
		Findings:      []string{"Moss thrives in damp shade with steady humidity."},
		EvidenceIDs:   []string{"ev-moss-1"},
		Notes:         []string{"Humidity matters more than direct light."},
		Questions:     []string{"Which moss species tolerate brighter light?"},
		Content:       message.Content,
		CreatedAt:     findingAt,
	}
	delivery := types.InboxDelivery{
		DeliveryID:   "delivery-" + uuid.NewString(),
		OwnerID:      "user-alice",
		ToAgentID:    parent.AgentID,
		FromAgentID:  child.AgentID,
		FromRunID:    child.RunID,
		ChannelID:    child.ChannelID,
		Role:         "researcher",
		Content:      message.Content,
		TrajectoryID: parent.RunID,
		CreatedAt:    findingAt,
	}
	if _, created, err := rt.store.DispatchResearchFinding(context.Background(), finding, message, delivery); err != nil {
		t.Fatalf("dispatch research finding: %v", err)
	} else if created {
		rt.emitChannelMessageEvent(WithToolExecutionContext(context.Background(), child), *message, child.OwnerID)
	}

	time.Sleep(200 * time.Millisecond)
	return parent, child
}

func TestHandleTraceTrajectoryIndexOwnerScoped(t *testing.T) {
	rt, handler := testAPISetup(t)

	parent, _ := seedTraceTrajectory(t, rt)
	if _, err := rt.StartRun(context.Background(), "bob trajectory", "user-bob"); err != nil {
		t.Fatalf("start bob run: %v", err)
	}

	req := authenticatedRequest(http.MethodGet, "/api/trace/trajectories?limit=50", "", "user-alice")
	w := httptest.NewRecorder()
	handler.HandleTraceTrajectories(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp traceTrajectoryListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Trajectories) == 0 {
		t.Fatal("expected at least one trajectory")
	}
	if resp.Trajectories[0].TrajectoryID != parent.RunID {
		t.Fatalf("first trajectory_id: got %q, want %q", resp.Trajectories[0].TrajectoryID, parent.RunID)
	}
	for _, trajectory := range resp.Trajectories {
		if trajectory.Title == "" {
			t.Fatal("trajectory title should not be empty")
		}
		if trajectory.TrajectoryID == "" {
			t.Fatal("trajectory_id should not be empty")
		}
	}
}

func TestHandleTraceTrajectorySnapshotIncludesGraphAndMoments(t *testing.T) {
	rt, handler := testAPISetup(t)

	parent, child := seedTraceTrajectory(t, rt)

	req := authenticatedRequest(http.MethodGet, "/api/trace/trajectories/"+parent.RunID, "", "user-alice")
	w := httptest.NewRecorder()
	handler.HandleTraceTrajectories(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp traceTrajectorySnapshotResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Trajectory.TrajectoryID != parent.RunID {
		t.Fatalf("trajectory_id: got %q, want %q", resp.Trajectory.TrajectoryID, parent.RunID)
	}
	if len(resp.Agents) < 2 {
		t.Fatalf("agents: got %d, want at least 2", len(resp.Agents))
	}
	if len(resp.Edges) == 0 {
		t.Fatal("expected at least one delegation edge")
	}
	foundEdge := false
	for _, edge := range resp.Edges {
		if edge.FromAgentID == parent.AgentID && edge.ToAgentID == child.AgentID {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Fatalf("expected delegation edge from %s to %s", parent.AgentID, child.AgentID)
	}
	foundMessageMoment := false
	for _, moment := range resp.Moments {
		if moment.Kind == types.EventChannelMessage && strings.Contains(moment.Summary, "damp shade") {
			foundMessageMoment = true
			if moment.MessageSeq == 0 {
				t.Fatal("channel.message moment should include message_seq")
			}
		}
	}
	if !foundMessageMoment {
		t.Fatal("expected research channel.message moment")
	}
}

func TestHandleTraceMomentDetailReturnsMessageAndFindings(t *testing.T) {
	rt, handler := testAPISetup(t)

	parent, _ := seedTraceTrajectory(t, rt)

	snapshotReq := authenticatedRequest(http.MethodGet, "/api/trace/trajectories/"+parent.RunID, "", "user-alice")
	snapshotW := httptest.NewRecorder()
	handler.HandleTraceTrajectories(snapshotW, snapshotReq)
	if snapshotW.Code != http.StatusOK {
		t.Fatalf("snapshot status: got %d, want %d", snapshotW.Code, http.StatusOK)
	}
	var snapshot traceTrajectorySnapshotResponse
	if err := json.NewDecoder(snapshotW.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}

	var target traceMomentSummary
	for _, moment := range snapshot.Moments {
		if moment.Kind == types.EventChannelMessage && strings.Contains(moment.Summary, "damp shade") {
			target = moment
			break
		}
	}
	if target.MomentID == "" {
		t.Fatal("expected research channel.message moment in snapshot")
	}

	detailReq := authenticatedRequest(http.MethodGet, "/api/trace/trajectories/"+parent.RunID+"/moments/"+target.MomentID, "", "user-alice")
	detailW := httptest.NewRecorder()
	handler.HandleTraceTrajectories(detailW, detailReq)
	if detailW.Code != http.StatusOK {
		t.Fatalf("detail status: got %d, want %d", detailW.Code, http.StatusOK)
	}

	var detail traceMomentDetailResponse
	if err := json.NewDecoder(detailW.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(detail.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(detail.Messages))
	}
	if !strings.Contains(detail.Messages[0].Content, "damp shade") {
		t.Fatalf("unexpected message content: %q", detail.Messages[0].Content)
	}
	if len(detail.Findings) != 1 {
		t.Fatalf("findings: got %d, want 1", len(detail.Findings))
	}
	if detail.Findings[0].FindingID == "" {
		t.Fatal("finding_id should not be empty")
	}
	if len(detail.References.EvidenceIDs) != 1 || detail.References.EvidenceIDs[0] != "ev-moss-1" {
		t.Fatalf("unexpected evidence ids: %+v", detail.References.EvidenceIDs)
	}
}

func TestHandleTraceTrajectoryEventsStreamFiltersByTrajectory(t *testing.T) {
	rt, handler := testAPISetup(t)

	parent, child := seedTraceTrajectory(t, rt)
	otherParent, err := rt.StartRunWithMetadata(context.Background(), "Other trajectory", "user-alice", map[string]any{
		runMetadataAgentProfile: "conductor",
		runMetadataAgentRole:    "conductor",
	})
	if err != nil {
		t.Fatalf("start other trajectory: %v", err)
	}

	req := authenticatedRequest(http.MethodGet, "/api/trace/trajectories/"+parent.RunID+"/events?after_seq=0", "", "user-alice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	go handler.HandleTraceTrajectories(w, req)
	time.Sleep(50 * time.Millisecond)

	if _, err := rt.ChannelPost(WithToolExecutionContext(context.Background(), child), child.ChannelID, "researcher", "researcher", "Second moss finding"); err != nil {
		t.Fatalf("trajectory channel post: %v", err)
	}
	if _, err := rt.ChannelPost(WithToolExecutionContext(context.Background(), otherParent), otherParent.ChannelID, "conductor", "conductor", "Other trajectory noise"); err != nil {
		t.Fatalf("other trajectory channel post: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	cancel()

	body := w.Body.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	foundTarget := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev types.EventRecord
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if ev.TrajectoryID != parent.RunID {
			t.Fatalf("unexpected trajectory in stream: got %q, want %q", ev.TrajectoryID, parent.RunID)
		}
		if ev.Kind == types.EventChannelMessage {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Fatal("expected channel.message event in trajectory stream")
	}
}
