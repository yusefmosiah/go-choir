package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAuthorKindValid(t *testing.T) {
	validKinds := []AuthorKind{AuthorUser, AuthorAppAgent}
	for _, k := range validKinds {
		if !k.Valid() {
			t.Errorf("expected %q to be valid", k)
		}
	}

	invalidKinds := []AuthorKind{"worker", "system", "", "admin"}
	for _, k := range invalidKinds {
		if k.Valid() {
			t.Errorf("expected %q to be invalid", k)
		}
	}
}

func TestDocumentJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	doc := Document{
		DocID:              "doc-1",
		OwnerID:            "user-1",
		Title:              "Test Document",
		CurrentRevisionID:  "rev-5",
		CreatedAt:          now,
		UpdatedAt:          now.Add(time.Minute),
	}

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}

	var decoded Document
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal document: %v", err)
	}

	if decoded.DocID != doc.DocID {
		t.Errorf("doc_id: got %q, want %q", decoded.DocID, doc.DocID)
	}
	if decoded.OwnerID != doc.OwnerID {
		t.Errorf("owner_id: got %q, want %q", decoded.OwnerID, doc.OwnerID)
	}
	if decoded.Title != doc.Title {
		t.Errorf("title: got %q, want %q", decoded.Title, doc.Title)
	}
	if decoded.CurrentRevisionID != doc.CurrentRevisionID {
		t.Errorf("current_revision_id: got %q, want %q", decoded.CurrentRevisionID, doc.CurrentRevisionID)
	}
}

func TestRevisionJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	citations := json.RawMessage(`[{"id":"c1","type":"url","value":"https://example.com"}]`)
	metadata := json.RawMessage(`{"tags":["draft"]}`)

	rev := Revision{
		RevisionID:       "rev-1",
		DocID:            "doc-1",
		OwnerID:          "user-1",
		AuthorKind:       AuthorUser,
		AuthorLabel:      "alice",
		Content:          "Hello, world!",
		Citations:        citations,
		Metadata:         metadata,
		ParentRevisionID: "",
		CreatedAt:        now,
	}

	data, err := json.Marshal(rev)
	if err != nil {
		t.Fatalf("marshal revision: %v", err)
	}

	var decoded Revision
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal revision: %v", err)
	}

	if decoded.RevisionID != rev.RevisionID {
		t.Errorf("revision_id: got %q, want %q", decoded.RevisionID, rev.RevisionID)
	}
	if decoded.AuthorKind != rev.AuthorKind {
		t.Errorf("author_kind: got %q, want %q", decoded.AuthorKind, rev.AuthorKind)
	}
	if decoded.Content != rev.Content {
		t.Errorf("content: got %q, want %q", decoded.Content, rev.Content)
	}
	if string(decoded.Citations) != string(citations) {
		t.Errorf("citations: got %q, want %q", string(decoded.Citations), string(citations))
	}
	if string(decoded.Metadata) != string(metadata) {
		t.Errorf("metadata: got %q, want %q", string(decoded.Metadata), string(metadata))
	}
}

func TestRevisionAppAgentAttribution(t *testing.T) {
	rev := Revision{
		RevisionID:  "rev-agent-1",
		DocID:       "doc-1",
		OwnerID:     "user-1",
		AuthorKind:  AuthorAppAgent,
		AuthorLabel: "appagent",
		Content:     "AI-generated content",
		CreatedAt:   time.Now().UTC(),
	}

	data, err := json.Marshal(rev)
	if err != nil {
		t.Fatalf("marshal revision: %v", err)
	}

	var decoded Revision
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal revision: %v", err)
	}

	if decoded.AuthorKind != AuthorAppAgent {
		t.Errorf("author_kind: got %q, want %q", decoded.AuthorKind, AuthorAppAgent)
	}
	if decoded.AuthorLabel != "appagent" {
		t.Errorf("author_label: got %q, want %q", decoded.AuthorLabel, "appagent")
	}
}

func TestCitationJSONRoundTrip(t *testing.T) {
	cit := Citation{
		ID:    "c1",
		Type:  "url",
		Value: "https://example.com",
		Label: "Example",
	}

	data, err := json.Marshal(cit)
	if err != nil {
		t.Fatalf("marshal citation: %v", err)
	}

	var decoded Citation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal citation: %v", err)
	}

	if decoded.ID != cit.ID {
		t.Errorf("id: got %q, want %q", decoded.ID, cit.ID)
	}
	if decoded.Type != cit.Type {
		t.Errorf("type: got %q, want %q", decoded.Type, cit.Type)
	}
	if decoded.Value != cit.Value {
		t.Errorf("value: got %q, want %q", decoded.Value, cit.Value)
	}
}

func TestDiffResultJSONRoundTrip(t *testing.T) {
	diff := DiffResult{
		FromRevisionID: "rev-1",
		ToRevisionID:   "rev-2",
		Sections: []DiffSection{
			{
				Type:        "unchanged",
				FromLine:    0,
				ToLine:      0,
				ToLineNum:   0,
				ToEndLine:   0,
				FromContent: "same line",
				ToContent:   "same line",
			},
			{
				Type:       "removed",
				FromLine:   1,
				ToLine:     1,
				ToLineNum:  -1,
				ToEndLine:  -1,
				FromContent: "old line",
			},
			{
				Type:      "added",
				FromLine:  -1,
				ToLine:    -1,
				ToLineNum: 1,
				ToEndLine: 1,
				ToContent: "new line",
			},
		},
		AddedLines:   1,
		RemovedLines: 1,
	}

	data, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("marshal diff result: %v", err)
	}

	var decoded DiffResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal diff result: %v", err)
	}

	if decoded.FromRevisionID != diff.FromRevisionID {
		t.Errorf("from_revision_id: got %q, want %q", decoded.FromRevisionID, diff.FromRevisionID)
	}
	if len(decoded.Sections) != 3 {
		t.Fatalf("sections: got %d, want 3", len(decoded.Sections))
	}
	if decoded.Sections[0].Type != "unchanged" {
		t.Errorf("section[0].type: got %q, want %q", decoded.Sections[0].Type, "unchanged")
	}
}

func TestBlameResultJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	blame := BlameResult{
		RevisionID: "rev-2",
		DocID:      "doc-1",
		Sections: []BlameSection{
			{
				RevisionID: "rev-1",
				AuthorKind: AuthorUser,
				AuthorLabel: "alice",
				StartLine:   0,
				EndLine:     0,
				Content:     "user-written line",
				Timestamp:   now,
			},
			{
				RevisionID: "rev-2",
				AuthorKind: AuthorAppAgent,
				AuthorLabel: "appagent",
				StartLine:   1,
				EndLine:     1,
				Content:     "agent-written line",
				Timestamp:   now.Add(time.Second),
			},
		},
	}

	data, err := json.Marshal(blame)
	if err != nil {
		t.Fatalf("marshal blame result: %v", err)
	}

	var decoded BlameResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal blame result: %v", err)
	}

	if decoded.RevisionID != blame.RevisionID {
		t.Errorf("revision_id: got %q, want %q", decoded.RevisionID, blame.RevisionID)
	}
	if len(decoded.Sections) != 2 {
		t.Fatalf("sections: got %d, want 2", len(decoded.Sections))
	}
	if decoded.Sections[0].AuthorKind != AuthorUser {
		t.Errorf("section[0].author_kind: got %q, want %q", decoded.Sections[0].AuthorKind, AuthorUser)
	}
	if decoded.Sections[1].AuthorKind != AuthorAppAgent {
		t.Errorf("section[1].author_kind: got %q, want %q", decoded.Sections[1].AuthorKind, AuthorAppAgent)
	}
}

func TestHistoryEntryJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	entry := HistoryEntry{
		RevisionID:       "rev-2",
		DocID:            "doc-1",
		AuthorKind:       AuthorAppAgent,
		AuthorLabel:      "appagent",
		CreatedAt:        now,
		Summary:          "Rephrased introduction for clarity",
		ParentRevisionID: "rev-1",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal history entry: %v", err)
	}

	var decoded HistoryEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal history entry: %v", err)
	}

	if decoded.AuthorKind != AuthorAppAgent {
		t.Errorf("author_kind: got %q, want %q", decoded.AuthorKind, AuthorAppAgent)
	}
	if decoded.Summary != entry.Summary {
		t.Errorf("summary: got %q, want %q", decoded.Summary, entry.Summary)
	}
	if decoded.ParentRevisionID != entry.ParentRevisionID {
		t.Errorf("parent_revision_id: got %q, want %q", decoded.ParentRevisionID, entry.ParentRevisionID)
	}
}
