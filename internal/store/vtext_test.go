package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

func vtextTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vtext-test.db")
	s, err := OpenVTextWorkspace(dbPath)
	if err != nil {
		t.Fatalf("open vtext test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ----- Document CRUD -----

func TestVTextCreateDocument(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Document",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	got, err := s.GetDocument(ctx, "doc-1", "user-1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.DocID != "doc-1" {
		t.Errorf("DocID = %q, want %q", got.DocID, "doc-1")
	}
	if got.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", got.OwnerID, "user-1")
	}
	if got.Title != "Test Document" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Document")
	}
}

func TestVTextGetDocumentOwnerScope(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Owned by user-1",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	// user-2 should not see user-1's document.
	_, err := s.GetDocument(ctx, "doc-1", "user-2")
	if err != ErrNotFound {
		t.Errorf("GetDocument as wrong owner: err=%v, want ErrNotFound", err)
	}
}

func TestVTextListDocumentsByOwner(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		doc := types.Document{
			DocID:   "doc-" + string(rune('a'+i)),
			OwnerID: "user-1",
			Title:   "Doc " + string(rune('a'+i)),
		}
		if err := s.CreateDocument(ctx, doc); err != nil {
			t.Fatalf("CreateDocument: %v", err)
		}
	}
	// Create a doc for another user.
	doc := types.Document{
		DocID:   "doc-x",
		OwnerID: "user-2",
		Title:   "Other User Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	docs, err := s.ListDocumentsByOwner(ctx, "user-1", 10)
	if err != nil {
		t.Fatalf("ListDocumentsByOwner: %v", err)
	}
	if len(docs) != 3 {
		t.Errorf("len(docs) = %d, want 3", len(docs))
	}
}

func TestVTextUpdateDocument(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Original Title",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	doc.Title = "Updated Title"
	doc.CurrentRevisionID = "rev-1"
	if err := s.UpdateDocument(ctx, doc); err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}

	got, err := s.GetDocument(ctx, "doc-1", "user-1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", got.Title, "Updated Title")
	}
	if got.CurrentRevisionID != "rev-1" {
		t.Errorf("CurrentRevisionID = %q, want %q", got.CurrentRevisionID, "rev-1")
	}
}

func TestVTextDeleteDocument(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "To Delete",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	if err := s.DeleteDocument(ctx, "doc-1", "user-1"); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	_, err := s.GetDocument(ctx, "doc-1", "user-1")
	if err != ErrNotFound {
		t.Errorf("GetDocument after delete: err=%v, want ErrNotFound", err)
	}
}

// ----- Revision CRUD -----

func TestVTextCreateRevision(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	// Create a document first.
	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	citations, _ := json.Marshal([]types.Citation{
		{ID: "c1", Type: "url", Value: "https://example.com", Label: "Example"},
	})
	metadata, _ := json.Marshal(map[string]any{"tags": []string{"draft"}})

	rev := types.Revision{
		RevisionID:  "rev-1",
		DocID:       "doc-1",
		OwnerID:     "user-1",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Content:     "Hello, world!",
		Citations:   citations,
		Metadata:    metadata,
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}

	got, err := s.GetRevision(ctx, "rev-1", "user-1")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if got.RevisionID != "rev-1" {
		t.Errorf("RevisionID = %q, want %q", got.RevisionID, "rev-1")
	}
	if got.AuthorKind != types.AuthorUser {
		t.Errorf("AuthorKind = %q, want %q", got.AuthorKind, types.AuthorUser)
	}
	if got.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", got.Content, "Hello, world!")
	}
	if got.AuthorLabel != "alice" {
		t.Errorf("AuthorLabel = %q, want %q", got.AuthorLabel, "alice")
	}
}

func TestVTextRevisionOwnerScope(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Owned by user-1",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	rev := types.Revision{
		RevisionID:  "rev-1",
		DocID:       "doc-1",
		OwnerID:     "user-1",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Content:     "Content",
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}

	// user-2 should not see user-1's revision.
	_, err := s.GetRevision(ctx, "rev-1", "user-2")
	if err != ErrNotFound {
		t.Errorf("GetRevision as wrong owner: err=%v, want ErrNotFound", err)
	}
}

func TestVTextListRevisionsByDoc(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	// Create 3 revisions with different authors.
	for i := 0; i < 3; i++ {
		authorKind := types.AuthorUser
		authorLabel := "alice"
		if i == 1 {
			authorKind = types.AuthorAppAgent
			authorLabel = "appagent"
		}
		rev := types.Revision{
			RevisionID:       "rev-" + string(rune('1'+i)),
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       authorKind,
			AuthorLabel:      authorLabel,
			Content:          "Content v" + string(rune('1'+i)),
			ParentRevisionID: "",
			CreatedAt:        time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if i > 0 {
			rev.ParentRevisionID = "rev-" + string(rune('0'+i))
		}
		if err := s.CreateRevision(ctx, rev); err != nil {
			t.Fatalf("CreateRevision %d: %v", i, err)
		}
	}

	revs, err := s.ListRevisionsByDoc(ctx, "doc-1", "user-1", 10)
	if err != nil {
		t.Fatalf("ListRevisionsByDoc: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("len(revs) = %d, want 3", len(revs))
	}

	// Should be ordered by created_at descending (newest first).
	if revs[0].RevisionID != "rev-3" {
		t.Errorf("first rev = %q, want %q", revs[0].RevisionID, "rev-3")
	}

	// Check attribution: user, appagent, user.
	if revs[2].AuthorKind != types.AuthorUser || revs[1].AuthorKind != types.AuthorAppAgent || revs[0].AuthorKind != types.AuthorUser {
		t.Errorf("author kinds = %v, %v, %v; want user, appagent, user", revs[2].AuthorKind, revs[1].AuthorKind, revs[0].AuthorKind)
	}
}

func TestVTextListRevisionsByDocOwnerScope(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Owned by user-1",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	rev := types.Revision{
		RevisionID:  "rev-1",
		DocID:       "doc-1",
		OwnerID:     "user-1",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Content:     "Content",
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}

	// user-2 should not see user-1's revisions.
	revs, err := s.ListRevisionsByDoc(ctx, "doc-1", "user-2", 10)
	if err != nil {
		t.Fatalf("ListRevisionsByDoc: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("len(revs) = %d, want 0 for wrong owner", len(revs))
	}
}

// ----- History -----

func TestVTextGetHistory(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	// Create revisions with parent chain.
	now := time.Now().UTC().Truncate(time.Millisecond)
	revs := []types.Revision{
		{
			RevisionID:  "rev-1",
			DocID:       "doc-1",
			OwnerID:     "user-1",
			AuthorKind:  types.AuthorUser,
			AuthorLabel: "alice",
			Content:     "First draft",
			CreatedAt:   now,
		},
		{
			RevisionID:       "rev-2",
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       types.AuthorAppAgent,
			AuthorLabel:      "appagent",
			Content:          "AI-improved draft",
			ParentRevisionID: "rev-1",
			CreatedAt:        now.Add(time.Second),
		},
		{
			RevisionID:       "rev-3",
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       types.AuthorUser,
			AuthorLabel:      "alice",
			Content:          "User edited",
			ParentRevisionID: "rev-2",
			CreatedAt:        now.Add(2 * time.Second),
		},
	}
	for _, r := range revs {
		if err := s.CreateRevision(ctx, r); err != nil {
			t.Fatalf("CreateRevision: %v", err)
		}
	}

	history, err := s.GetHistory(ctx, "doc-1", "user-1", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("len(history) = %d, want 3", len(history))
	}

	// Should be newest first.
	if history[0].RevisionID != "rev-3" {
		t.Errorf("first entry = %q, want %q", history[0].RevisionID, "rev-3")
	}
	// Check attribution metadata is present.
	if history[0].AuthorKind != types.AuthorUser {
		t.Errorf("first entry AuthorKind = %q, want %q", history[0].AuthorKind, types.AuthorUser)
	}
	if history[1].AuthorKind != types.AuthorAppAgent {
		t.Errorf("second entry AuthorKind = %q, want %q", history[1].AuthorKind, types.AuthorAppAgent)
	}
	// Check parent revision chain.
	if history[0].ParentRevisionID != "rev-2" {
		t.Errorf("first entry ParentRevisionID = %q, want %q", history[0].ParentRevisionID, "rev-2")
	}
}

// ----- Diff -----

func TestVTextGetDiff(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	now := time.Now().UTC()
	revs := []types.Revision{
		{
			RevisionID:  "rev-1",
			DocID:       "doc-1",
			OwnerID:     "user-1",
			AuthorKind:  types.AuthorUser,
			AuthorLabel: "alice",
			Content:     "line1\nline2\nline3\n",
			CreatedAt:   now,
		},
		{
			RevisionID:       "rev-2",
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       types.AuthorAppAgent,
			AuthorLabel:      "appagent",
			Content:          "line1\nline2-modified\nline3\nline4-added\n",
			ParentRevisionID: "rev-1",
			CreatedAt:        now.Add(time.Second),
		},
	}
	for _, r := range revs {
		if err := s.CreateRevision(ctx, r); err != nil {
			t.Fatalf("CreateRevision: %v", err)
		}
	}

	diff, err := s.GetDiff(ctx, "rev-1", "rev-2", "user-1")
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if diff.FromRevisionID != "rev-1" {
		t.Errorf("FromRevisionID = %q, want %q", diff.FromRevisionID, "rev-1")
	}
	if diff.ToRevisionID != "rev-2" {
		t.Errorf("ToRevisionID = %q, want %q", diff.ToRevisionID, "rev-2")
	}
	// There should be some change detected.
	if len(diff.Sections) == 0 {
		t.Error("no diff sections detected")
	}
	if diff.AddedLines == 0 && diff.RemovedLines == 0 {
		t.Error("no lines added or removed")
	}
}

// ----- Blame -----

func TestVTextGetBlame(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	now := time.Now().UTC()
	revs := []types.Revision{
		{
			RevisionID:  "rev-1",
			DocID:       "doc-1",
			OwnerID:     "user-1",
			AuthorKind:  types.AuthorUser,
			AuthorLabel: "alice",
			Content:     "line1\nline2\nline3\n",
			CreatedAt:   now,
		},
		{
			RevisionID:       "rev-2",
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       types.AuthorAppAgent,
			AuthorLabel:      "appagent",
			Content:          "line1\nline2-modified\nline3\n",
			ParentRevisionID: "rev-1",
			CreatedAt:        now.Add(time.Second),
		},
	}
	for _, r := range revs {
		if err := s.CreateRevision(ctx, r); err != nil {
			t.Fatalf("CreateRevision: %v", err)
		}
	}

	blame, err := s.GetBlame(ctx, "rev-2", "user-1")
	if err != nil {
		t.Fatalf("GetBlame: %v", err)
	}
	if blame.RevisionID != "rev-2" {
		t.Errorf("RevisionID = %q, want %q", blame.RevisionID, "rev-2")
	}
	if blame.DocID != "doc-1" {
		t.Errorf("DocID = %q, want %q", blame.DocID, "doc-1")
	}
	if len(blame.Sections) == 0 {
		t.Error("no blame sections")
	}

	// Verify that sections have different author kinds.
	hasUser := false
	hasAgent := false
	for _, sec := range blame.Sections {
		if sec.AuthorKind == types.AuthorUser {
			hasUser = true
		}
		if sec.AuthorKind == types.AuthorAppAgent {
			hasAgent = true
		}
	}
	if !hasUser || !hasAgent {
		t.Errorf("blame should contain both user and appagent sections; hasUser=%v, hasAgent=%v", hasUser, hasAgent)
	}
}

// ----- Citations and Metadata persistence -----

func TestVTextCitationsMetadataRoundTrip(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Test Doc",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	citations := []types.Citation{
		{ID: "c1", Type: "url", Value: "https://example.com", Label: "Example"},
		{ID: "c2", Type: "footnote", Value: "See page 5"},
	}
	citJSON, _ := json.Marshal(citations)
	metaJSON, _ := json.Marshal(map[string]any{
		"tags":    []string{"draft", "important"},
		"version": 2,
	})

	rev := types.Revision{
		RevisionID:  "rev-1",
		DocID:       "doc-1",
		OwnerID:     "user-1",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Content:     "Document with citations",
		Citations:   citJSON,
		Metadata:    metaJSON,
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}

	got, err := s.GetRevision(ctx, "rev-1", "user-1")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}

	// Verify citations round-trip.
	var gotCitations []types.Citation
	if err := json.Unmarshal(got.Citations, &gotCitations); err != nil {
		t.Fatalf("unmarshal citations: %v", err)
	}
	if len(gotCitations) != 2 {
		t.Errorf("len(citations) = %d, want 2", len(gotCitations))
	}
	if gotCitations[0].Value != "https://example.com" {
		t.Errorf("citation[0].Value = %q, want %q", gotCitations[0].Value, "https://example.com")
	}

	// Verify metadata round-trip.
	var gotMeta map[string]any
	if err := json.Unmarshal(got.Metadata, &gotMeta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if gotMeta["version"] != float64(2) {
		t.Errorf("metadata.version = %v, want 2", gotMeta["version"])
	}
}

// ----- Snapshot (open historical revision without mutating head) -----

func TestVTextSnapshotDoesNotMutateHead(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:             "doc-1",
		OwnerID:           "user-1",
		Title:             "Test Doc",
		CurrentRevisionID: "rev-2",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	now := time.Now().UTC()
	revs := []types.Revision{
		{
			RevisionID:  "rev-1",
			DocID:       "doc-1",
			OwnerID:     "user-1",
			AuthorKind:  types.AuthorUser,
			AuthorLabel: "alice",
			Content:     "Old content",
			CreatedAt:   now,
		},
		{
			RevisionID:       "rev-2",
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       types.AuthorUser,
			AuthorLabel:      "alice",
			Content:          "New content",
			ParentRevisionID: "rev-1",
			CreatedAt:        now.Add(time.Second),
		},
	}
	for _, r := range revs {
		if err := s.CreateRevision(ctx, r); err != nil {
			t.Fatalf("CreateRevision: %v", err)
		}
	}

	// Open the old revision (snapshot).
	snapshot, err := s.GetRevision(ctx, "rev-1", "user-1")
	if err != nil {
		t.Fatalf("GetRevision (snapshot): %v", err)
	}
	if snapshot.Content != "Old content" {
		t.Errorf("snapshot content = %q, want %q", snapshot.Content, "Old content")
	}

	// Verify head is unchanged.
	got, err := s.GetDocument(ctx, "doc-1", "user-1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.CurrentRevisionID != "rev-2" {
		t.Errorf("CurrentRevisionID after snapshot = %q, want %q", got.CurrentRevisionID, "rev-2")
	}
}

// ----- Workspace setup -----

func TestVTextInitWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsPath := filepath.Join(dir, "workspace.db")

	s, err := OpenVTextWorkspace(wsPath)
	if err != nil {
		t.Fatalf("OpenVTextWorkspace: %v", err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()

	// Verify the vtext schema is applied by creating a document.
	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Workspace Test",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument in workspace: %v", err)
	}

	got, err := s.GetDocument(ctx, "doc-1", "user-1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.DocID != "doc-1" {
		t.Errorf("DocID = %q, want %q", got.DocID, "doc-1")
	}

	// Verify the workspace directory exists.
	if _, err := os.Stat(s.VTextPath()); os.IsNotExist(err) {
		t.Errorf("workspace directory %q was not created", s.VTextPath())
	}
}

// ----- Diff owner scope -----

func TestVTextDiffOwnerScope(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Owned by user-1",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	now := time.Now().UTC()
	revs := []types.Revision{
		{
			RevisionID:  "rev-1",
			DocID:       "doc-1",
			OwnerID:     "user-1",
			AuthorKind:  types.AuthorUser,
			AuthorLabel: "alice",
			Content:     "Content A",
			CreatedAt:   now,
		},
		{
			RevisionID:       "rev-2",
			DocID:            "doc-1",
			OwnerID:          "user-1",
			AuthorKind:       types.AuthorAppAgent,
			AuthorLabel:      "appagent",
			Content:          "Content B",
			ParentRevisionID: "rev-1",
			CreatedAt:        now.Add(time.Second),
		},
	}
	for _, r := range revs {
		if err := s.CreateRevision(ctx, r); err != nil {
			t.Fatalf("CreateRevision: %v", err)
		}
	}

	// user-2 should not be able to diff user-1's revisions.
	_, err := s.GetDiff(ctx, "rev-1", "rev-2", "user-2")
	if err == nil {
		t.Error("GetDiff as wrong owner: expected error, got nil")
	}
}

// ----- Blame owner scope -----

func TestVTextBlameOwnerScope(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	doc := types.Document{
		DocID:   "doc-1",
		OwnerID: "user-1",
		Title:   "Owned by user-1",
	}
	if err := s.CreateDocument(ctx, doc); err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	rev := types.Revision{
		RevisionID:  "rev-1",
		DocID:       "doc-1",
		OwnerID:     "user-1",
		AuthorKind:  types.AuthorUser,
		AuthorLabel: "alice",
		Content:     "Content",
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.CreateRevision(ctx, rev); err != nil {
		t.Fatalf("CreateRevision: %v", err)
	}

	// user-2 should not be able to blame user-1's revision.
	_, err := s.GetBlame(ctx, "rev-1", "user-2")
	if err != ErrNotFound {
		t.Errorf("GetBlame as wrong owner: err=%v, want ErrNotFound", err)
	}
}

// ----- Agent mutation tracking tests -----

func TestVTextAgentMutationCreateAndGet(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("CreateAgentMutation: %v", err)
	}

	got, err := s.GetPendingAgentMutationByDoc(ctx, "doc-1", "user-1")
	if err != nil {
		t.Fatalf("GetPendingAgentMutationByDoc: %v", err)
	}
	if got == nil {
		t.Fatal("GetPendingAgentMutationByDoc returned nil")
	}
	if got.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", got.TaskID, "task-1")
	}
	if got.State != "pending" {
		t.Errorf("State = %q, want %q", got.State, "pending")
	}
}

func TestVTextAgentMutationByTask(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("CreateAgentMutation: %v", err)
	}

	got, err := s.GetAgentMutationByTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetAgentMutationByTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetAgentMutationByTask returned nil")
	}
	if got.DocID != "doc-1" {
		t.Errorf("DocID = %q, want %q", got.DocID, "doc-1")
	}
}

func TestVTextAgentMutationComplete(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("CreateAgentMutation: %v", err)
	}

	// Complete the mutation.
	if err := s.CompleteAgentMutation(ctx, "task-1", "rev-agent-1"); err != nil {
		t.Fatalf("CompleteAgentMutation: %v", err)
	}

	// Verify the mutation is now completed.
	got, err := s.GetAgentMutationByTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetAgentMutationByTask: %v", err)
	}
	if got.State != "completed" {
		t.Errorf("State = %q, want %q", got.State, "completed")
	}
	if got.RevisionID != "rev-agent-1" {
		t.Errorf("RevisionID = %q, want %q", got.RevisionID, "rev-agent-1")
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt is nil, want a timestamp")
	}

	// No pending mutation should be found for this doc.
	pending, err := s.GetPendingAgentMutationByDoc(ctx, "doc-1", "user-1")
	if err != nil {
		t.Fatalf("GetPendingAgentMutationByDoc: %v", err)
	}
	if pending != nil {
		t.Error("pending mutation should be nil after completion")
	}
}

func TestVTextAgentMutationNoDuplicateOnCompletion(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("CreateAgentMutation: %v", err)
	}

	// Complete once.
	if err := s.CompleteAgentMutation(ctx, "task-1", "rev-agent-1"); err != nil {
		t.Fatalf("first CompleteAgentMutation: %v", err)
	}

	// Try to complete again — should fail with ErrMutationAlreadyCompleted.
	err := s.CompleteAgentMutation(ctx, "task-1", "rev-agent-2")
	if err != ErrMutationAlreadyCompleted {
		t.Errorf("second CompleteAgentMutation: err=%v, want ErrMutationAlreadyCompleted", err)
	}

	// Verify only the first revision ID was saved.
	got, _ := s.GetAgentMutationByTask(ctx, "task-1")
	if got.RevisionID != "rev-agent-1" {
		t.Errorf("RevisionID = %q, want %q (should not be overwritten by second completion)", got.RevisionID, "rev-agent-1")
	}
}

func TestVTextAgentMutationIdempotentCreation(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("first CreateAgentMutation: %v", err)
	}

	// Creating the same mutation again should succeed (INSERT OR IGNORE).
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("duplicate CreateAgentMutation: %v", err)
	}
}

func TestVTextAgentMutationFail(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("CreateAgentMutation: %v", err)
	}

	if err := s.FailAgentMutation(ctx, "task-1"); err != nil {
		t.Fatalf("FailAgentMutation: %v", err)
	}

	got, _ := s.GetAgentMutationByTask(ctx, "task-1")
	if got.State != "failed" {
		t.Errorf("State = %q, want %q", got.State, "failed")
	}
}

func TestVTextAgentMutationNoCrossUserAccess(t *testing.T) {
	s := vtextTestStore(t)
	ctx := context.Background()

	m := AgentMutation{
		DocID:     "doc-1",
		TaskID:    "task-1",
		OwnerID:   "user-1",
		State:     "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAgentMutation(ctx, m); err != nil {
		t.Fatalf("CreateAgentMutation: %v", err)
	}

	// user-2 should not see user-1's pending mutation.
	got, err := s.GetPendingAgentMutationByDoc(ctx, "doc-1", "user-2")
	if err != nil {
		t.Fatalf("GetPendingAgentMutationByDoc as user-2: %v", err)
	}
	if got != nil {
		t.Error("user-2 should not see user-1's pending mutation")
	}
}
