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
