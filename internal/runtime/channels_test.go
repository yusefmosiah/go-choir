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
	"github.com/yusefmosiah/go-choir/internal/types"
)

// --- AgentChannel Tests ---

func TestAgentChannelPost(t *testing.T) {
	ch := NewAgentChannel()

	cursor, err := ch.Post(ChannelMessage{
		From:    "appagent",
		Role:    "coordinator",
		Content: "Starting document revision",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}
}

func TestAgentChannelPostEmpty(t *testing.T) {
	ch := NewAgentChannel()

	_, err := ch.Post(ChannelMessage{
		From:    "appagent",
		Content: "",
	})
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestAgentChannelPostSetsTimestamp(t *testing.T) {
	ch := NewAgentChannel()

	_, err := ch.Post(ChannelMessage{
		From:    "appagent",
		Content: "test",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	msgs, _, err := ch.ReadSince(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msgs[0].Timestamp.IsZero() {
		t.Error("timestamp should be set automatically")
	}
}

func TestAgentChannelPostClosed(t *testing.T) {
	ch := NewAgentChannel()
	ch.Close()

	_, err := ch.Post(ChannelMessage{
		From:    "appagent",
		Content: "test",
	})
	if err != ErrChannelClosed {
		t.Errorf("error: got %v, want ErrChannelClosed", err)
	}
}

func TestAgentChannelReadSince(t *testing.T) {
	ch := NewAgentChannel()

	if _, err := ch.Post(ChannelMessage{From: "a", Content: "msg1"}); err != nil {
		t.Fatalf("post1: %v", err)
	}
	if _, err := ch.Post(ChannelMessage{From: "b", Content: "msg2"}); err != nil {
		t.Fatalf("post2: %v", err)
	}
	if _, err := ch.Post(ChannelMessage{From: "c", Content: "msg3"}); err != nil {
		t.Fatalf("post3: %v", err)
	}

	// Read from beginning.
	msgs, cursor, err := ch.ReadSince(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages: got %d, want 3", len(msgs))
	}
	if cursor != 3 {
		t.Errorf("cursor: got %d, want 3", cursor)
	}

	// Read from cursor 1.
	msgs, _, err = ch.ReadSince(1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages: got %d, want 2", len(msgs))
	}
	if msgs[0].From != "b" {
		t.Errorf("first message from: got %q, want b", msgs[0].From)
	}
}

func TestAgentChannelReadSinceOutOfRange(t *testing.T) {
	ch := NewAgentChannel()

	if _, err := ch.Post(ChannelMessage{From: "a", Content: "msg1"}); err != nil {
		t.Fatalf("post: %v", err)
	}

	_, _, err := ch.ReadSince(5) // beyond the end
	if err == nil {
		t.Error("expected error for out-of-range cursor")
	}
}

func TestAgentChannelWait(t *testing.T) {
	ch := NewAgentChannel()

	// Post a message asynchronously.
	go func() {
		time.Sleep(50 * time.Millisecond)
		if _, err := ch.Post(ChannelMessage{From: "worker", Content: "result"}); err != nil {
			t.Errorf("async post: %v", err)
		}
	}()

	// Wait for messages from cursor 0.
	msgs, cursor, err := ch.Wait(context.Background(), 0)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "result" {
		t.Errorf("content: got %q, want result", msgs[0].Content)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}
}

func TestAgentChannelWaitContextCancelled(t *testing.T) {
	ch := NewAgentChannel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := ch.Wait(ctx, 0)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestAgentChannelWaitClosed(t *testing.T) {
	ch := NewAgentChannel()

	go func() {
		time.Sleep(50 * time.Millisecond)
		ch.Close()
	}()

	_, _, err := ch.Wait(context.Background(), 0)
	if err != ErrChannelClosed {
		t.Errorf("error: got %v, want ErrChannelClosed", err)
	}
}

func TestAgentChannelCursor(t *testing.T) {
	ch := NewAgentChannel()

	if ch.Cursor() != 0 {
		t.Errorf("initial cursor: got %d, want 0", ch.Cursor())
	}

	if _, err := ch.Post(ChannelMessage{From: "a", Content: "msg1"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if ch.Cursor() != 1 {
		t.Errorf("cursor after 1 post: got %d, want 1", ch.Cursor())
	}
}

func TestAgentChannelClose(t *testing.T) {
	ch := NewAgentChannel()

	ch.Close()
	if !ch.IsClosed() {
		t.Error("channel should be closed")
	}

	// Double close should not panic.
	ch.Close()
}

// --- ChannelManager Tests ---

func TestChannelManagerGetOrCreate(t *testing.T) {
	mgr := NewChannelManager()

	ch1, err := mgr.Channel("task-123")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	// Same work ID should return the same channel.
	ch2, err := mgr.Channel("task-123")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	if ch1 != ch2 {
		t.Error("expected same channel instance for same work ID")
	}
}

func TestChannelManagerEmptyWorkID(t *testing.T) {
	mgr := NewChannelManager()

	_, err := mgr.Channel("")
	if err == nil {
		t.Error("expected error for empty work ID")
	}
}

func TestChannelManagerClose(t *testing.T) {
	mgr := NewChannelManager()

	_, err := mgr.Channel("task-123")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	if err := mgr.Close("task-123"); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Channel should be gone from the manager.
	if err := mgr.Close("task-123"); err == nil {
		t.Error("expected error for closing non-existent channel")
	}
}

func TestChannelManagerListChannels(t *testing.T) {
	mgr := NewChannelManager()

	if _, err := mgr.Channel("task-1"); err != nil {
		t.Fatalf("channel task-1: %v", err)
	}
	if _, err := mgr.Channel("task-2"); err != nil {
		t.Fatalf("channel task-2: %v", err)
	}
	if _, err := mgr.Channel("task-3"); err != nil {
		t.Fatalf("channel task-3: %v", err)
	}

	ids := mgr.ListChannels()
	if len(ids) != 3 {
		t.Fatalf("channels: got %d, want 3", len(ids))
	}
}

func TestChannelManagerPostToChannel(t *testing.T) {
	mgr := NewChannelManager()

	cursor, err := mgr.PostToChannel("task-123", ChannelMessage{
		From:    "appagent",
		Role:    "coordinator",
		Content: "Starting revision",
	}, nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}

	// Verify message was posted.
	ch, err := mgr.Channel("task-123")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	msgs, _, err := ch.ReadSince(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "Starting revision" {
		t.Errorf("content: got %q, want Starting revision", msgs[0].Content)
	}
}

func TestChannelManagerPostToChannelWithEmit(t *testing.T) {
	mgr := NewChannelManager()

	var emittedKinds []types.EventKind
	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		emittedKinds = append(emittedKinds, kind)
	}

	_, err := mgr.PostToChannel("task-123", ChannelMessage{
		From:    "appagent",
		Role:    "coordinator",
		Content: "Message with emit",
	}, emit)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if len(emittedKinds) != 1 {
		t.Fatalf("emitted events: got %d, want 1", len(emittedKinds))
	}
	if emittedKinds[0] != types.EventChannelMessage {
		t.Errorf("event kind: got %q, want channel.message", emittedKinds[0])
	}
}

// --- Runtime Channel Integration Tests ---

func TestRuntimeChannelPost(t *testing.T) {
	rt, _ := testRuntime(t)

	cursor, err := rt.ChannelPost(context.Background(), "task-123", "appagent", "coordinator", "Starting work")
	if err != nil {
		t.Fatalf("channel post: %v", err)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}
}

func TestRuntimeChannelRead(t *testing.T) {
	rt, _ := testRuntime(t)

	if _, err := rt.ChannelPost(context.Background(), "task-123", "appagent", "coordinator", "Starting work"); err != nil {
		t.Fatalf("post1: %v", err)
	}
	if _, err := rt.ChannelPost(context.Background(), "task-123", "worker-1", "status", "In progress"); err != nil {
		t.Fatalf("post2: %v", err)
	}

	msgs, cursor, err := rt.ChannelRead("task-123", 0)
	if err != nil {
		t.Fatalf("channel read: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages: got %d, want 2", len(msgs))
	}
	if cursor != 2 {
		t.Errorf("cursor: got %d, want 2", cursor)
	}

	// Read only new messages.
	msgs, _, err = rt.ChannelRead("task-123", 1)
	if err != nil {
		t.Fatalf("channel read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].From != "worker-1" {
		t.Errorf("from: got %q, want worker-1", msgs[0].From)
	}
}

func TestRuntimeChannelPostEmitsEvent(t *testing.T) {
	rt, _ := testRuntime(t)

	// Subscribe to the event bus.
	ch := rt.EventBus().SubscribeWithBuffer(128)
	defer rt.EventBus().Unsubscribe(ch)

	_, err := rt.ChannelPost(context.Background(), "task-123", "appagent", "coordinator", "Channel message")
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
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel message event")
	}
}

func TestRuntimeChannelWait(t *testing.T) {
	rt, _ := testRuntime(t)

	// Post a message asynchronously.
	go func() {
		time.Sleep(50 * time.Millisecond)
		if _, err := rt.ChannelPost(context.Background(), "task-123", "worker-1", "status", "Done"); err != nil {
			t.Errorf("async post: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgs, cursor, err := rt.ChannelWait(ctx, "task-123", 0)
	if err != nil {
		t.Fatalf("channel wait: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages: got %d, want 1", len(msgs))
	}
	if msgs[0].Content != "Done" {
		t.Errorf("content: got %q, want Done", msgs[0].Content)
	}
	if cursor != 1 {
		t.Errorf("cursor: got %d, want 1", cursor)
	}
}

func TestRuntimeChannelManagerAccessor(t *testing.T) {
	rt, _ := testRuntime(t)

	mgr := rt.ChannelManager()
	if mgr == nil {
		t.Fatal("channel manager should not be nil")
	}

	// Should be the same instance used by the runtime.
	if _, err := mgr.Channel("test-work"); err != nil {
		t.Fatalf("channel: %v", err)
	}
	ids := mgr.ListChannels()
	if len(ids) != 1 {
		t.Errorf("channels: got %d, want 1", len(ids))
	}
}

func TestRuntimeToolRegistryAccessor(t *testing.T) {
	// Without tool registry.
	rt, _ := testRuntime(t)
	if rt.ToolRegistry() != nil {
		t.Error("tool registry should be nil when not configured")
	}

	// With tool registry.
	registry := NewToolRegistry()
	if err := registry.Register(Tool{
		Name: "test",
		Func: func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	dir := filepath.Join(os.TempDir(), "go-choir-m3-accessor-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, "accessor.db")
	_ = os.Remove(dbPath)

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

	rt2 := New(cfg, s, bus, provider, WithToolRegistry(registry))

	if rt2.ToolRegistry() == nil {
		t.Error("tool registry should not be nil when configured")
	}
	if rt2.ToolRegistry().Size() != 1 {
		t.Errorf("tool registry size: got %d, want 1", rt2.ToolRegistry().Size())
	}

	rt2.Stop()
	_ = s.Close()
	_ = os.Remove(dbPath)
}

func TestRuntimeWithChannelManagerOption(t *testing.T) {
	customMgr := NewChannelManager()

	dir := filepath.Join(os.TempDir(), "go-choir-m3-cmopt-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, "cmopt.db")
	_ = os.Remove(dbPath)

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

	rt := New(cfg, s, bus, provider, WithChannelManager(customMgr))

	if rt.ChannelManager() != customMgr {
		t.Error("channel manager should be the custom instance")
	}

	rt.Stop()
	_ = s.Close()
	_ = os.Remove(dbPath)
}
