package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yusefmosiah/go-choir/internal/events"
	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

type researchFindingEvidenceInput struct {
	Kind      string          `json:"kind"`
	SourceURI string          `json:"source_uri,omitempty"`
	Title     string          `json:"title,omitempty"`
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

func RegisterResearcherTools(registry *ToolRegistry, rt *Runtime) error {
	return registry.Register(newSubmitResearchFindingsTool(rt))
}

func newSubmitResearchFindingsTool(rt *Runtime) Tool {
	type args struct {
		FindingID string                         `json:"finding_id"`
		AgentID   string                         `json:"agent_id,omitempty"`
		ChannelID string                         `json:"channel_id,omitempty"`
		Findings  []string                       `json:"findings,omitempty"`
		Evidence  []researchFindingEvidenceInput `json:"evidence,omitempty"`
		Notes     []string                       `json:"notes,omitempty"`
		Questions []string                       `json:"questions,omitempty"`
	}
	return Tool{
		Name:        "submit_research_findings",
		Description: "Persist researcher evidence and atomically send one addressed findings delivery to the owning agent.",
		Parameters: jsonSchemaObject(map[string]any{
			"finding_id": map[string]any{"type": "string"},
			"agent_id":   map[string]any{"type": "string"},
			"channel_id": map[string]any{"type": "string"},
			"findings": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"evidence": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":       map[string]any{"type": "string"},
						"source_uri": map[string]any{"type": "string"},
						"title":      map[string]any{"type": "string"},
						"content":    map[string]any{"type": "string"},
						"metadata":   map[string]any{"type": "object"},
					},
					"required":             []string{"kind", "content"},
					"additionalProperties": false,
				},
			},
			"notes": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"questions": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		}, []string{"finding_id"}, false),
		Func: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode submit_research_findings args: %w", err)
			}
			findingID := strings.TrimSpace(in.FindingID)
			if findingID == "" {
				return "", fmt.Errorf("finding_id must not be empty")
			}
			ownerID := stringFromToolContext(ctx, toolCtxOwnerID)
			agentID := stringFromToolContext(ctx, toolCtxAgentID)
			runID := stringFromToolContext(ctx, toolCtxRunID)
			role := stringFromToolContext(ctx, toolCtxRole)
			channelID := strings.TrimSpace(in.ChannelID)
			if ownerID == "" || agentID == "" || runID == "" {
				return "", fmt.Errorf("submit_research_findings missing researcher context")
			}

			findings := trimNonEmpty(in.Findings)
			notes := trimNonEmpty(in.Notes)
			questions := trimNonEmpty(in.Questions)
			if len(findings) == 0 && len(in.Evidence) == 0 && len(notes) == 0 && len(questions) == 0 {
				return "", fmt.Errorf("submit_research_findings requires findings, evidence, notes, or questions")
			}

			targetAgentID, targetChannelID, err := resolveFindingsTarget(ctx, rt, strings.TrimSpace(in.AgentID))
			if err != nil {
				return "", err
			}
			if channelID == "" {
				channelID = targetChannelID
			}
			if channelID == "" {
				channelID = stringFromToolContext(ctx, toolCtxChannelID)
			}
			if channelID == "" {
				return "", fmt.Errorf("submit_research_findings could not resolve channel_id")
			}

			evidenceIDs := make([]string, 0, len(in.Evidence))
			for idx, item := range in.Evidence {
				rec, err := ensureFindingEvidence(ctx, rt.store, ownerID, agentID, findingID, idx, item)
				if err != nil {
					return "", err
				}
				evidenceIDs = append(evidenceIDs, rec.EvidenceID)
			}

			content := buildResearchFindingsMessage(findings, in.Evidence, evidenceIDs, notes, questions)
			trajectoryID := ""
			if runRec, _ := ctx.Value(toolCtxRunRecord).(*types.RunRecord); runRec != nil && runRec.Metadata != nil {
				if id, _ := runRec.Metadata[runMetadataTrajectoryID].(string); strings.TrimSpace(id) != "" {
					trajectoryID = strings.TrimSpace(id)
				}
			}
			finding := types.ResearchFindingRecord{
				FindingID:     findingID,
				OwnerID:       ownerID,
				AgentID:       agentID,
				TargetAgentID: targetAgentID,
				ChannelID:     channelID,
				TrajectoryID:  trajectoryID,
				Findings:      findings,
				EvidenceIDs:   evidenceIDs,
				Notes:         notes,
				Questions:     questions,
				Content:       content,
				CreatedAt:     time.Now().UTC(),
			}

			message := &types.ChannelMessage{
				ChannelID:    channelID,
				From:         runID,
				FromAgentID:  agentID,
				FromRunID:    runID,
				ToAgentID:    targetAgentID,
				TrajectoryID: trajectoryID,
				Role:         nonEmpty(role, AgentProfileResearcher),
				Content:      content,
				Timestamp:    finding.CreatedAt,
			}
			delivery := types.InboxDelivery{
				DeliveryID:   uuid.NewString(),
				OwnerID:      ownerID,
				ToAgentID:    targetAgentID,
				FromAgentID:  agentID,
				FromRunID:    runID,
				ChannelID:    channelID,
				Role:         message.Role,
				Content:      content,
				TrajectoryID: trajectoryID,
				CreatedAt:    finding.CreatedAt,
			}

			stored, created, err := rt.store.DispatchResearchFinding(ctx, finding, message, delivery)
			if err != nil {
				return "", err
			}
			if !created {
				if err := validateExistingResearchFinding(stored, finding); err != nil {
					return "", err
				}
			} else {
				rt.emitChannelMessageEvent(ctx, *message, ownerID)
			}

			return toolResultJSON(map[string]any{
				"finding_id":    stored.FindingID,
				"agent_id":      stored.TargetAgentID,
				"channel_id":    stored.ChannelID,
				"cursor":        stored.MessageSeq,
				"evidence_ids":  stored.EvidenceIDs,
				"trajectory_id": stored.TrajectoryID,
				"status":        map[bool]string{true: "submitted", false: "existing"}[created],
			})
		},
	}
}

func resolveFindingsTarget(ctx context.Context, rt *Runtime, explicitAgentID string) (string, string, error) {
	if explicitAgentID != "" {
		target, err := rt.store.GetAgent(ctx, explicitAgentID)
		if err != nil {
			return "", "", fmt.Errorf("submit_research_findings target lookup: %w", err)
		}
		return explicitAgentID, strings.TrimSpace(target.ChannelID), nil
	}
	runRec, _ := ctx.Value(toolCtxRunRecord).(*types.RunRecord)
	if runRec == nil || strings.TrimSpace(runRec.ParentRunID) == "" {
		return "", "", fmt.Errorf("submit_research_findings requires agent_id or a parent run")
	}
	parent, err := rt.store.GetRun(ctx, strings.TrimSpace(runRec.ParentRunID))
	if err != nil {
		return "", "", fmt.Errorf("submit_research_findings parent lookup: %w", err)
	}
	return agentIDForRun(&parent), channelIDForRun(&parent), nil
}

func ensureFindingEvidence(ctx context.Context, s *store.Store, ownerID, agentID, findingID string, index int, item researchFindingEvidenceInput) (types.EvidenceRecord, error) {
	kind := strings.TrimSpace(item.Kind)
	if kind == "" {
		return types.EvidenceRecord{}, fmt.Errorf("evidence[%d].kind must not be empty", index)
	}
	content := strings.TrimSpace(item.Content)
	if content == "" {
		return types.EvidenceRecord{}, fmt.Errorf("evidence[%d].content must not be empty", index)
	}
	evidenceID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("choir:research-finding:"+findingID+fmt.Sprintf(":%d", index))).String()
	rec := types.EvidenceRecord{
		EvidenceID: evidenceID,
		OwnerID:    ownerID,
		AgentID:    agentID,
		Kind:       kind,
		SourceURI:  strings.TrimSpace(item.SourceURI),
		Title:      strings.TrimSpace(item.Title),
		Content:    item.Content,
		Metadata:   item.Metadata,
		CreatedAt:  time.Now().UTC(),
	}
	if len(rec.Metadata) == 0 {
		rec.Metadata = json.RawMessage(`{}`)
	}
	existing, err := s.GetEvidence(ctx, evidenceID, ownerID)
	if err == nil {
		if existing.AgentID != rec.AgentID || existing.Kind != rec.Kind || existing.SourceURI != rec.SourceURI || existing.Title != rec.Title || existing.Content != rec.Content || rawJSONText(existing.Metadata) != rawJSONText(rec.Metadata) {
			return types.EvidenceRecord{}, fmt.Errorf("finding_id %s reuses evidence slot %d with different payload", findingID, index)
		}
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return types.EvidenceRecord{}, err
	}
	if err := s.CreateEvidence(ctx, rec); err != nil {
		return types.EvidenceRecord{}, err
	}
	return rec, nil
}

func validateExistingResearchFinding(existing, want types.ResearchFindingRecord) error {
	if existing.AgentID != want.AgentID || existing.TargetAgentID != want.TargetAgentID || existing.ChannelID != want.ChannelID || existing.Content != want.Content || !stringSlicesEqual(existing.Findings, want.Findings) || !stringSlicesEqual(existing.EvidenceIDs, want.EvidenceIDs) || !stringSlicesEqual(existing.Notes, want.Notes) || !stringSlicesEqual(existing.Questions, want.Questions) {
		return fmt.Errorf("finding_id %s already exists with different payload", want.FindingID)
	}
	return nil
}

func buildResearchFindingsMessage(findings []string, evidence []researchFindingEvidenceInput, evidenceIDs, notes, questions []string) string {
	var b strings.Builder
	b.WriteString("Research findings ready.")
	if len(findings) > 0 {
		b.WriteString("\n\nFindings:")
		for _, finding := range findings {
			b.WriteString("\n- ")
			b.WriteString(finding)
		}
	}
	if len(evidenceIDs) > 0 {
		b.WriteString("\n\nEvidence:")
		for idx, evidenceID := range evidenceIDs {
			b.WriteString("\n- [")
			b.WriteString(evidenceID)
			b.WriteString("]")
			if idx < len(evidence) {
				title := strings.TrimSpace(evidence[idx].Title)
				sourceURI := strings.TrimSpace(evidence[idx].SourceURI)
				switch {
				case title != "" && sourceURI != "":
					b.WriteString(" ")
					b.WriteString(title)
					b.WriteString(" — ")
					b.WriteString(sourceURI)
				case title != "":
					b.WriteString(" ")
					b.WriteString(title)
				case sourceURI != "":
					b.WriteString(" ")
					b.WriteString(sourceURI)
				}
			}
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n\nNotes:")
		for _, note := range notes {
			b.WriteString("\n- ")
			b.WriteString(note)
		}
	}
	if len(questions) > 0 {
		b.WriteString("\n\nOpen questions:")
		for _, question := range questions {
			b.WriteString("\n- ")
			b.WriteString(question)
		}
	}
	return b.String()
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func trimNonEmpty(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func rawJSONText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return strings.TrimSpace(string(raw))
}

func (rt *Runtime) emitChannelMessageEvent(ctx context.Context, message types.ChannelMessage, ownerID string) {
	payload, err := json.Marshal(map[string]any{
		"channel_id":    message.ChannelID,
		"cursor":        message.Seq,
		"from":          message.From,
		"from_agent_id": message.FromAgentID,
		"from_loop_id":  message.FromRunID,
		"to_agent_id":   message.ToAgentID,
		"to_loop_id":    message.ToRunID,
		"trajectory_id": message.TrajectoryID,
		"role":          message.Role,
		"content":       message.Content,
	})
	if err != nil {
		log.Printf("runtime: marshal channel event payload: %v", err)
		return
	}
	evRec := &types.EventRecord{
		EventID:      uuid.New().String(),
		RunID:        message.FromRunID,
		AgentID:      message.FromAgentID,
		ChannelID:    message.ChannelID,
		OwnerID:      ownerID,
		TrajectoryID: message.TrajectoryID,
		Timestamp:    time.Now().UTC(),
		Kind:         types.EventChannelMessage,
		Payload:      payload,
	}
	if err := rt.store.AppendEvent(ctx, evRec); err != nil {
		log.Printf("runtime: persist channel event: %v", err)
		return
	}
	rt.bus.Publish(events.RuntimeEvent{
		Record: *evRec,
		Actor:  events.ActorChannel,
		Cause:  events.CauseChannelMessage,
	})
	rt.maybeWakeVTextOnWorkerMessage(ctx, ownerID, message)
}
