package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

func RegisterCoAgentTools(registry *ToolRegistry, rt *Runtime) error {
	for _, tool := range []Tool{
		newSpawnAgentTool(rt),
		newPostMessageTool(rt),
		newReadMessagesTool(rt),
		newWaitForMessageTool(rt),
		newCloseAgentTool(rt),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newSpawnAgentTool(rt *Runtime) Tool {
	type args struct {
		Objective string `json:"objective"`
		Role      string `json:"role"`
		Profile   string `json:"profile,omitempty"`
		ChannelID string `json:"channel_id,omitempty"`
		Model     string `json:"model,omitempty"`
	}
	return Tool{
		Name:        "spawn_agent",
		Description: "Spawn a child agent run with a specific role/profile and optional shared channel.",
		Parameters: jsonSchemaObject(map[string]any{
			"objective":  map[string]any{"type": "string"},
			"role":       map[string]any{"type": "string"},
			"profile":    map[string]any{"type": "string"},
			"channel_id": map[string]any{"type": "string"},
			"model":      map[string]any{"type": "string"},
		}, []string{"objective", "role"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode spawn_agent args: %w", err)
			}
			parentID := stringFromToolContext(ctx, toolCtxRunID)
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			if parentID == "" || ownerID == "" {
				return "", fmt.Errorf("spawn_agent missing run context")
			}
			role := strings.TrimSpace(in.Role)
			if role == "" {
				return "", fmt.Errorf("role must not be empty")
			}
			callerProfile := stringFromToolContext(ctx, toolCtxProfile)
			profile := strings.TrimSpace(in.Profile)
			if profile == "" {
				profile = role
			}
			if !canDelegateTo(callerProfile, profile) {
				return "", fmt.Errorf("%s cannot delegate to %s", callerProfile, profile)
			}
			constraints := map[string]any{
				runMetadataAgentRole:    role,
				runMetadataAgentProfile: profile,
			}
			if channelID := strings.TrimSpace(in.ChannelID); channelID != "" {
				constraints[runMetadataChannelID] = channelID
			}
			if model := strings.TrimSpace(in.Model); model != "" {
				constraints[runMetadataModel] = model
			}
			if callerProfile == AgentProfileConductor && profile == AgentProfileVText {
				parentRec, _ := ctx.Value(toolCtxRunRecord).(*types.RunRecord)
				if parentRec == nil {
					parentRec = &types.RunRecord{
						RunID:        parentID,
						OwnerID:      ownerID,
						AgentProfile: callerProfile,
					}
				}
				decision, err := rt.ensureConductorVTextRoute(ctx, parentRec, in.Objective)
				if err != nil {
					return "", err
				}
				return toolResultJSON(map[string]any{
					"action":                 decision.Action,
					"app":                    decision.App,
					"title":                  decision.Title,
					"seed_prompt":            decision.SeedPrompt,
					"initial_content":        decision.InitialContent,
					"create_initial_version": decision.CreateInitialVersion != nil && *decision.CreateInitialVersion,
					"doc_id":                 decision.DocID,
					"initial_revision_id":    decision.InitialRevisionID,
					"initial_loop_id":        decision.InitialLoopID,
					"loop_id":                decision.InitialLoopID,
					"channel_id":             decision.DocID,
					"role":                   role,
					"profile":                profile,
					"state":                  types.RunPending,
				})
			}
			child, err := rt.StartChildRun(ctx, parentID, in.Objective, ownerID, constraints)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"agent_id":   child.AgentID,
				"loop_id":    child.RunID,
				"channel_id": child.ChannelID,
				"role":       role,
				"profile":    profile,
				"state":      child.State,
			})
		},
	}
}

func newPostMessageTool(rt *Runtime) Tool {
	type args struct {
		ChannelID string `json:"channel_id"`
		From      string `json:"from,omitempty"`
		Role      string `json:"role,omitempty"`
		Content   string `json:"content"`
	}
	return Tool{
		Name:        "post_message",
		Description: "Post a message to a shared channel without blocking.",
		Parameters: jsonSchemaObject(map[string]any{
			"channel_id": map[string]any{"type": "string"},
			"from":       map[string]any{"type": "string"},
			"role":       map[string]any{"type": "string"},
			"content":    map[string]any{"type": "string"},
		}, []string{"channel_id", "content"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode post_message args: %w", err)
			}
			from := strings.TrimSpace(in.From)
			if from == "" {
				from = stringFromToolContext(ctx, toolCtxRunID)
			}
			role := strings.TrimSpace(in.Role)
			if role == "" {
				role = stringFromToolContext(ctx, toolCtxRole)
			}
			cursor, err := rt.ChannelPost(ctx, in.ChannelID, from, role, in.Content)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"channel_id": in.ChannelID,
				"cursor":     cursor,
				"status":     "posted",
			})
		},
	}
}

func newReadMessagesTool(rt *Runtime) Tool {
	type args struct {
		ChannelID string `json:"channel_id"`
		Cursor    uint64 `json:"cursor,omitempty"`
	}
	return Tool{
		Name:        "read_messages",
		Description: "Read messages from a shared channel since a cursor.",
		Parameters: jsonSchemaObject(map[string]any{
			"channel_id": map[string]any{"type": "string"},
			"cursor":     map[string]any{"type": "integer", "minimum": 0},
		}, []string{"channel_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode read_messages args: %w", err)
			}
			messages, cursor, err := rt.ChannelRead(in.ChannelID, in.Cursor)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"channel_id": in.ChannelID,
				"messages":   messages,
				"cursor":     cursor,
			})
		},
	}
}

func newWaitForMessageTool(rt *Runtime) Tool {
	type args struct {
		ChannelID string `json:"channel_id"`
		Cursor    uint64 `json:"cursor,omitempty"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
	}
	return Tool{
		Name:        "wait_for_message",
		Description: "Block until a new message arrives on a shared channel or the timeout expires.",
		Parameters: jsonSchemaObject(map[string]any{
			"channel_id": map[string]any{"type": "string"},
			"cursor":     map[string]any{"type": "integer", "minimum": 0},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"channel_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode wait_for_message args: %w", err)
			}
			timeout := 30 * time.Second
			if in.TimeoutMS > 0 {
				timeout = time.Duration(in.TimeoutMS) * time.Millisecond
			}
			waitCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			messages, cursor, err := rt.ChannelWait(waitCtx, in.ChannelID, in.Cursor)
			if err != nil {
				if err == context.DeadlineExceeded || err == waitCtx.Err() {
					return toolResultJSON(map[string]any{
						"channel_id": in.ChannelID,
						"messages":   []ChannelMessage{},
						"cursor":     in.Cursor,
						"timed_out":  true,
					})
				}
				return "", err
			}
			return toolResultJSON(map[string]any{
				"channel_id": in.ChannelID,
				"messages":   messages,
				"cursor":     cursor,
				"timed_out":  false,
			})
		},
	}
}

func newCloseAgentTool(rt *Runtime) Tool {
	type args struct {
		AgentID string `json:"agent_id"`
	}
	return Tool{
		Name:        "close_agent",
		Description: "Cancel a spawned agent by agent id.",
		Parameters: jsonSchemaObject(map[string]any{
			"agent_id": map[string]any{"type": "string"},
		}, []string{"agent_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode close_agent args: %w", err)
			}
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			if ownerID == "" {
				return "", fmt.Errorf("close_agent missing owner context")
			}
			if err := rt.CancelAgent(ctx, in.AgentID, ownerID); err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"agent_id": in.AgentID,
				"status":   "closed",
			})
		},
	}
}
