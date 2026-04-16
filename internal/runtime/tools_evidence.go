package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yusefmosiah/go-choir/internal/types"
)

func RegisterEvidenceTools(registry *ToolRegistry, rt *Runtime) error {
	for _, tool := range []Tool{
		newSaveEvidenceTool(rt),
		newReadEvidenceTool(rt),
		newListEvidenceTool(rt),
	} {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newSaveEvidenceTool(rt *Runtime) Tool {
	type args struct {
		Kind      string          `json:"kind"`
		SourceURI string          `json:"source_uri,omitempty"`
		Title     string          `json:"title,omitempty"`
		Content   string          `json:"content"`
		Metadata  json.RawMessage `json:"metadata,omitempty"`
	}
	return Tool{
		Name:        "save_evidence",
		Description: "Persist retrieved or evidentiary material into the user's embedded Dolt workspace.",
		Parameters: jsonSchemaObject(map[string]any{
			"kind":       map[string]any{"type": "string"},
			"source_uri": map[string]any{"type": "string"},
			"title":      map[string]any{"type": "string"},
			"content":    map[string]any{"type": "string"},
			"metadata":   map[string]any{"type": "object"},
		}, []string{"kind", "content"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode save_evidence args: %w", err)
			}
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			agentID := stringFromToolContext(ctx, toolCtxAgentID)
			if ownerID == "" || agentID == "" {
				return "", fmt.Errorf("save_evidence missing owner or agent context")
			}
			kind := strings.TrimSpace(in.Kind)
			if kind == "" {
				return "", fmt.Errorf("kind must not be empty")
			}
			content := strings.TrimSpace(in.Content)
			if content == "" {
				return "", fmt.Errorf("content must not be empty")
			}
			rec := types.EvidenceRecord{
				EvidenceID: uuid.NewString(),
				OwnerID:    ownerID,
				AgentID:    agentID,
				Kind:       kind,
				SourceURI:  strings.TrimSpace(in.SourceURI),
				Title:      strings.TrimSpace(in.Title),
				Content:    in.Content,
				Metadata:   in.Metadata,
				CreatedAt:  time.Now().UTC(),
			}
			if err := rt.store.CreateEvidence(ctx, rec); err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"evidence_id": rec.EvidenceID,
				"owner_id":    rec.OwnerID,
				"agent_id":    rec.AgentID,
				"kind":        rec.Kind,
				"source_uri":  rec.SourceURI,
				"title":       rec.Title,
				"created_at":  rec.CreatedAt.Format(time.RFC3339Nano),
			})
		},
	}
}

func newReadEvidenceTool(rt *Runtime) Tool {
	type args struct {
		EvidenceID string `json:"evidence_id"`
	}
	return Tool{
		Name:        "read_evidence",
		Description: "Read a saved evidence record from embedded Dolt.",
		Parameters: jsonSchemaObject(map[string]any{
			"evidence_id": map[string]any{"type": "string"},
		}, []string{"evidence_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode read_evidence args: %w", err)
			}
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			if ownerID == "" {
				return "", fmt.Errorf("read_evidence missing owner context")
			}
			rec, err := rt.store.GetEvidence(ctx, strings.TrimSpace(in.EvidenceID), ownerID)
			if err != nil {
				return "", err
			}
			return toolResultJSON(map[string]any{
				"evidence_id": rec.EvidenceID,
				"owner_id":    rec.OwnerID,
				"agent_id":    rec.AgentID,
				"kind":        rec.Kind,
				"source_uri":  rec.SourceURI,
				"title":       rec.Title,
				"content":     rec.Content,
				"metadata":    rec.Metadata,
				"created_at":  rec.CreatedAt.Format(time.RFC3339Nano),
			})
		},
	}
}

func newListEvidenceTool(rt *Runtime) Tool {
	type args struct {
		AgentID string `json:"agent_id,omitempty"`
		Limit   int    `json:"limit,omitempty"`
	}
	return Tool{
		Name:        "list_evidence",
		Description: "List recent saved evidence records for an agent or owner scope.",
		Parameters: jsonSchemaObject(map[string]any{
			"agent_id": map[string]any{"type": "string"},
			"limit":    map[string]any{"type": "integer", "minimum": 1},
		}, nil, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode list_evidence args: %w", err)
			}
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			if ownerID == "" {
				return "", fmt.Errorf("list_evidence missing owner context")
			}
			agentID := strings.TrimSpace(in.AgentID)
			if agentID == "" {
				agentID = stringFromToolContext(ctx, toolCtxAgentID)
			}
			recs, err := rt.store.ListEvidenceByAgent(ctx, ownerID, agentID, in.Limit)
			if err != nil {
				return "", err
			}
			items := make([]map[string]any, 0, len(recs))
			for _, rec := range recs {
				items = append(items, map[string]any{
					"evidence_id": rec.EvidenceID,
					"agent_id":    rec.AgentID,
					"kind":        rec.Kind,
					"source_uri":  rec.SourceURI,
					"title":       rec.Title,
					"created_at":  rec.CreatedAt.Format(time.RFC3339Nano),
				})
			}
			return toolResultJSON(map[string]any{
				"agent_id": agentID,
				"items":    items,
			})
		},
	}
}
