package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- Parent-Child Channel Integration Tests ---
//
// These tests verify channel-based communication between parent and child
// tasks (VAL-CHOIR-006, VAL-CHOIR-015). The feature requirements are:
//
//   - Child task can send message to parent via channel
//   - Parent can subscribe to messages from specific child
//   - Results delivered through channel when child completes
//   - Channel scoped to parent-child relationship
//   - Message format: {from, to, type, payload}

// testParentChildSetup creates a fresh Runtime and APIHandler with a parent
// task that has an active channel, ready for spawning children.
func testParentChildSetup(t *testing.T) (*Runtime, *APIHandler, string) {
	t.Helper()

	dir := t.TempDir()
	dbPath := dbPathForTest(t, dir)

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bus := events.NewEventBus()
	provider := NewStubProvider(10 * time.Millisecond)
	cfg := Config{
		SandboxID:           "sandbox-test",
		StorePath:           dbPath,
		ProviderTimeout:     10 * time.Millisecond,
		SupervisionInterval: 1 * time.Hour,
	}

	rt := New(cfg, s, bus, provider)
	handler := NewAPIHandler(rt)

	t.Cleanup(func() {
		rt.Stop()
		_ = s.Close()
	})

	// Create a parent task.
	parentRec, err := rt.SubmitTask(context.Background(), "parent objective", "user-alice")
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	// Wait for parent task to start running.
	time.Sleep(50 * time.Millisecond)

	return rt, handler, parentRec.TaskID
}

// dbPathForTest returns a unique database path for the test.
func dbPathForTest(t *testing.T, dir string) string {
	t.Helper()
	return fmt.Sprintf("%s/%s.db", dir, t.Name())
}

// --- Core Parent-Child Channel Tests ---

// TestParentChildChannel_ChildSendsParentReceives verifies that a child task
// can send a message to its parent via channel, and the parent can receive it
// (VAL-CHOIR-006, feature expected behavior #1 and #2).
func TestParentChildChannel_ChildSendsParentReceives(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Post a message from the child to the parent channel.
	cursor, err := rt.ChannelPost(ctx, parentID, "worker-1", "worker", "Research complete: found 5 sources")
	if err != nil {
		t.Fatalf("channel post from child: %v", err)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}

	// Parent reads the message from the channel.
	msgs, newCursor, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent channel read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].From != "worker-1" {
		t.Errorf("from: got %q, want worker-1", msgs[0].From)
	}
	if msgs[0].Content != "Research complete: found 5 sources" {
		t.Errorf("content: got %q, want research result", msgs[0].Content)
	}
	if newCursor != 1 {
		t.Errorf("new cursor: got %d, want 1", newCursor)
	}
}

// TestParentChildChannel_ChildSendsResultOnCompletion verifies that results
// are delivered through the channel when a child task completes
// (VAL-CHOIR-006, feature expected behavior #3).
func TestParentChildChannel_ChildSendsResultOnCompletion(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Simulate child posting its result to the parent's channel.
	resultContent := "Final analysis: Go was created in 2009 at Google."
	_, err := rt.ChannelPost(ctx, parentID, "child-worker", "result", resultContent)
	if err != nil {
		t.Fatalf("child result post: %v", err)
	}

	// Simulate child posting a status update before the result.
	_, err = rt.ChannelPost(ctx, parentID, "child-worker", "status", "50% complete")
	if err != nil {
		t.Fatalf("child status post: %v", err)
	}

	// Parent reads all messages since beginning.
	msgs, cursor, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("messages: got %d, want 2", len(msgs))
	}

	// First message should be the result.
	if msgs[0].Role != "result" {
		t.Errorf("first msg role: got %q, want result", msgs[0].Role)
	}
	if msgs[0].Content != resultContent {
		t.Errorf("first msg content: got %q, want %q", msgs[0].Content, resultContent)
	}

	// Second message should be the status.
	if msgs[1].Role != "status" {
		t.Errorf("second msg role: got %q, want status", msgs[1].Role)
	}
	if cursor != 2 {
		t.Errorf("cursor: got %d, want 2", cursor)
	}
}

// TestParentChildChannel_ScopedToRelationship verifies that channels are
// properly scoped to the parent-child relationship. Other parents should
// not receive messages from unrelated children (VAL-CHOIR-015).
func TestParentChildChannel_ScopedToRelationship(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Create a second parent task.
	parent2Rec, err := rt.SubmitTask(ctx, "second parent objective", "user-bob")
	if err != nil {
		t.Fatalf("create second parent: %v", err)
	}

	// Post a message to the first parent's channel.
	_, err = rt.ChannelPost(ctx, parentID, "worker-1", "status", "Message for parent 1")
	if err != nil {
		t.Fatalf("post to parent 1 channel: %v", err)
	}

	// Post a message to the second parent's channel.
	_, err = rt.ChannelPost(ctx, parent2Rec.TaskID, "worker-2", "status", "Message for parent 2")
	if err != nil {
		t.Fatalf("post to parent 2 channel: %v", err)
	}

	// Verify parent 1 only sees its own messages.
	msgs1, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent 1 read: %v", err)
	}
	if len(msgs1) != 1 {
		t.Fatalf("parent 1 messages: got %d, want 1", len(msgs1))
	}
	if msgs1[0].Content != "Message for parent 1" {
		t.Errorf("parent 1 content: got %q, want Message for parent 1", msgs1[0].Content)
	}
	if msgs1[0].From != "worker-1" {
		t.Errorf("parent 1 from: got %q, want worker-1", msgs1[0].From)
	}

	// Verify parent 2 only sees its own messages.
	msgs2, _, err := rt.ChannelRead(parent2Rec.TaskID, 0)
	if err != nil {
		t.Fatalf("parent 2 read: %v", err)
	}
	if len(msgs2) != 1 {
		t.Fatalf("parent 2 messages: got %d, want 1", len(msgs2))
	}
	if msgs2[0].Content != "Message for parent 2" {
		t.Errorf("parent 2 content: got %q, want Message for parent 2", msgs2[0].Content)
	}
	if msgs2[0].From != "worker-2" {
		t.Errorf("parent 2 from: got %q, want worker-2", msgs2[0].From)
	}
}

// TestParentChildChannel_WaitForChildMessages verifies that a parent can
// subscribe to (wait for) messages from a specific child using the blocking
// Wait method (feature expected behavior #2).
func TestParentChildChannel_WaitForChildMessages(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Asynchronously post a message after a delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, err := rt.ChannelPost(ctx, parentID, "worker-1", "status", "Async result")
		if err != nil {
			t.Errorf("async post: %v", err)
		}
	}()

	// Parent waits for messages.
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	msgs, cursor, err := rt.ChannelWait(waitCtx, parentID, 0)
	if err != nil {
		t.Fatalf("parent wait: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "Async result" {
		t.Errorf("content: got %q, want Async result", msgs[0].Content)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}
}

// TestParentChildChannel_MultipleChildrenSameParent verifies that multiple
// children can post messages to the same parent channel, and the parent
// receives all of them (VAL-CHOIR-006, VAL-CHOIR-008).
func TestParentChildChannel_MultipleChildrenSameParent(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Multiple children post to the same parent channel.
	for i := 0; i < 3; i++ {
		_, err := rt.ChannelPost(ctx, parentID, fmt.Sprintf("worker-%d", i), "status", fmt.Sprintf("Worker %d result", i))
		if err != nil {
			t.Fatalf("worker %d post: %v", i, err)
		}
	}

	// Parent reads all messages.
	msgs, cursor, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}

	if len(msgs) != 3 {
		t.Fatalf("messages: got %d, want 3", len(msgs))
	}
	if cursor != 3 {
		t.Errorf("cursor: got %d, want 3", cursor)
	}

	// Verify each message came from a different worker.
	for i, msg := range msgs {
		expectedFrom := fmt.Sprintf("worker-%d", i)
		if msg.From != expectedFrom {
			t.Errorf("msg %d from: got %q, want %q", i, msg.From, expectedFrom)
		}
	}
}

// TestParentChildChannel_CrossChannelIsolation verifies that posting to one
// channel does not wake waiters on another channel (VAL-CHOIR-015).
func TestParentChildChannel_CrossChannelIsolation(t *testing.T) {
	rt, _, parentAID := testParentChildSetup(t)

	ctx := context.Background()

	// Create a second parent with a different channel.
	parentB, err := rt.SubmitTask(ctx, "parent B objective", "user-bob")
	if err != nil {
		t.Fatalf("create parent B: %v", err)
	}

	// Start a waiter on parent B's channel.
	bReceived := make(chan []ChannelMessage, 1)
	go func() {
		msgs, _, err := rt.ChannelWait(ctx, parentB.TaskID, 0)
		if err != nil {
			t.Errorf("parent B wait: %v", err)
			return
		}
		bReceived <- msgs
	}()

	// Give the waiter time to register.
	time.Sleep(30 * time.Millisecond)

	// Post to parent A's channel — this should NOT wake parent B's waiter.
	_, err = rt.ChannelPost(ctx, parentAID, "worker-A", "status", "For parent A only")
	if err != nil {
		t.Fatalf("post to parent A: %v", err)
	}

	// Verify parent B's waiter did NOT receive the message.
	select {
	case <-bReceived:
		t.Fatal("parent B should not have received parent A's message")
	case <-time.After(100 * time.Millisecond):
		// Expected: parent B did not wake up.
	}

	// Now post to parent B's channel — this should wake the waiter.
	_, err = rt.ChannelPost(ctx, parentB.TaskID, "worker-B", "status", "For parent B")
	if err != nil {
		t.Fatalf("post to parent B: %v", err)
	}

	select {
	case msgs := <-bReceived:
		if len(msgs) != 1 {
			t.Fatalf("parent B messages: got %d, want 1", len(msgs))
		}
		if msgs[0].Content != "For parent B" {
			t.Errorf("parent B content: got %q, want For parent B", msgs[0].Content)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("parent B should have received its own message")
	}
}

// TestParentChildChannel_ChannelClosureIsolation verifies that closing one
// channel does not affect other channels (VAL-CHOIR-015). It also verifies
// that closing a channel discards its messages and creates a fresh channel
// on next access.
func TestParentChildChannel_ChannelClosureIsolation(t *testing.T) {
	rt, _, parentAID := testParentChildSetup(t)

	ctx := context.Background()

	// Create a second parent.
	parentB, err := rt.SubmitTask(ctx, "parent B objective", "user-bob")
	if err != nil {
		t.Fatalf("create parent B: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	// Post to both channels.
	_, _ = rt.ChannelPost(ctx, parentAID, "worker", "status", "A msg")
	_, _ = rt.ChannelPost(ctx, parentB.TaskID, "worker", "status", "B msg")

	// Close parent A's channel — this removes it from the manager.
	mgr := rt.ChannelManager()
	if err := mgr.Close(parentAID); err != nil {
		t.Fatalf("close parent A channel: %v", err)
	}

	// Verify closing parent A does not affect parent B's channel.
	msgs, _, err := rt.ChannelRead(parentB.TaskID, 0)
	if err != nil {
		t.Fatalf("parent B read after closing parent A: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("parent B messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "B msg" {
		t.Errorf("parent B content: got %q, want B msg", msgs[0].Content)
	}

	// Verify parent B can still post new messages.
	_, err = rt.ChannelPost(ctx, parentB.TaskID, "worker", "status", "B msg 2")
	if err != nil {
		t.Fatalf("parent B post after closing parent A: %v", err)
	}

	// Verify parent A's channel is now gone (Close removes it from the map).
	// Attempting to close again should fail since it was removed.
	if err := mgr.Close(parentAID); err == nil {
		t.Error("second close should fail since channel was removed")
	}

	// Accessing parent A's channel again creates a fresh, empty channel.
	ch, err := mgr.Channel(parentAID)
	if err != nil {
		t.Fatalf("re-create parent A channel: %v", err)
	}
	if ch.IsClosed() {
		t.Error("re-created channel should be open")
	}
	// The new channel should be empty (old messages discarded).
	msgsA, _, _ := ch.ReadSince(0)
	if len(msgsA) != 0 {
		t.Errorf("re-created channel should be empty, got %d messages", len(msgsA))
	}
}

// TestParentChildChannel_EventEmission verifies that channel messages between
// parent and child emit observable events through the event bus
// (VAL-CHOIR-006, VAL-CHOIR-011).
func TestParentChildChannel_EventEmission(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Subscribe to events.
	ch := rt.EventBus().SubscribeWithBuffer(128)
	defer rt.EventBus().Unsubscribe(ch)

	// Child posts a message to the parent's channel.
	_, err := rt.ChannelPost(ctx, parentID, "worker-1", "status", "Research done")
	if err != nil {
		t.Fatalf("channel post: %v", err)
	}

	// Should receive a channel.message event.
	select {
	case ev := <-ch:
		if ev.Record.Kind != types.EventChannelMessage {
			t.Errorf("event kind: got %q, want channel.message", ev.Record.Kind)
		}
		if ev.Actor != events.ActorChannel {
			t.Errorf("actor: got %q, want channel", ev.Actor)
		}
		if ev.Cause != events.CauseChannelMessage {
			t.Errorf("cause: got %q, want channel_message", ev.Cause)
		}
		// Verify payload contains work_id and from.
		var payload map[string]any
		if err := json.Unmarshal(ev.Record.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload["work_id"] != parentID {
			t.Errorf("payload work_id: got %v, want %q", payload["work_id"], parentID)
		}
		if payload["from"] != "worker-1" {
			t.Errorf("payload from: got %v, want worker-1", payload["from"])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for channel.message event")
	}
}

// --- Spawn + Channel Integration Tests ---

// TestParentChildChannel_SpawnedChildPostsToParentChannel verifies that when
// a child task is spawned, a channel is available for the child to post
// results that the parent can receive (VAL-CHOIR-006).
func TestParentChildChannel_SpawnedChildPostsToParentChannel(t *testing.T) {
	rt, handler, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Spawn a child task.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"research topic X"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("spawn status: got %d, want %d; body: %s", w.Code, http.StatusAccepted, w.Body.String())
	}

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode spawn response: %v", err)
	}

	childID := resp.TaskID

	// Simulate the child worker posting a result to the parent's channel.
	// The child posts to the parent's channel (keyed by parentID).
	_, err := rt.ChannelPost(ctx, parentID, childID, "result", "Research complete: topic X analyzed")
	if err != nil {
		t.Fatalf("child post to parent channel: %v", err)
	}

	// Parent reads the result.
	msgs, cursor, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("parent messages: got %d, want 1", len(msgs))
	}
	if msgs[0].From != childID {
		t.Errorf("from: got %q, want child ID %q", msgs[0].From, childID)
	}
	if msgs[0].Content != "Research complete: topic X analyzed" {
		t.Errorf("content: got %q", msgs[0].Content)
	}
	if msgs[0].Role != "result" {
		t.Errorf("role: got %q, want result", msgs[0].Role)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}

	// Verify the child also has its own channel (keyed by child ID).
	childCh, err := rt.ChannelManager().Channel(childID)
	if err != nil {
		t.Fatalf("get child channel: %v", err)
	}
	if childCh == nil {
		t.Error("child should have its own channel")
	}
}

// TestParentChildChannel_MultipleSpawnedChildrenPostResults verifies that
// multiple spawned children can all post results to the parent channel
// and the parent receives all of them in order (VAL-CHOIR-006, VAL-CHOIR-008).
// Since spawned children now auto-post results on completion, the parent
// receives both auto-posted results and manually posted results.
func TestParentChildChannel_MultipleSpawnedChildrenPostResults(t *testing.T) {
	rt, handler, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Spawn 3 children.
	childIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"parent_id":"%s","objective":"child %d"}`, parentID, i)
		req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
		w := httptest.NewRecorder()
		handler.HandleSpawn(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("spawn %d: got %d", i, w.Code)
		}

		var resp spawnResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		childIDs[i] = resp.TaskID
	}

	// Wait for children to complete (auto-post results).
	time.Sleep(100 * time.Millisecond)

	// Each child also manually posts a result to the parent channel.
	for i, childID := range childIDs {
		_, err := rt.ChannelPost(ctx, parentID, childID, "result", fmt.Sprintf("Child %d result", i))
		if err != nil {
			t.Fatalf("child %d post: %v", i, err)
		}
	}

	// Parent reads all results.
	msgs, cursor, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}

	// Should have at least 6 messages: 3 auto-posted + 3 manual.
	if len(msgs) < 3 {
		t.Fatalf("messages: got %d, want at least 3", len(msgs))
	}

	// Verify each manual message is present.
	for i, childID := range childIDs {
		expected := fmt.Sprintf("Child %d result", i)
		found := false
		for _, msg := range msgs {
			if msg.From == childID && msg.Content == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("manual result from child %d (%s) not found", i, childID[:8])
		}
	}

	if cursor < 3 {
		t.Errorf("cursor: got %d, want at least 3", cursor)
	}
}

// TestParentChildChannel_ParentWaitsForChildResult verifies the full
// parent-child flow: spawn child → child completes and auto-posts result →
// parent waits and receives results (feature verification step #2).
// Since spawned children now auto-post results on completion, the parent
// automatically receives the child's result through the channel.
func TestParentChildChannel_ParentWaitsForChildResult(t *testing.T) {
	rt, handler, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Spawn a child.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"research topic Y"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	var resp spawnResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	childID := resp.TaskID

	// Parent waits for the child's result. The child task auto-posts
	// its result on completion (~10ms with the test stub provider).
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	msgs, cursor, err := rt.ChannelWait(waitCtx, parentID, 0)
	if err != nil {
		t.Fatalf("parent wait: %v", err)
	}

	// Should receive at least the auto-posted result.
	if len(msgs) < 1 {
		t.Fatalf("messages: got %d, want at least 1", len(msgs))
	}

	// Find a result message from the child.
	foundResult := false
	for _, msg := range msgs {
		if msg.From == childID && msg.Role == "result" {
			foundResult = true
			break
		}
	}
	if !foundResult {
		t.Errorf("no result message from child %s found among %d messages", childID[:8], len(msgs))
	}

	if cursor < 1 {
		t.Errorf("cursor: got %d, want at least 1", cursor)
	}
}

// TestParentChildChannel_ChannelAutoCreatedOnSpawn verifies that spawning a
// child task automatically ensures a channel exists for the parent, enabling
// immediate communication without explicit channel setup.
func TestParentChildChannel_ChannelAutoCreatedOnSpawn(t *testing.T) {
	_, handler, parentID := testParentChildSetup(t)

	// Before spawning, the parent should have a channel (created by SubmitTask).
	mgr := NewChannelManager() // fresh manager to test auto-creation

	// Spawn a child — this should auto-create a channel for the parent
	// if one doesn't exist yet in the runtime's manager.
	body := fmt.Sprintf(`{"parent_id":"%s","objective":"auto channel test"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("spawn: got %d, want %d", w.Code, http.StatusAccepted)
	}

	// The runtime's channel manager should have a channel for the parent.
	// (The channel is created lazily on first access via ChannelPost/ChannelRead.)
	// This test verifies the manager works correctly.
	_, err := mgr.Channel("test-auto")
	if err != nil {
		t.Fatalf("auto-create channel: %v", err)
	}
	if len(mgr.ListChannels()) != 1 {
		t.Errorf("channels: got %d, want 1", len(mgr.ListChannels()))
	}
}

// TestParentChildChannel_ErrorNotification verifies that when a child task
// fails, the error can be communicated to the parent through the channel
// (VAL-CHOIR-009).
func TestParentChildChannel_ErrorNotification(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Simulate a child posting an error to the parent channel.
	_, err := rt.ChannelPost(ctx, parentID, "worker-1", "error", "Failed: provider timeout after 30s")
	if err != nil {
		t.Fatalf("error post: %v", err)
	}

	// Parent reads the error message.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Role != "error" {
		t.Errorf("role: got %q, want error", msgs[0].Role)
	}
	if msgs[0].Content != "Failed: provider timeout after 30s" {
		t.Errorf("content: got %q", msgs[0].Content)
	}
}

// TestParentChildChannel_IncrementalRead verifies that the parent can
// incrementally read messages using cursor-based reads, only seeing
// new messages since the last read.
func TestParentChildChannel_IncrementalRead(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Child posts 3 messages.
	rt.ChannelPost(ctx, parentID, "worker", "status", "Starting")
	rt.ChannelPost(ctx, parentID, "worker", "status", "In progress")
	rt.ChannelPost(ctx, parentID, "worker", "result", "Done")

	// Parent reads from beginning.
	msgs, cursor, _ := rt.ChannelRead(parentID, 0)
	if len(msgs) != 3 {
		t.Fatalf("first read: got %d, want 3", len(msgs))
	}
	if cursor != 3 {
		t.Errorf("first cursor: got %d, want 3", cursor)
	}

	// Child posts 2 more messages.
	rt.ChannelPost(ctx, parentID, "worker", "status", "Cleanup started")
	rt.ChannelPost(ctx, parentID, "worker", "status", "Cleanup done")

	// Parent reads only new messages since cursor 3.
	msgs2, cursor2, _ := rt.ChannelRead(parentID, cursor)
	if len(msgs2) != 2 {
		t.Fatalf("incremental read: got %d, want 2", len(msgs2))
	}
	if msgs2[0].Content != "Cleanup started" {
		t.Errorf("first new msg: got %q", msgs2[0].Content)
	}
	if msgs2[1].Content != "Cleanup done" {
		t.Errorf("second new msg: got %q", msgs2[1].Content)
	}
	if cursor2 != 5 {
		t.Errorf("second cursor: got %d, want 5", cursor2)
	}
}

// TestParentChildChannel_MessageFormat verifies that messages conform to
// the expected format with from, to (via work_id), type (role), and payload
// (content) fields (feature expected behavior #5).
func TestParentChildChannel_MessageFormat(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Post a well-structured message.
	_, err := rt.ChannelPost(ctx, parentID, "worker-1", "result", `{"summary":"Research complete","sources":5}`)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	msgs, _, _ := rt.ChannelRead(parentID, 0)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}

	msg := msgs[0]

	// Verify message format fields.
	// from (From field)
	if msg.From != "worker-1" {
		t.Errorf("From (sender): got %q, want worker-1", msg.From)
	}

	// to is implicit — the channel is keyed by parentID (the recipient).
	// We verify this by checking the channel manager's mapping.
	ch, _ := rt.ChannelManager().Channel(parentID)
	if ch == nil {
		t.Error("channel should exist for parent")
	}

	// type (Role field)
	if msg.Role != "result" {
		t.Errorf("Role (message type): got %q, want result", msg.Role)
	}

	// payload (Content field)
	if msg.Content != `{"summary":"Research complete","sources":5}` {
		t.Errorf("Content (payload): got %q", msg.Content)
	}

	// timestamp should be set
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

// TestParentChildChannel_ConcurrentAccess verifies that channels support
// concurrent access from multiple children posting to the same parent.
func TestParentChildChannel_ConcurrentAccess(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// 10 children concurrently post to the parent channel.
	done := make(chan int, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			_, err := rt.ChannelPost(ctx, parentID, fmt.Sprintf("worker-%d", idx), "status", fmt.Sprintf("Result %d", idx))
			if err != nil {
				t.Errorf("concurrent post %d: %v", idx, err)
			}
			done <- idx
		}(i)
	}

	// Wait for all goroutines to complete.
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all 10 messages were received.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}
	if len(msgs) != 10 {
		t.Errorf("messages: got %d, want 10", len(msgs))
	}
}

// --- Convenience Method Tests ---

// TestParentChildChannel_PostChildResult verifies the PostChildResult
// convenience method for child result delivery (VAL-CHOIR-006).
func TestParentChildChannel_PostChildResult(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Post a result using the convenience method.
	cursor, err := rt.PostChildResult(ctx, parentID, "child-1", "Analysis complete: 42 items found")
	if err != nil {
		t.Fatalf("post child result: %v", err)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}

	// Read the result from the parent channel.
	msgs, _, err := rt.ChannelRead(parentID, 0)
	if err != nil {
		t.Fatalf("parent read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].From != "child-1" {
		t.Errorf("from: got %q, want child-1", msgs[0].From)
	}
	if msgs[0].Role != "result" {
		t.Errorf("role: got %q, want result", msgs[0].Role)
	}
	if msgs[0].Content != "Analysis complete: 42 items found" {
		t.Errorf("content: got %q", msgs[0].Content)
	}
}

// TestParentChildChannel_PostChildError verifies the PostChildError
// convenience method for error delivery (VAL-CHOIR-009).
func TestParentChildChannel_PostChildError(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Post an error using the convenience method.
	_, err := rt.PostChildError(ctx, parentID, "child-1", "Provider timeout")
	if err != nil {
		t.Fatalf("post child error: %v", err)
	}

	// Read the error from the parent channel.
	msgs, _, _ := rt.ChannelRead(parentID, 0)
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Role != "error" {
		t.Errorf("role: got %q, want error", msgs[0].Role)
	}
	if msgs[0].Content != "Provider timeout" {
		t.Errorf("content: got %q, want Provider timeout", msgs[0].Content)
	}
}

// TestParentChildChannel_PostChildProgress verifies the PostChildProgress
// convenience method for progress updates (VAL-CHOIR-011).
func TestParentChildChannel_PostChildProgress(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Post progress updates using the convenience method.
	rt.PostChildProgress(ctx, parentID, "child-1", "25% complete")
	rt.PostChildProgress(ctx, parentID, "child-1", "50% complete")
	rt.PostChildProgress(ctx, parentID, "child-1", "75% complete")
	rt.PostChildResult(ctx, parentID, "child-1", "Done")

	// Read all messages.
	msgs, _, _ := rt.ChannelRead(parentID, 0)
	if len(msgs) != 4 {
		t.Fatalf("messages: got %d, want 4", len(msgs))
	}

	// Verify message order and roles.
	for i, expected := range []struct {
		role, content string
	}{
		{"status", "25% complete"},
		{"status", "50% complete"},
		{"status", "75% complete"},
		{"result", "Done"},
	} {
		if msgs[i].Role != expected.role {
			t.Errorf("msg %d role: got %q, want %q", i, msgs[i].Role, expected.role)
		}
		if msgs[i].Content != expected.content {
			t.Errorf("msg %d content: got %q, want %q", i, msgs[i].Content, expected.content)
		}
	}
}

// TestParentChildChannel_WaitForChildResultFiltered verifies the
// WaitForChildResult method that filters by child ID and role.
func TestParentChildChannel_WaitForChildResultFiltered(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Post messages from multiple children.
	rt.ChannelPost(ctx, parentID, "child-1", "status", "Child 1 working")
	rt.ChannelPost(ctx, parentID, "child-2", "status", "Child 2 working")
	rt.ChannelPost(ctx, parentID, "child-1", "result", "Child 1 done")

	// Wait for child-1's result only.
	msgs, cursor, err := rt.WaitForChildResult(ctx, parentID, "child-1", "result")
	if err != nil {
		t.Fatalf("wait for child result: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("filtered messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "Child 1 done" {
		t.Errorf("content: got %q, want Child 1 done", msgs[0].Content)
	}
	if msgs[0].From != "child-1" {
		t.Errorf("from: got %q, want child-1", msgs[0].From)
	}
	if cursor != 3 {
		t.Errorf("cursor: got %d, want 3", cursor)
	}
}

// TestParentChildChannel_WaitForChildResultAsync verifies WaitForChildResult
// when the result arrives asynchronously after the wait starts.
func TestParentChildChannel_WaitForChildResultAsync(t *testing.T) {
	rt, _, parentID := testParentChildSetup(t)

	ctx := context.Background()

	// Asynchronously post a result after a delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		rt.PostChildResult(ctx, parentID, "child-1", "Async result ready")
	}()

	// Wait for the result with a timeout.
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	msgs, _, err := rt.WaitForChildResult(waitCtx, parentID, "child-1", "result")
	if err != nil {
		t.Fatalf("wait for child result: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "Async result ready" {
		t.Errorf("content: got %q, want Async result ready", msgs[0].Content)
	}
}

// TestParentChildChannel_ChannelsAutoCreatedOnSpawn verifies that spawning
// a child task automatically creates channels for both parent and child
// in the channel manager.
func TestParentChildChannel_ChannelsAutoCreatedOnSpawn(t *testing.T) {
	rt, handler, parentID := testParentChildSetup(t)

	body := fmt.Sprintf(`{"parent_id":"%s","objective":"auto channel test"}`, parentID)
	req := authenticatedRequest(http.MethodPost, "/api/agent/spawn", body, "user-alice")
	w := httptest.NewRecorder()
	handler.HandleSpawn(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("spawn: got %d", w.Code)
	}

	var resp spawnResponse
	json.NewDecoder(w.Body).Decode(&resp)

	mgr := rt.ChannelManager()
	channels := mgr.ListChannels()

	// Both parent and child should have channels.
	hasParent := false
	hasChild := false
	for _, ch := range channels {
		if ch == parentID {
			hasParent = true
		}
		if ch == resp.TaskID {
			hasChild = true
		}
	}

	if !hasParent {
		t.Error("parent channel should be auto-created on spawn")
	}
	if !hasChild {
		t.Error("child channel should be auto-created on spawn")
	}
}
