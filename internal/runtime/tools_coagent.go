package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
		WorkID    string `json:"work_id,omitempty"`
		Model     string `json:"model,omitempty"`
	}
	return Tool{
		Name:        "spawn_agent",
		Description: "Spawn a child agent task with a specific role/profile and optional shared work channel.",
		Parameters: jsonSchemaObject(map[string]any{
			"objective": map[string]any{"type": "string"},
			"role":      map[string]any{"type": "string"},
			"profile":   map[string]any{"type": "string"},
			"work_id":   map[string]any{"type": "string"},
			"model":     map[string]any{"type": "string"},
		}, []string{"objective", "role"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode spawn_agent args: %w", err)
			}
			parentID := stringFromToolContext(ctx, toolCtxTaskID)
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			if parentID == "" || ownerID == "" {
				return "", fmt.Errorf("spawn_agent missing task context")
			}
			role := strings.TrimSpace(in.Role)
			if role == "" {
				return "", fmt.Errorf("role must not be empty")
			}
			profile := strings.TrimSpace(in.Profile)
			if profile == "" {
				profile = role
			}
			constraints := map[string]any{
				taskMetadataAgentRole:    role,
				taskMetadataAgentProfile: profile,
			}
			if workID := strings.TrimSpace(in.WorkID); workID != "" {
				constraints[taskMetadataWorkID] = workID
			}
			if model := strings.TrimSpace(in.Model); model != "" {
				constraints[taskMetadataModel] = model
			}
			child, err := rt.SpawnTask(ctx, parentID, in.Objective, ownerID, constraints)
			if err != nil {
				return "", err
			}
			workID := workIDForTask(child)
			return toolResultJSON(map[string]any{
				"agent_id": child.TaskID,
				"task_id":  child.TaskID,
				"work_id":  workID,
				"role":     role,
				"profile":  profile,
				"state":    child.State,
			})
		},
	}
}

func newPostMessageTool(rt *Runtime) Tool {
	type args struct {
		WorkID  string `json:"work_id"`
		From    string `json:"from,omitempty"`
		Role    string `json:"role,omitempty"`
		Content string `json:"content"`
	}
	return Tool{
		Name:        "post_message",
		Description: "Post a message to a shared work channel without blocking.",
		Parameters: jsonSchemaObject(map[string]any{
			"work_id": map[string]any{"type": "string"},
			"from":    map[string]any{"type": "string"},
			"role":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		}, []string{"work_id", "content"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode post_message args: %w", err)
			}
			from := strings.TrimSpace(in.From)
			if from == "" {
				from = stringFromToolContext(ctx, toolCtxTaskID)
			}
			role := strings.TrimSpace(in.Role)
			if role == "" {
				role = stringFromToolContext(ctx, toolCtxRole)
			}
			cursor, err := rt.ChannelPost(ctx, in.WorkID, from, role, in.Content)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"work_id": in.WorkID,
				"cursor":  cursor,
				"status":  "posted",
			})
		},
	}
}

func newReadMessagesTool(rt *Runtime) Tool {
	type args struct {
		WorkID string `json:"work_id"`
		Cursor uint64 `json:"cursor,omitempty"`
	}
	return Tool{
		Name:        "read_messages",
		Description: "Read messages from a shared work channel since a cursor.",
		Parameters: jsonSchemaObject(map[string]any{
			"work_id": map[string]any{"type": "string"},
			"cursor":  map[string]any{"type": "integer", "minimum": 0},
		}, []string{"work_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode read_messages args: %w", err)
			}
			messages, cursor, err := rt.ChannelRead(in.WorkID, in.Cursor)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"work_id":  in.WorkID,
				"messages": messages,
				"cursor":   cursor,
			})
		},
	}
}

func newWaitForMessageTool(rt *Runtime) Tool {
	type args struct {
		WorkID    string `json:"work_id"`
		Cursor    uint64 `json:"cursor,omitempty"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
	}
	return Tool{
		Name:        "wait_for_message",
		Description: "Block until a new message arrives on a work channel or the timeout expires.",
		Parameters: jsonSchemaObject(map[string]any{
			"work_id":    map[string]any{"type": "string"},
			"cursor":     map[string]any{"type": "integer", "minimum": 0},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"work_id"}, false),
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
			messages, cursor, err := rt.ChannelWait(waitCtx, in.WorkID, in.Cursor)
			if err != nil {
				if err == context.DeadlineExceeded || err == waitCtx.Err() {
					return toolResultJSON(map[string]any{
						"work_id":   in.WorkID,
						"messages":  []ChannelMessage{},
						"cursor":    in.Cursor,
						"timed_out": true,
					})
				}
				return "", err
			}
			return toolResultJSON(map[string]any{
				"work_id":   in.WorkID,
				"messages":  messages,
				"cursor":    cursor,
				"timed_out": false,
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
		Description: "Cancel a spawned agent task by task/agent id.",
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
			if err := rt.CancelTask(ctx, in.AgentID, ownerID); err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"agent_id": in.AgentID,
				"status":   "closed",
			})
		},
	}
}
