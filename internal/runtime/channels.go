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
// coordination context (typically keyed by work ID or task ID). Messages
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

// ChannelManager manages agent channels keyed by work/task identifiers.
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

// Channel returns (or creates) the agent channel for the given work ID.
// Returns an error if the work ID is empty.
func (m *ChannelManager) Channel(workID string) (*AgentChannel, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, fmt.Errorf("work_id must not be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if ch, ok := m.channels[workID]; ok {
		return ch, nil
	}
	ch := NewAgentChannel()
	m.channels[workID] = ch
	return ch, nil
}

// Close closes and removes the channel for the given work ID.
// Returns an error if the work ID has no channel.
func (m *ChannelManager) Close(workID string) error {
	m.mu.Lock()
	ch, ok := m.channels[workID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no channel for work_id %q", workID)
	}
	delete(m.channels, workID)
	m.mu.Unlock()

	ch.Close()
	return nil
}

// ListChannels returns the work IDs of all active channels.
func (m *ChannelManager) ListChannels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	ids := make([]string, 0, len(m.channels))
	for id := range m.channels {
		ids = append(ids, id)
	}
	return ids
}

// PostToChannel is a convenience method that posts a message to the channel
// for the given work ID, creating the channel if needed. It also emits a
// channel.message event through the provided emit function, making
// inter-agent coordination observable through the runtime event stream.
func (m *ChannelManager) PostToChannel(workID string, message ChannelMessage, emit EventEmitFunc) (uint64, error) {
	ch, err := m.Channel(workID)
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
			"work_id":   workID,
			"from":      message.From,
			"role":      message.Role,
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

// ChannelPost posts a message to the runtime's channel manager and emits
// a corresponding event. This is the primary way for runtime components
// (conductor, scheduler, workers) to send coordination messages that
// are observable through the event stream.
func (rt *Runtime) ChannelPost(ctx context.Context, workID, from, role, content string) (uint64, error) {
	message := ChannelMessage{
		From:      from,
		Role:      role,
		Content:   content,
		Timestamp: time.Now().UTC(),
	}

	emit := func(kind types.EventKind, phase string, payload json.RawMessage) {
		evRec := &types.EventRecord{
			EventID:   uuid.New().String(),
			TaskID:    workID,
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

	return rt.channelMgr.PostToChannel(workID, message, emit)
}

// ChannelRead reads messages from the channel for the given work ID since
// the provided cursor position. Returns the messages and the new cursor.
func (rt *Runtime) ChannelRead(workID string, cursor uint64) ([]ChannelMessage, uint64, error) {
	ch, err := rt.channelMgr.Channel(workID)
	if err != nil {
		return nil, cursor, err
	}
	return ch.ReadSince(cursor)
}

// ChannelWait waits for new messages on the channel for the given work ID
// after the provided cursor position.
func (rt *Runtime) ChannelWait(ctx context.Context, workID string, cursor uint64) ([]ChannelMessage, uint64, error) {
	ch, err := rt.channelMgr.Channel(workID)
	if err != nil {
		return nil, cursor, err
	}
	return ch.Wait(ctx, cursor)
}
