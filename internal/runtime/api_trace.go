package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yusefmosiah/go-choir/internal/store"
	"github.com/yusefmosiah/go-choir/internal/types"
)

type traceTrajectoryListResponse struct {
	Trajectories []traceTrajectorySummary `json:"trajectories"`
}

type traceTrajectorySnapshotResponse struct {
	Trajectory traceTrajectorySummary `json:"trajectory"`
	Agents     []traceAgentNode       `json:"agents"`
	Edges      []traceAgentEdge       `json:"edges"`
	Moments    []traceMomentSummary   `json:"moments"`
}

type traceMomentDetailResponse struct {
	TrajectoryID string                        `json:"trajectory_id"`
	Moment       traceMomentSummary            `json:"moment"`
	Events       []types.EventRecord           `json:"events"`
	Messages     []types.ChannelMessage        `json:"messages"`
	Findings     []types.ResearchFindingRecord `json:"findings"`
	References   traceMomentReferences         `json:"references"`
}

type traceTrajectorySummary struct {
	TrajectoryID     string         `json:"trajectory_id"`
	Title            string         `json:"title"`
	Subtitle         string         `json:"subtitle,omitempty"`
	State            types.RunState `json:"state,omitempty"`
	Live             bool           `json:"live"`
	LatestActivityAt string         `json:"latest_activity_at,omitempty"`
	LeadAgents       []string       `json:"lead_agents,omitempty"`
	AgentCount       int            `json:"agent_count"`
	DelegationCount  int            `json:"delegation_count"`
	MomentCount      int            `json:"moment_count"`
	MessageCount     int            `json:"message_count"`
	FindingCount     int            `json:"finding_count"`
	DocID            string         `json:"doc_id,omitempty"`
	LatestStreamSeq  int64          `json:"latest_stream_seq,omitempty"`
}

type traceAgentNode struct {
	AgentID          string         `json:"agent_id"`
	Label            string         `json:"label"`
	Profile          string         `json:"profile,omitempty"`
	Role             string         `json:"role,omitempty"`
	State            types.RunState `json:"state,omitempty"`
	RunCount         int            `json:"run_count"`
	FirstSeenAt      string         `json:"first_seen_at,omitempty"`
	LatestActivityAt string         `json:"latest_activity_at,omitempty"`
	Entry            bool           `json:"entry"`
}

type traceAgentEdge struct {
	FromAgentID      string `json:"from_agent_id"`
	ToAgentID        string `json:"to_agent_id"`
	DelegationCount  int    `json:"delegation_count"`
	LatestActivityAt string `json:"latest_activity_at,omitempty"`
}

type traceMomentSummary struct {
	MomentID          string          `json:"moment_id"`
	StreamSeq         int64           `json:"stream_seq"`
	Timestamp         string          `json:"timestamp"`
	Kind              types.EventKind `json:"kind"`
	Phase             string          `json:"phase,omitempty"`
	RunID             string          `json:"loop_id,omitempty"`
	AgentID           string          `json:"agent_id,omitempty"`
	AgentLabel        string          `json:"agent_label,omitempty"`
	AgentProfile      string          `json:"agent_profile,omitempty"`
	AgentRole         string          `json:"agent_role,omitempty"`
	ChannelID         string          `json:"channel_id,omitempty"`
	Summary           string          `json:"summary"`
	Tone              string          `json:"tone"`
	HasDetail         bool            `json:"has_detail"`
	MessageSeq        int64           `json:"message_seq,omitempty"`
	DocID             string          `json:"doc_id,omitempty"`
	RevisionID        string          `json:"revision_id,omitempty"`
	CurrentRevisionID string          `json:"current_revision_id,omitempty"`
	FindingID         string          `json:"finding_id,omitempty"`
}

type traceMomentReferences struct {
	DocID             string   `json:"doc_id,omitempty"`
	RevisionID        string   `json:"revision_id,omitempty"`
	CurrentRevisionID string   `json:"current_revision_id,omitempty"`
	FindingID         string   `json:"finding_id,omitempty"`
	EvidenceIDs       []string `json:"evidence_ids,omitempty"`
}

type traceTrajectoryBundle struct {
	Trajectory traceTrajectorySummary
	Agents     []traceAgentNode
	Edges      []traceAgentEdge
	Moments    []traceMomentSummary
	events     []types.EventRecord
	messages   []types.ChannelMessage
	findings   []types.ResearchFindingRecord
	agentIndex map[string]traceAgentNode
}

func (h *APIHandler) HandleTraceTrajectories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}

	ownerID, err := authenticateUser(r)
	if err != nil {
		writeAPIJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/trace/trajectories")
	if path == "" || path == "/" {
		h.handleTraceTrajectoryIndex(w, r, ownerID)
		return
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "trajectory_id is required"})
		return
	}
	trajectoryID := strings.TrimSpace(parts[0])

	switch {
	case len(parts) == 1:
		h.handleTraceTrajectorySnapshot(w, r, ownerID, trajectoryID)
	case len(parts) == 2 && parts[1] == "events":
		h.handleTraceTrajectoryEvents(w, r, ownerID, trajectoryID)
	case len(parts) == 3 && parts[1] == "moments":
		h.handleTraceTrajectoryMomentDetail(w, r, ownerID, trajectoryID, strings.TrimSpace(parts[2]))
	default:
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "trace route not found"})
	}
}

func (h *APIHandler) handleTraceTrajectoryIndex(w http.ResponseWriter, r *http.Request, ownerID string) {
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	runs, err := h.rt.ListRunsByOwner(r.Context(), ownerID, limit)
	if err != nil {
		log.Printf("runtime trace: list runs by owner: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list trajectories"})
		return
	}
	events, err := h.rt.Store().ListEventsByOwner(r.Context(), ownerID, limit*4)
	if err != nil {
		log.Printf("runtime trace: list owner events: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to list trajectories"})
		return
	}

	writeAPIJSON(w, http.StatusOK, traceTrajectoryListResponse{
		Trajectories: buildTraceTrajectoryIndex(runs, events),
	})
}

func (h *APIHandler) handleTraceTrajectorySnapshot(w http.ResponseWriter, r *http.Request, ownerID, trajectoryID string) {
	bundle, err := h.loadTraceTrajectoryBundle(r.Context(), ownerID, trajectoryID)
	if err != nil {
		if err == store.ErrNotFound {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "trajectory not found"})
			return
		}
		log.Printf("runtime trace: load trajectory snapshot: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to load trajectory"})
		return
	}
	writeAPIJSON(w, http.StatusOK, traceTrajectorySnapshotResponse{
		Trajectory: bundle.Trajectory,
		Agents:     bundle.Agents,
		Edges:      bundle.Edges,
		Moments:    bundle.Moments,
	})
}

func (h *APIHandler) handleTraceTrajectoryMomentDetail(w http.ResponseWriter, r *http.Request, ownerID, trajectoryID, momentID string) {
	if momentID == "" {
		writeAPIJSON(w, http.StatusBadRequest, apiError{Error: "moment_id is required"})
		return
	}

	bundle, err := h.loadTraceTrajectoryBundle(r.Context(), ownerID, trajectoryID)
	if err != nil {
		if err == store.ErrNotFound {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "trajectory not found"})
			return
		}
		log.Printf("runtime trace: load moment detail bundle: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to load moment detail"})
		return
	}

	var selected types.EventRecord
	found := false
	for _, ev := range bundle.events {
		if ev.EventID == momentID {
			selected = ev
			found = true
			break
		}
	}
	if !found {
		writeAPIJSON(w, http.StatusNotFound, apiError{Error: "moment not found"})
		return
	}

	moment := buildTraceMomentSummary(selected, bundle.agentIndex)
	detail := traceMomentDetailResponse{
		TrajectoryID: trajectoryID,
		Moment:       moment,
		Events:       []types.EventRecord{selected},
		References:   buildTraceMomentReferences(parseTracePayload(selected.Payload)),
	}

	messageSeq := moment.MessageSeq
	channelID := strings.TrimSpace(moment.ChannelID)
	if messageSeq > 0 && channelID != "" {
		for _, msg := range bundle.messages {
			if msg.ChannelID == channelID && msg.Seq == messageSeq {
				detail.Messages = append(detail.Messages, msg)
			}
		}
	}

	findingID := moment.FindingID
	for _, finding := range bundle.findings {
		if findingID != "" && finding.FindingID == findingID {
			detail.Findings = append(detail.Findings, finding)
			continue
		}
		if channelID != "" && messageSeq > 0 && finding.ChannelID == channelID && finding.MessageSeq == messageSeq {
			detail.Findings = append(detail.Findings, finding)
		}
	}
	if len(detail.Findings) > 0 {
		detail.References.EvidenceIDs = append(detail.References.EvidenceIDs, collectFindingEvidenceIDs(detail.Findings)...)
		if detail.References.FindingID == "" {
			detail.References.FindingID = detail.Findings[0].FindingID
		}
	}

	writeAPIJSON(w, http.StatusOK, detail)
}

func (h *APIHandler) handleTraceTrajectoryEvents(w http.ResponseWriter, r *http.Request, ownerID, trajectoryID string) {
	if _, err := h.loadTraceTrajectoryBundle(r.Context(), ownerID, trajectoryID); err != nil {
		if err == store.ErrNotFound {
			writeAPIJSON(w, http.StatusNotFound, apiError{Error: "trajectory not found"})
			return
		}
		log.Printf("runtime trace: verify trajectory stream: %v", err)
		writeAPIJSON(w, http.StatusInternalServerError, apiError{Error: "failed to open trajectory stream"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	afterSeq := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
			afterSeq = n
		}
	}
	if afterSeq > 0 {
		events, err := h.rt.Store().ListEventsByTrajectoryAfter(r.Context(), ownerID, trajectoryID, afterSeq, 500)
		if err != nil {
			log.Printf("runtime trace: historical trajectory events: %v", err)
		} else {
			for _, ev := range events {
				writeTraceEvent(w, ev)
			}
			if len(events) > 0 {
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}

	ch := h.rt.EventBus().SubscribeWithBuffer(128)
	defer h.rt.EventBus().Unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Record.OwnerID != ownerID || strings.TrimSpace(ev.Record.TrajectoryID) != trajectoryID {
				continue
			}
			writeTraceEvent(w, ev.Record)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

func writeTraceEvent(w http.ResponseWriter, ev types.EventRecord) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func (h *APIHandler) loadTraceTrajectoryBundle(ctx context.Context, ownerID, trajectoryID string) (traceTrajectoryBundle, error) {
	runs, err := h.rt.ListRunsByOwner(ctx, ownerID, 1000)
	if err != nil {
		return traceTrajectoryBundle{}, fmt.Errorf("list owner runs: %w", err)
	}
	filteredRuns := make([]types.RunRecord, 0)
	for _, run := range runs {
		if traceTrajectoryIDForRun(run) == trajectoryID {
			filteredRuns = append(filteredRuns, run)
		}
	}

	events, err := h.rt.Store().ListEventsByTrajectory(ctx, ownerID, trajectoryID, 2000)
	if err != nil {
		return traceTrajectoryBundle{}, fmt.Errorf("list trajectory events: %w", err)
	}
	messages, err := h.rt.Store().ListChannelMessagesByTrajectory(ctx, ownerID, trajectoryID, 1000)
	if err != nil {
		return traceTrajectoryBundle{}, fmt.Errorf("list trajectory messages: %w", err)
	}
	findings, err := h.rt.Store().ListResearchFindingsByTrajectory(ctx, ownerID, trajectoryID, 1000)
	if err != nil {
		return traceTrajectoryBundle{}, fmt.Errorf("list trajectory findings: %w", err)
	}

	if len(filteredRuns) == 0 && len(events) == 0 && len(messages) == 0 && len(findings) == 0 {
		return traceTrajectoryBundle{}, store.ErrNotFound
	}

	agents, agentIndex := buildTraceAgentNodes(filteredRuns)
	edges := buildTraceAgentEdges(filteredRuns)
	moments := buildTraceMomentSummaries(events, agentIndex)
	trajectory := buildTraceTrajectorySummary(trajectoryID, filteredRuns, agents, edges, moments, messages, findings)

	return traceTrajectoryBundle{
		Trajectory: trajectory,
		Agents:     agents,
		Edges:      edges,
		Moments:    moments,
		events:     events,
		messages:   messages,
		findings:   findings,
		agentIndex: agentIndex,
	}, nil
}

func buildTraceTrajectoryIndex(runs []types.RunRecord, events []types.EventRecord) []traceTrajectorySummary {
	if len(runs) == 0 {
		return nil
	}
	groupedRuns := make(map[string][]types.RunRecord)
	for _, run := range runs {
		trajectoryID := traceTrajectoryIDForRun(run)
		groupedRuns[trajectoryID] = append(groupedRuns[trajectoryID], run)
	}
	groupedEvents := make(map[string][]types.EventRecord)
	for _, ev := range events {
		trajectoryID := strings.TrimSpace(ev.TrajectoryID)
		if trajectoryID == "" {
			continue
		}
		groupedEvents[trajectoryID] = append(groupedEvents[trajectoryID], ev)
	}

	summaries := make([]traceTrajectorySummary, 0, len(groupedRuns))
	for trajectoryID, runGroup := range groupedRuns {
		agents, _ := buildTraceAgentNodes(runGroup)
		edges := buildTraceAgentEdges(runGroup)
		moments := buildTraceMomentSummaries(groupedEvents[trajectoryID], nil)
		summaries = append(summaries, buildTraceTrajectorySummary(trajectoryID, runGroup, agents, edges, moments, nil, nil))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].LatestActivityAt > summaries[j].LatestActivityAt
	})
	return summaries
}

func buildTraceTrajectorySummary(trajectoryID string, runs []types.RunRecord, agents []traceAgentNode, edges []traceAgentEdge, moments []traceMomentSummary, messages []types.ChannelMessage, findings []types.ResearchFindingRecord) traceTrajectorySummary {
	latestAt := time.Time{}
	var latestRun types.RunRecord
	for _, run := range runs {
		runAt := traceRunActivityTime(run)
		if runAt.After(latestAt) {
			latestAt = runAt
			latestRun = run
		}
	}
	for _, moment := range moments {
		ts := parseTraceTime(moment.Timestamp)
		if ts.After(latestAt) {
			latestAt = ts
		}
	}
	for _, msg := range messages {
		if msg.Timestamp.After(latestAt) {
			latestAt = msg.Timestamp
		}
	}
	for _, finding := range findings {
		if finding.CreatedAt.After(latestAt) {
			latestAt = finding.CreatedAt
		}
	}

	leadAgents := make([]string, 0)
	for _, agent := range agents {
		if agent.Entry {
			leadAgents = append(leadAgents, agent.Label)
		}
	}
	if len(leadAgents) == 0 {
		for _, agent := range agents {
			leadAgents = append(leadAgents, agent.Label)
		}
	}
	sort.Strings(leadAgents)

	latestStreamSeq := int64(0)
	for _, moment := range moments {
		if moment.StreamSeq > latestStreamSeq {
			latestStreamSeq = moment.StreamSeq
		}
	}

	title := traceTrajectoryTitle(latestRun, trajectoryID)
	subtitle := traceTrajectorySubtitle(latestRun, leadAgents)
	docID := traceRunMetadataString(latestRun, "doc_id")
	if docID == "" {
		for _, moment := range moments {
			if strings.TrimSpace(moment.DocID) != "" {
				docID = strings.TrimSpace(moment.DocID)
				break
			}
		}
	}

	return traceTrajectorySummary{
		TrajectoryID:     trajectoryID,
		Title:            title,
		Subtitle:         subtitle,
		State:            latestRun.State,
		Live:             latestRun.State == types.RunPending || latestRun.State == types.RunRunning || latestRun.State == types.RunBlocked,
		LatestActivityAt: formatTraceTime(latestAt),
		LeadAgents:       leadAgents,
		AgentCount:       len(agents),
		DelegationCount:  len(edges),
		MomentCount:      len(moments),
		MessageCount:     len(messages),
		FindingCount:     len(findings),
		DocID:            docID,
		LatestStreamSeq:  latestStreamSeq,
	}
}

func buildTraceAgentNodes(runs []types.RunRecord) ([]traceAgentNode, map[string]traceAgentNode) {
	type agg struct {
		traceAgentNode
		firstSeen time.Time
		latestAt  time.Time
	}
	byAgent := make(map[string]*agg)
	childAgents := make(map[string]bool)
	for _, run := range runs {
		agentID := strings.TrimSpace(run.AgentID)
		if agentID == "" {
			agentID = traceSyntheticAgentID(run)
		}
		entry, ok := byAgent[agentID]
		if !ok {
			entry = &agg{
				traceAgentNode: traceAgentNode{
					AgentID:  agentID,
					Label:    traceAgentLabel(run),
					Profile:  traceRunProfile(run),
					Role:     traceRunRole(run),
					State:    run.State,
					RunCount: 0,
				},
				firstSeen: traceRunActivityTime(run),
				latestAt:  traceRunActivityTime(run),
			}
			byAgent[agentID] = entry
		}
		runAt := traceRunActivityTime(run)
		if runAt.Before(entry.firstSeen) {
			entry.firstSeen = runAt
		}
		if runAt.After(entry.latestAt) {
			entry.latestAt = runAt
			entry.State = run.State
			entry.Label = traceAgentLabel(run)
			entry.Profile = traceRunProfile(run)
			entry.Role = traceRunRole(run)
		}
		entry.RunCount++
		if strings.TrimSpace(run.ParentRunID) != "" {
			childAgents[agentID] = true
		}
	}

	agents := make([]traceAgentNode, 0, len(byAgent))
	index := make(map[string]traceAgentNode, len(byAgent))
	for agentID, entry := range byAgent {
		entry.Entry = !childAgents[agentID]
		entry.FirstSeenAt = formatTraceTime(entry.firstSeen)
		entry.LatestActivityAt = formatTraceTime(entry.latestAt)
		agents = append(agents, entry.traceAgentNode)
		index[agentID] = entry.traceAgentNode
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Entry != agents[j].Entry {
			return agents[i].Entry
		}
		return agents[i].LatestActivityAt < agents[j].LatestActivityAt
	})
	return agents, index
}

func buildTraceAgentEdges(runs []types.RunRecord) []traceAgentEdge {
	parentRuns := make(map[string]types.RunRecord, len(runs))
	for _, run := range runs {
		parentRuns[run.RunID] = run
	}
	type edgeKey struct {
		from string
		to   string
	}
	type edgeAgg struct {
		count  int
		latest time.Time
	}
	agg := make(map[edgeKey]*edgeAgg)
	for _, run := range runs {
		if strings.TrimSpace(run.ParentRunID) == "" {
			continue
		}
		parent, ok := parentRuns[strings.TrimSpace(run.ParentRunID)]
		if !ok {
			continue
		}
		key := edgeKey{from: traceAgentID(parent), to: traceAgentID(run)}
		if key.from == "" || key.to == "" {
			continue
		}
		entry, ok := agg[key]
		if !ok {
			entry = &edgeAgg{}
			agg[key] = entry
		}
		entry.count++
		runAt := traceRunActivityTime(run)
		if runAt.After(entry.latest) {
			entry.latest = runAt
		}
	}

	edges := make([]traceAgentEdge, 0, len(agg))
	for key, entry := range agg {
		edges = append(edges, traceAgentEdge{
			FromAgentID:      key.from,
			ToAgentID:        key.to,
			DelegationCount:  entry.count,
			LatestActivityAt: formatTraceTime(entry.latest),
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].LatestActivityAt == edges[j].LatestActivityAt {
			return edges[i].FromAgentID < edges[j].FromAgentID
		}
		return edges[i].LatestActivityAt < edges[j].LatestActivityAt
	})
	return edges
}

func buildTraceMomentSummaries(events []types.EventRecord, agentIndex map[string]traceAgentNode) []traceMomentSummary {
	moments := make([]traceMomentSummary, 0, len(events))
	for _, ev := range events {
		if ev.Kind == types.EventRunDelta {
			continue
		}
		moments = append(moments, buildTraceMomentSummary(ev, agentIndex))
	}
	sort.Slice(moments, func(i, j int) bool {
		return moments[i].StreamSeq < moments[j].StreamSeq
	})
	return moments
}

func buildTraceMomentSummary(ev types.EventRecord, agentIndex map[string]traceAgentNode) traceMomentSummary {
	payload := parseTracePayload(ev.Payload)
	agent := traceAgentNode{}
	if agentIndex != nil {
		agent = agentIndex[strings.TrimSpace(ev.AgentID)]
	}
	docID := payloadString(payload, "doc_id")
	revisionID := payloadString(payload, "revision_id")
	currentRevisionID := payloadString(payload, "current_revision_id")
	findingID := payloadString(payload, "finding_id")
	channelID := strings.TrimSpace(ev.ChannelID)
	if ch := payloadString(payload, "channel_id"); ch != "" {
		channelID = ch
	}
	return traceMomentSummary{
		MomentID:          ev.EventID,
		StreamSeq:         ev.StreamSeq,
		Timestamp:         formatTraceTime(ev.Timestamp),
		Kind:              ev.Kind,
		Phase:             strings.TrimSpace(ev.Phase),
		RunID:             strings.TrimSpace(ev.RunID),
		AgentID:           strings.TrimSpace(ev.AgentID),
		AgentLabel:        traceNonEmpty(agent.Label, shortTraceID(ev.AgentID)),
		AgentProfile:      agent.Profile,
		AgentRole:         agent.Role,
		ChannelID:         channelID,
		Summary:           traceEventSummary(ev, payload),
		Tone:              traceEventTone(ev),
		HasDetail:         true,
		MessageSeq:        payloadInt64(payload, "cursor"),
		DocID:             docID,
		RevisionID:        revisionID,
		CurrentRevisionID: currentRevisionID,
		FindingID:         findingID,
	}
}

func buildTraceMomentReferences(payload map[string]any) traceMomentReferences {
	return traceMomentReferences{
		DocID:             payloadString(payload, "doc_id"),
		RevisionID:        payloadString(payload, "revision_id"),
		CurrentRevisionID: payloadString(payload, "current_revision_id"),
		FindingID:         payloadString(payload, "finding_id"),
		EvidenceIDs:       payloadStringSlice(payload, "evidence_ids"),
	}
}

func collectFindingEvidenceIDs(findings []types.ResearchFindingRecord) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, finding := range findings {
		for _, evidenceID := range finding.EvidenceIDs {
			if evidenceID == "" {
				continue
			}
			if _, ok := seen[evidenceID]; ok {
				continue
			}
			seen[evidenceID] = struct{}{}
			ids = append(ids, evidenceID)
		}
	}
	sort.Strings(ids)
	return ids
}

func parseTracePayload(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]any{}
	}
	return payload
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func payloadStringSlice(payload map[string]any, key string) []string {
	raw, ok := payload[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, _ := item.(string)
		text = strings.TrimSpace(text)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func payloadInt64(payload map[string]any, key string) int64 {
	switch value := payload[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n
	default:
		return 0
	}
}

func traceEventSummary(ev types.EventRecord, payload map[string]any) string {
	switch ev.Kind {
	case types.EventRunSubmitted:
		if parentID := payloadString(payload, "parent_id"); parentID != "" {
			return fmt.Sprintf("spawned from %s", shortTraceID(parentID))
		}
		return "loop submitted"
	case types.EventRunStarted:
		return "loop started"
	case types.EventRunProgress:
		if status := payloadString(payload, "status"); status != "" {
			return status
		}
		if phase := payloadString(payload, "phase"); phase != "" {
			return phase
		}
		return "progress update"
	case types.EventToolInvoked:
		return fmt.Sprintf("invoked %s", traceNonEmpty(payloadString(payload, "tool"), "tool"))
	case types.EventToolResult:
		tool := traceNonEmpty(payloadString(payload, "tool"), "tool")
		if isError, _ := payload["is_error"].(bool); isError {
			return fmt.Sprintf("%s returned error", tool)
		}
		return fmt.Sprintf("%s returned", tool)
	case types.EventChannelMessage:
		from := traceNonEmpty(payloadString(payload, "from"), "agent")
		toAgent := payloadString(payload, "to_agent_id")
		content := traceExcerpt(payloadString(payload, "content"), 96)
		if toAgent != "" {
			return fmt.Sprintf("%s -> %s: %s", from, shortTraceID(toAgent), traceNonEmpty(content, "message"))
		}
		return fmt.Sprintf("%s: %s", from, traceNonEmpty(content, "message"))
	case types.EventRunCompleted:
		return "loop completed"
	case types.EventRunFailed, types.EventRunBlocked, types.EventRunCancelled:
		if errText := payloadString(payload, "error"); errText != "" {
			return errText
		}
		return string(ev.Kind)
	case types.EventVTextAgentRevisionStarted:
		return "vtext revision started"
	case types.EventVTextAgentRevisionProgress:
		if phase := payloadString(payload, "phase"); phase != "" {
			return fmt.Sprintf("vtext %s", phase)
		}
		return "vtext revision progress"
	case types.EventVTextAgentRevisionCompleted:
		if revisionID := payloadString(payload, "revision_id"); revisionID != "" {
			return fmt.Sprintf("created revision %s", shortTraceID(revisionID))
		}
		return "vtext revision completed"
	case types.EventVTextAgentRevisionFailed:
		if errText := payloadString(payload, "error"); errText != "" {
			return errText
		}
		return "vtext revision failed"
	case types.EventVTextDocumentRevisionCreated:
		if revisionID := payloadString(payload, "revision_id"); revisionID != "" {
			return fmt.Sprintf("document head -> %s", shortTraceID(revisionID))
		}
		return "document revision created"
	default:
		return string(ev.Kind)
	}
}

func traceEventTone(ev types.EventRecord) string {
	switch ev.Kind {
	case types.EventRunFailed, types.EventRunBlocked, types.EventRunCancelled, types.EventVTextAgentRevisionFailed:
		return "error"
	case types.EventRunCompleted, types.EventVTextAgentRevisionCompleted, types.EventVTextDocumentRevisionCreated:
		return "success"
	case types.EventChannelMessage:
		return "message"
	case types.EventToolInvoked, types.EventToolResult:
		return "tool"
	default:
		return "neutral"
	}
}

func traceTrajectoryIDForRun(run types.RunRecord) string {
	if trajectoryID := traceRunMetadataString(run, runMetadataTrajectoryID); trajectoryID != "" {
		return trajectoryID
	}
	if channelID := strings.TrimSpace(run.ChannelID); channelID != "" {
		return channelID
	}
	return run.RunID
}

func traceRunMetadataString(run types.RunRecord, key string) string {
	if run.Metadata == nil {
		return ""
	}
	value, _ := run.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func traceRunProfile(run types.RunRecord) string {
	if profile := strings.TrimSpace(run.AgentProfile); profile != "" {
		return profile
	}
	if profile := traceRunMetadataString(run, runMetadataAgentProfile); profile != "" {
		return profile
	}
	if taskType := traceRunMetadataString(run, "type"); taskType != "" {
		return taskType
	}
	return "loop"
}

func traceRunRole(run types.RunRecord) string {
	if role := strings.TrimSpace(run.AgentRole); role != "" {
		return role
	}
	if role := traceRunMetadataString(run, runMetadataAgentRole); role != "" {
		return role
	}
	return traceRunProfile(run)
}

func traceTrajectoryTitle(run types.RunRecord, trajectoryID string) string {
	if title := traceExcerpt(run.Prompt, 72); title != "" {
		return title
	}
	if docID := traceRunMetadataString(run, "doc_id"); docID != "" {
		return fmt.Sprintf("vtext %s", shortTraceID(docID))
	}
	return trajectoryID
}

func traceTrajectorySubtitle(run types.RunRecord, leadAgents []string) string {
	if len(leadAgents) > 0 {
		return strings.Join(leadAgents, " + ")
	}
	profile := traceRunProfile(run)
	role := traceRunRole(run)
	if role != "" && role != profile {
		return role + " · " + profile
	}
	return profile
}

func traceExcerpt(text string, max int) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if normalized == "" {
		return ""
	}
	if len(normalized) <= max {
		return normalized
	}
	return normalized[:max-1] + "…"
}

func traceSyntheticAgentID(run types.RunRecord) string {
	if role := traceRunRole(run); role != "" {
		return role + ":" + run.RunID
	}
	return "loop:" + run.RunID
}

func traceAgentID(run types.RunRecord) string {
	if id := strings.TrimSpace(run.AgentID); id != "" {
		return id
	}
	return traceSyntheticAgentID(run)
}

func traceAgentLabel(run types.RunRecord) string {
	role := traceRunRole(run)
	profile := traceRunProfile(run)
	switch {
	case role != "" && profile != "" && role != profile:
		return role + " · " + profile
	case role != "":
		return role
	case profile != "":
		return profile
	default:
		return shortTraceID(traceAgentID(run))
	}
}

func traceRunActivityTime(run types.RunRecord) time.Time {
	if !run.UpdatedAt.IsZero() {
		return run.UpdatedAt
	}
	return run.CreatedAt
}

func formatTraceTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func parseTraceTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ts, _ := time.Parse(time.RFC3339Nano, value)
	return ts
}

func shortTraceID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func traceNonEmpty(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}
