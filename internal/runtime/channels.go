package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/types"
)

// ErrChannelClosed is returned when trying to post to or wait on a closed channel.
var ErrChannelClosed = errors.New("agent channel closed")

// ChannelMessage is the go-choir adaptation of Cogent's ChannelMessage.
// It represents a message posted to an agent channel for inter-agent
// coordination. Channels support appagent and worker communication without
// going through the LLM provider loop, enabling structured coordination
// between the conductor, scheduler, and worker components.
type ChannelMessage = types.ChannelMessage

// AgentChannel is a buffered, cursor-based message stream for a single
// coordination context (keyed by channel ID). Messages
// are append-only and can be read incrementally using cursor positions.
//
// Adapted from Cogent's AgentChannel but simplified:
//   - No persistent storage (messages exist only in memory; durability
//     comes from the runtime's event persistence if needed).
//   - No separate wait channel per message (a single signaling mechanism
//     is used for waiters).
//   - Thread-safe for concurrent read and write.
type AgentChannel struct {
	mu       sync.Mutex
	messages []ChannelMessage
	closed   bool
	waiters  []chan struct{}
}

// Load appends already-persisted messages into the in-memory mirror without waking waiters.
func (c *AgentChannel) Load(messages []ChannelMessage) {
	if len(messages) == 0 {
		return
	}
	c.mu.Lock()
	c.messages = append(c.messages, cloneChannelMessages(messages)...)
	c.mu.Unlock()
}

// NewAgentChannel creates a new, open agent channel.
func NewAgentChannel() *AgentChannel {
	return &AgentChannel{}
}

// Cursor returns the current message cursor position (i.e., the number of
// messages posted so far). Callers can use this to track their read position.
func (c *AgentChannel) Cursor() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return uint64(len(c.messages))
}

// Post appends a message to the channel and wakes any goroutines waiting
// for new messages. Returns the new cursor position. Returns an error if
// the channel is closed or the message content is empty.
func (c *AgentChannel) Post(message ChannelMessage) (uint64, error) {
	if strings.TrimSpace(message.Content) == "" {
		return 0, fmt.Errorf("message content must not be empty")
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now().UTC()
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, ErrChannelClosed
	}
	c.messages = append(c.messages, message)
	cursor := uint64(len(c.messages))

	// Wake all waiters.
	for _, w := range c.waiters {
		close(w)
	}
	c.waiters = c.waiters[:0]

	c.mu.Unlock()
	return cursor, nil
}

// ReadSince returns messages starting from the given cursor position.
// Returns the messages and the new cursor position. If cursor is out of
// range, returns an error.
func (c *AgentChannel) ReadSince(cursor uint64) ([]ChannelMessage, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cursor > uint64(len(c.messages)) {
		return nil, uint64(len(c.messages)), fmt.Errorf("cursor %d out of range (max %d)", cursor, len(c.messages))
	}
	return cloneChannelMessages(c.messages[cursor:]), uint64(len(c.messages)), nil
}

// Wait blocks until new messages are available after the given cursor,
// the channel is closed, or the context is cancelled. Returns the new
// messages and cursor position.
func (c *AgentChannel) Wait(ctx context.Context, cursor uint64) ([]ChannelMessage, uint64, error) {
	for {
		c.mu.Lock()
		switch {
		case cursor > uint64(len(c.messages)):
			next := uint64(len(c.messages))
			c.mu.Unlock()
			return nil, next, fmt.Errorf("cursor %d out of range (max %d)", cursor, len(c.messages))
		case cursor < uint64(len(c.messages)):
			messages := cloneChannelMessages(c.messages[cursor:])
			next := uint64(len(c.messages))
			c.mu.Unlock()
			return messages, next, nil
		case c.closed:
			next := uint64(len(c.messages))
			c.mu.Unlock()
			return nil, next, ErrChannelClosed
		}

		// No new messages and not closed — register a waiter.
		waitCh := make(chan struct{})
		c.waiters = append(c.waiters, waitCh)
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, cursor, ctx.Err()
		case <-waitCh:
			// New message posted — loop back to check.
		}
	}
}

// Close marks the channel as closed and wakes any waiting goroutines.
// After closing, Post returns ErrChannelClosed and Wait returns
// ErrChannelClosed. It is safe to call Close multiple times.
func (c *AgentChannel) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true

	// Wake all waiters so they can observe the closure.
	for _, w := range c.waiters {
		close(w)
	}
	c.waiters = c.waiters[:0]
}

// IsClosed returns whether the channel has been closed.
func (c *AgentChannel) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// ChannelManager manages agent channels keyed by channel identifiers.
// It provides a central point for creating, looking up, and closing
// channels, enabling the runtime to coordinate between the conductor,
// scheduler, appagent, and worker components.
//
// Adapted from Cogent's ChannelManager but simplified:
//   - No persistent storage (channels are in-process only).
//   - Channel creation is implicit on first access (no explicit Open).
//   - Thread-safe for concurrent access.
type ChannelManager struct {
	mu       sync.Mutex
	channels map[string]*AgentChannel
}

// NewChannelManager creates a new, empty channel manager.
func NewChannelManager() *ChannelManager {
	return &ChannelManager{
		channels: make(map[string]*AgentChannel),
	}
}

// Channel returns (or creates) the agent channel for the given channel ID.
// Returns an error if the channel ID is empty.
func (m *ChannelManager) Channel(channelID string) (*AgentChannel, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, fmt.Errorf("channel_id must not be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if ch, ok := m.channels[channelID]; ok {
		return ch, nil
	}
	ch := NewAgentChannel()
	m.channels[channelID] = ch
	return ch, nil
}

// Close closes and removes the channel for the given channel ID.
// Returns an error if the channel ID has no channel.
func (m *ChannelManager) Close(channelID string) error {
	m.mu.Lock()
	ch, ok := m.channels[channelID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no channel for channel_id %q", channelID)
	}
	delete(m.channels, channelID)
	m.mu.Unlock()

	ch.Close()
	return nil
}

// ListChannels returns the channel IDs of all active channels.
func (m *ChannelManager) ListChannels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	ids := make([]string, 0, len(m.channels))
	for id := range m.channels {
		ids = append(ids, id)
	}
	return ids
}

// PostToChannel is a convenience method that posts a message to the in-memory
// mirror for the given channel ID, creating the channel if needed. It also emits a
// channel.message event through the provided emit function, making
// inter-agent coordination observable through the runtime event stream.
func (m *ChannelManager) PostToChannel(channelID string, message ChannelMessage, emit EventEmitFunc) (uint64, error) {
	ch, err := m.Channel(channelID)
	if err != nil {
		return 0, err
	}

	cursor, err := ch.Post(message)
	if err != nil {
		return 0, err
	}

	// Emit observable event for the channel message.
	if emit != nil {
		payload, _ := json.Marshal(map[string]any{
			"channel_id":  channelID,
			"from":        message.From,
			"role":        message.Role,
			"content_len": len(message.Content),
		})
		emit(types.EventChannelMessage, "channel", payload)
	}

	return cursor, nil
}

// --- Helpers ---

func cloneChannelMessages(messages []ChannelMessage) []ChannelMessage {
	if len(messages) == 0 {
		return []ChannelMessage{}
	}
	cloned := make([]ChannelMessage, len(messages))
	copy(cloned, messages)
	return cloned
}

// --- Channel-aware Runtime integration ---

func (rt *Runtime) hydrateChannel(ctx context.Context, channelID string, ch *AgentChannel) error {
	messages, err := rt.store.ListChannelMessages(ctx, stringFromToolContext(ctx, toolCtxOwnerID), channelID, int64(ch.Cursor()), 500)
	if err != nil {
		return err
	}
	ch.Load(messages)
	return nil
}

// ChannelPost posts a broadcast message to the runtime's channel manager and
// emits a corresponding event. Broadcast channel posts remain useful as the
// audit / trace log surface and for internal parent-child status reporting, but
// agent-facing addressed delivery should use ChannelCast.
func (rt *Runtime) ChannelPost(ctx context.Context, channelID, from, role, content string) (uint64, error) {
	return rt.ChannelCast(ctx, channelID, "", "", from, role, content)
}

// ChannelCast posts an addressed message to the channel log, enqueues a
// delivery for the target agent, and emits the corresponding event.
func (rt *Runtime) ChannelCast(ctx context.Context, channelID, toAgentID, toRunID, from, role, content string) (uint64, error) {
	ch, err := rt.channelMgr.Channel(channelID)
	if err != nil {
		return 0, err
	}
	trajectoryID := ""
	if runRec, _ := ctx.Value(toolCtxRunRecord).(*types.RunRecord); runRec != nil && runRec.Metadata != nil {
		if id, _ := runRec.Metadata[runMetadataTrajectoryID].(string); strings.TrimSpace(id) != "" {
			trajectoryID = strings.TrimSpace(id)
		}
	}
	message := ChannelMessage{
		ChannelID:    channelID,
		FromAgentID:  stringFromToolContext(ctx, toolCtxAgentID),
		FromRunID:    stringFromToolContext(ctx, toolCtxRunID),
		ToAgentID:    strings.TrimSpace(toAgentID),
		ToRunID:      strings.TrimSpace(toRunID),
		TrajectoryID: trajectoryID,
		From:         from,
		Role:         role,
		Content:      content,
		Timestamp:    time.Now().UTC(),
	}
	ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
	if ownerID == "" && message.FromRunID != "" {
		if rec, err := rt.store.GetRun(context.Background(), message.FromRunID); err == nil {
			ownerID = rec.OwnerID
		}
	}
	if err := rt.store.AppendChannelMessage(ctx, &message, ownerID); err != nil {
		return 0, err
	}
	if message.ToAgentID != "" {
		if err := rt.store.EnqueueInboxDelivery(ctx, types.InboxDelivery{
			DeliveryID:   uuid.New().String(),
			OwnerID:      ownerID,
			ToAgentID:    message.ToAgentID,
			ToRunID:      message.ToRunID,
			FromAgentID:  message.FromAgentID,
			FromRunID:    message.FromRunID,
			ChannelID:    message.ChannelID,
			Role:         message.Role,
			Content:      message.Content,
			TrajectoryID: message.TrajectoryID,
			CreatedAt:    message.Timestamp,
		}); err != nil {
			return 0, err
		}
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		evRec := &types.EventRecord{
			EventID:   uuid.New().String(),
			RunID:     message.FromRunID,
			AgentID:   message.FromAgentID,
			ChannelID: channelID,
			OwnerID:   ownerID,
			Timestamp: time.Now().UTC(),
			Kind:      kind,
			Payload:   payload,
		}
		if err := rt.store.AppendEvent(ctx, evRec); err != nil {
			log.Printf("runtime: persist channel event: %v", err)
		}
		rt.bus.Publish(events.RuntimeEvent{
			Record: *evRec,
			Actor:  events.ActorChannel,
			Cause:  events.CauseChannelMessage,
		})
	}

	if _, err := ch.Post(message); err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]any{
		"channel_id":    channelID,
		"from":          message.From,
		"to_agent_id":   message.ToAgentID,
		"to_loop_id":    message.ToRunID,
		"trajectory_id": message.TrajectoryID,
		"role":          message.Role,
		"content_len":   len(message.Content),
	})
	emit(types.EventChannelMessage, "channel", payload)
	rt.maybeWakeVTextOnWorkerMessage(context.Background(), ownerID, message)
	return uint64(message.Seq), nil
}

// ChannelRead reads messages from the channel for the given channel ID since
// the provided cursor position. Returns the messages and the new cursor.
func (rt *Runtime) ChannelRead(channelID string, cursor uint64) ([]ChannelMessage, uint64, error) {
	ch, err := rt.channelMgr.Channel(channelID)
	if err != nil {
		return nil, cursor, err
	}
	if err := rt.hydrateChannel(context.Background(), channelID, ch); err != nil {
		return nil, cursor, err
	}
	return ch.ReadSince(cursor)
}

// ChannelWait waits for new messages on the channel for the given channel ID
// after the provided cursor position.
func (rt *Runtime) ChannelWait(ctx context.Context, channelID string, cursor uint64) ([]ChannelMessage, uint64, error) {
	ch, err := rt.channelMgr.Channel(channelID)
	if err != nil {
		return nil, cursor, err
	}
	if err := rt.hydrateChannel(ctx, channelID, ch); err != nil {
		return nil, cursor, err
	}
	return ch.Wait(ctx, cursor)
}

// --- Parent-Child Channel Helpers ---

// ensureParentChildChannels creates channels for both the parent and child
// run IDs, enabling immediate bidirectional communication. The parent
// channel is keyed by the parent run ID, and the child channel is keyed
// by the child run ID. Children post results to the parent's channel,
// and the parent can wait/read from either channel.
//
// This is called automatically during StartChildRun to ensure channels are
// available without explicit setup.
func (m *ChannelManager) ensureParentChildChannels(parentID, childID string) error {
	if _, err := m.Channel(parentID); err != nil {
		return fmt.Errorf("ensure parent channel: %w", err)
	}
	if _, err := m.Channel(childID); err != nil {
		return fmt.Errorf("ensure child channel: %w", err)
	}
	return nil
}

// PostChildResult is a convenience method that posts a result message from
// a child run to its parent's channel. The message is tagged with
// role="result" and the child's run ID as the sender. This is the primary
// way for child workers to report completion to their parent
// (VAL-CHOIR-006).
func (rt *Runtime) PostChildResult(ctx context.Context, parentChannelID, childRunID, result string) (uint64, error) {
	if stringFromToolContext(ctx, toolCtxRunID) == "" && strings.TrimSpace(childRunID) != "" {
		ctx = context.WithValue(ctx, toolCtxRunID, childRunID)
	}
	return rt.ChannelPost(ctx, parentChannelID, childRunID, "result", result)
}

// PostChildError is a convenience method that posts an error message from
// a child run to its parent's channel. The message is tagged with
// role="error" and the child's run ID as the sender. This enables parents
// to receive error notifications from failed children (VAL-CHOIR-009).
func (rt *Runtime) PostChildError(ctx context.Context, parentChannelID, childRunID, errMsg string) (uint64, error) {
	if stringFromToolContext(ctx, toolCtxRunID) == "" && strings.TrimSpace(childRunID) != "" {
		ctx = context.WithValue(ctx, toolCtxRunID, childRunID)
	}
	return rt.ChannelPost(ctx, parentChannelID, childRunID, "error", errMsg)
}

// PostChildProgress is a convenience method that posts a progress message
// from a child run to its parent's channel. The message is tagged with
// role="status" and the child's run ID as the sender. This enables parents
// to track child progress (VAL-CHOIR-011).
func (rt *Runtime) PostChildProgress(ctx context.Context, parentChannelID, childRunID, progress string) (uint64, error) {
	if stringFromToolContext(ctx, toolCtxRunID) == "" && strings.TrimSpace(childRunID) != "" {
		ctx = context.WithValue(ctx, toolCtxRunID, childRunID)
	}
	return rt.ChannelPost(ctx, parentChannelID, childRunID, "status", progress)
}

// WaitForChildResult waits for messages from a specific child on the parent's
// channel. It filters messages to return only those from the specified childID
// with the given role (e.g., "result", "error", "status").
func (rt *Runtime) WaitForChildResult(ctx context.Context, parentID, childID, role string) ([]ChannelMessage, uint64, error) {
	// First check for existing messages from this child.
	ch, err := rt.channelMgr.Channel(parentID)
	if err != nil {
		return nil, 0, err
	}

	// Scan existing messages for matching child+role.
	msgs, cursor, err := ch.ReadSince(0)
	if err != nil {
		return nil, 0, err
	}

	filtered := filterMessages(msgs, childID, role)
	if len(filtered) > 0 {
		return filtered, cursor, nil
	}

	// No matching messages yet — wait for new ones.
	for {
		newMsgs, newCursor, err := ch.Wait(ctx, cursor)
		if err != nil {
			return nil, cursor, err
		}
		cursor = newCursor

		filtered = filterMessages(newMsgs, childID, role)
		if len(filtered) > 0 {
			return filtered, cursor, nil
		}
		// Keep waiting if we got messages but not from the right child/role.
	}
}

// filterMessages filters channel messages by sender and role.
func filterMessages(msgs []ChannelMessage, from, role string) []ChannelMessage {
	var result []ChannelMessage
	for _, m := range msgs {
		if m.From == from && (role == "" || m.Role == role) {
			result = append(result, m)
		}
	}
	return result
}
