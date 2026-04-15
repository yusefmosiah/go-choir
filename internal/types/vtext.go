// Package types defines the vtext domain types for the go-choir document
// editing system.
//
// These types represent the core vocabulary for the vtext feature: documents,
// revisions, citations, metadata, authorship, and the data structures needed
// for history/snapshot/diff/blame APIs.
//
// Design decisions:
//   - AuthorKind distinguishes user and appagent edits. Users and appagents
//     are peer canonical editors; subordinate workers never become canonical
//     authors (core system invariant).
//   - Revisions are immutable once created. Each revision carries a stable
//     revision ID, author kind, author label, and full document content
//     snapshot. This supports history, snapshot, diff, and blame without
//     needing to reconstruct content from deltas.
//   - Citations and metadata are stored per-document and per-revision so
//     they round-trip through create/edit/history flows and remain available
//     when inspecting a historical revision, not only the current head
//     (VAL-ETEXT-010).
//   - The diff and blame result types are pure value types computed from
//     revision pairs, not persisted independently.
package types

import (
	"encoding/json"
	"time"
)

// AuthorKind distinguishes who created a canonical revision.
// Users and appagents are peer canonical editors; subordinate workers
// never directly become canonical authors.
type AuthorKind string

const (
	// AuthorUser means the revision was created by a direct user edit.
	AuthorUser AuthorKind = "user"

	// AuthorAppAgent means the revision was created by the appagent
	// (an AI-driven canonical edit). Subordinate workers that help
	// produce the change are attributed to the appagent, not to
	// themselves (VAL-CROSS-120).
	AuthorAppAgent AuthorKind = "appagent"
)

// Valid returns true if the AuthorKind value is a recognized kind.
func (a AuthorKind) Valid() bool {
	switch a {
	case AuthorUser, AuthorAppAgent:
		return true
	default:
		return false
	}
}

// Document represents an vtext document with identity, title, current
// content head, and metadata. The document record points to the latest
// revision but retains all historical revisions for history/snapshot/diff/blame.
type Document struct {
	// DocID is the unique stable identifier for this document.
	DocID string `json:"doc_id"`

	// OwnerID is the authenticated user who owns this document.
	OwnerID string `json:"owner_id"`

	// Title is the document title.
	Title string `json:"title"`

	// CurrentRevisionID is the revision ID of the current head revision.
	// Empty when the document is first created before any content is saved.
	CurrentRevisionID string `json:"current_revision_id,omitempty"`

	// CreatedAt is when the document was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the document was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}

// Revision represents an immutable snapshot of document content at a point
// in time. Each revision carries the full document text, citations, and
// metadata, plus authorship attribution.
//
// Revisions are never modified after creation. This makes history,
// snapshot, diff, and blame straightforward: compare any two revisions
// by content, and attribute each revision to its author.
type Revision struct {
	// RevisionID is the unique stable identifier for this revision.
	RevisionID string `json:"revision_id"`

	// DocID is the document this revision belongs to.
	DocID string `json:"doc_id"`

	// OwnerID is the document owner (denormalized for query efficiency).
	OwnerID string `json:"owner_id"`

	// AuthorKind is who created this revision (user or appagent).
	AuthorKind AuthorKind `json:"author_kind"`

	// AuthorLabel is a human-readable label for the author (e.g., the
	// username or "appagent"). This is sufficient to distinguish direct
	// user edits from agent-authored edits in the history view
	// (VAL-ETEXT-006).
	AuthorLabel string `json:"author_label"`

	// Content is the full document text at this revision.
	Content string `json:"content"`

	// Citations is the JSON-encoded citations array at this revision.
	// Citations persist with document history and remain available when
	// inspecting a selected historical revision (VAL-ETEXT-010).
	Citations json.RawMessage `json:"citations,omitempty"`

	// Metadata is the JSON-encoded metadata object at this revision.
	// Metadata persists with document history and remains available when
	// inspecting a selected historical revision (VAL-ETEXT-010).
	Metadata json.RawMessage `json:"metadata,omitempty"`

	// ParentRevisionID is the revision this one was based on. Empty for
	// the first revision of a document.
	ParentRevisionID string `json:"parent_revision_id,omitempty"`

	// CreatedAt is when this revision was created.
	CreatedAt time.Time `json:"created_at"`
}

// Citation represents a single citation attached to a document.
// Citations are stored as JSON arrays in revision records so they
// round-trip through history (VAL-ETEXT-010).
type Citation struct {
	// ID is the citation identifier within the document.
	ID string `json:"id"`

	// Type is the citation type (e.g., "url", "reference", "footnote").
	Type string `json:"type"`

	// Value is the citation content (URL, reference key, footnote text).
	Value string `json:"value"`

	// Label is an optional display label.
	Label string `json:"label,omitempty"`
}

// DiffSection represents a contiguous changed section between two revisions.
// The diff is computed from the content of two revisions and organized into
// sections that show what changed (VAL-ETEXT-008).
type DiffSection struct {
	// Type classifies the change: "added", "removed", or "unchanged".
	Type string `json:"type"`

	// FromLine is the starting line number in the from revision (0-based).
	// -1 for purely added sections.
	FromLine int `json:"from_line"`

	// ToLine is the ending line number in the from revision (0-based, inclusive).
	// -1 for purely added sections.
	ToLine int `json:"to_line"`

	// FromContent is the content from the from revision in this section.
	// Empty for purely added sections.
	FromContent string `json:"from_content,omitempty"`

	// ToLineNum is the starting line number in the to revision (0-based).
	// -1 for purely removed sections.
	ToLineNum int `json:"to_line_num"`

	// ToEndLine is the ending line number in the to revision (0-based, inclusive).
	// -1 for purely removed sections.
	ToEndLine int `json:"to_end_line"`

	// ToContent is the content from the to revision in this section.
	// Empty for purely removed sections.
	ToContent string `json:"to_content,omitempty"`
}

// DiffResult is the result of comparing two revisions.
// It lists the changed sections between the from and to revisions
// (VAL-ETEXT-008).
type DiffResult struct {
	// FromRevisionID is the revision ID of the from side.
	FromRevisionID string `json:"from_revision_id"`

	// ToRevisionID is the revision ID of the to side.
	ToRevisionID string `json:"to_revision_id"`

	// Sections is the list of diff sections showing what changed.
	Sections []DiffSection `json:"sections"`

	// AddedLines is the total number of added lines.
	AddedLines int `json:"added_lines"`

	// RemovedLines is the total number of removed lines.
	RemovedLines int `json:"removed_lines"`
}

// BlameSection represents attribution for a contiguous section of document
// content. The blame view identifies the last editor per section, showing
// whether it was the user or the agent (VAL-ETEXT-009).
type BlameSection struct {
	// RevisionID is the revision that last modified this section.
	RevisionID string `json:"revision_id"`

	// AuthorKind is who made the change (user or appagent).
	AuthorKind AuthorKind `json:"author_kind"`

	// AuthorLabel is the human-readable author label.
	AuthorLabel string `json:"author_label"`

	// StartLine is the starting line of this section (0-based).
	StartLine int `json:"start_line"`

	// EndLine is the ending line of this section (0-based, inclusive).
	EndLine int `json:"end_line"`

	// Content is the text content of this section.
	Content string `json:"content"`

	// Timestamp is when the revision was created.
	Timestamp time.Time `json:"timestamp"`
}

// BlameResult is the result of blame analysis on a document revision.
// It provides section-level attribution that distinguishes whether the
// last editor was the user or the agent (VAL-ETEXT-009).
type BlameResult struct {
	// RevisionID is the revision being blamed.
	RevisionID string `json:"revision_id"`

	// DocID is the document ID.
	DocID string `json:"doc_id"`

	// Sections is the list of blame sections with per-section attribution.
	Sections []BlameSection `json:"sections"`
}

// HistoryEntry represents a single revision entry in the document history.
// It carries enough metadata for the history view to show a chronological
// revision list with explicit attribution (VAL-ETEXT-006).
type HistoryEntry struct {
	// RevisionID is the stable revision identifier.
	RevisionID string `json:"revision_id"`

	// DocID is the document this revision belongs to.
	DocID string `json:"doc_id"`

	// AuthorKind is who created this revision (user or appagent).
	AuthorKind AuthorKind `json:"author_kind"`

	// AuthorLabel is the human-readable label for the author.
	AuthorLabel string `json:"author_label"`

	// CreatedAt is when this revision was created.
	CreatedAt time.Time `json:"created_at"`

	// Summary is an optional short description of the change.
	Summary string `json:"summary,omitempty"`

	// ParentRevisionID is the parent revision ID (for history chain).
	ParentRevisionID string `json:"parent_revision_id,omitempty"`
}
