package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yusefmosiah/go-choir/internal/types"
	"github.com/yusefmosiah/go-choir/internal/vmctl"
)

func RegisterVMControlTools(registry *ToolRegistry, rt *Runtime) error {
	for _, tool := range []Tool{
		newForkDesktopTool(rt),
		newPublishDesktopTool(rt),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newForkDesktopTool(rt *Runtime) Tool {
	type args struct {
		DesktopID string `json:"desktop_id,omitempty"`
	}
	return Tool{
		Name:        "fork_desktop",
		Description: "Create a background candidate desktop VM cloned from the current desktop's layout, without exposing it for user switching yet.",
		Parameters:  jsonSchemaObject(map[string]any{"desktop_id": map[string]any{"type": "string"}}, nil, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					return "", fmt.Errorf("decode fork_desktop args: %w", err)
				}
			}
			if rt == nil {
				return "", fmt.Errorf("fork_desktop missing runtime")
			}
			if strings.TrimSpace(rt.cfg.VmctlURL) == "" {
				return "", fmt.Errorf("fork_desktop requires runtime vmctl configuration")
			}

			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			sourceDesktopID := strings.TrimSpace(stringFromToolContext(ctx, toolCtxDesktopID))
			if ownerID == "" {
				return "", fmt.Errorf("fork_desktop missing owner context")
			}
			if sourceDesktopID == "" {
				sourceDesktopID = types.PrimaryDesktopID
			}

			targetDesktopID := normalizeForkDesktopID(in.DesktopID)
			if targetDesktopID == sourceDesktopID {
				return "", fmt.Errorf("fork_desktop target must differ from source desktop")
			}

			client := vmctl.NewClient(rt.cfg.VmctlURL)
			resolved, err := client.ForkDesktop(ownerID, sourceDesktopID, targetDesktopID)
			if err != nil {
				return "", err
			}

			sourceState, err := rt.store.GetDesktopStateForDesktop(ctx, ownerID, sourceDesktopID)
			if err != nil {
				return "", fmt.Errorf("fork_desktop load source state: %w", err)
			}
			clonedState := cloneDesktopState(sourceState)
			clonedState.OwnerID = ownerID
			clonedState.DesktopID = resolved.DesktopID
			clonedState.UpdatedAt = time.Now().UTC()
			if err := rt.store.SaveDesktopStateForDesktop(ctx, clonedState); err != nil {
				return "", fmt.Errorf("fork_desktop save cloned state: %w", err)
			}

			return toolResultJSON(map[string]any{
				"status":              "forked_background",
				"desktop_id":          resolved.DesktopID,
				"parent_desktop_id":   sourceDesktopID,
				"published":           resolved.Published,
				"availability":        "background_only",
				"copied_window_count": len(clonedState.Windows),
			})
		},
	}
}

func newPublishDesktopTool(rt *Runtime) Tool {
	type args struct {
		DesktopID string `json:"desktop_id"`
	}
	return Tool{
		Name:        "publish_desktop",
		Description: "Publish a prepared candidate desktop so it becomes user-switchable.",
		Parameters:  jsonSchemaObject(map[string]any{"desktop_id": map[string]any{"type": "string"}}, []string{"desktop_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode publish_desktop args: %w", err)
			}
			if rt == nil {
				return "", fmt.Errorf("publish_desktop missing runtime")
			}
			if strings.TrimSpace(rt.cfg.VmctlURL) == "" {
				return "", fmt.Errorf("publish_desktop requires runtime vmctl configuration")
			}
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			if ownerID == "" {
				return "", fmt.Errorf("publish_desktop missing owner context")
			}
			desktopID := strings.TrimSpace(in.DesktopID)
			if desktopID == "" {
				return "", fmt.Errorf("publish_desktop requires desktop_id")
			}

			client := vmctl.NewClient(rt.cfg.VmctlURL)
			resolved, err := client.PublishDesktop(ownerID, desktopID)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"status":            "published",
				"desktop_id":        resolved.DesktopID,
				"parent_desktop_id": resolved.ParentDesktopID,
				"published":         resolved.Published,
				"desktop_url":       "/?desktop_id=" + resolved.DesktopID,
			})
		},
	}
}

func normalizeForkDesktopID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if r == '-' || r == '_' || r == ' ' {
			if b.Len() > 0 {
				b.WriteByte('-')
			}
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" || id == types.PrimaryDesktopID {
		return "branch-" + uuid.New().String()[:8]
	}
	return id
}

func cloneDesktopState(state types.DesktopState) types.DesktopState {
	raw, err := json.Marshal(state)
	if err != nil {
		return state
	}
	var cloned types.DesktopState
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return state
	}
	return cloned
}
