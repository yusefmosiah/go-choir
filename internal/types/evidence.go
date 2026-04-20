package types

import (
	"encoding/json"
	"time"
)

// EvidenceRecord is a durable piece of retrieved or generated material
// captured by an agent. It is intentionally generic so later citation,
// retrieval, and trace flows can interpret it without the schema trying to
// encode higher-level orchestration algorithms.
type EvidenceRecord struct {
	EvidenceID string          `json:"evidence_id"`
	OwnerID    string          `json:"owner_id"`
	AgentID    string          `json:"agent_id"`
	Kind       string          `json:"kind"`
	SourceURI  string          `json:"source_uri,omitempty"`
	Title      string          `json:"title,omitempty"`
	Content    string          `json:"content"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// ResearchFindingRecord captures one researcher-specific "persist evidence, then
// notify the owning agent" dispatch. The finding_id is user/agent supplied so
// retries can be deduplicated without replaying the addressed message.
type ResearchFindingRecord struct {
	FindingID     string    `json:"finding_id"`
	OwnerID       string    `json:"owner_id"`
	AgentID       string    `json:"agent_id"`
	TargetAgentID string    `json:"target_agent_id"`
	ChannelID     string    `json:"channel_id"`
	MessageSeq    int64     `json:"message_seq"`
	TrajectoryID  string    `json:"trajectory_id,omitempty"`
	Findings      []string  `json:"findings,omitempty"`
	EvidenceIDs   []string  `json:"evidence_ids,omitempty"`
	Notes         []string  `json:"notes,omitempty"`
	Questions     []string  `json:"questions,omitempty"`
	Content       string    `json:"content"`
	CreatedAt     time.Time `json:"created_at"`
}
