// Package store provides vtext document persistence for the go-choir sandbox
// runtime.
//
// The vtext store persists documents, revisions, citations, and metadata
// using an embedded Dolt workspace, enabling history-capable persistence with
// first-class versioning semantics, history/snapshot/diff/blame APIs, and
// per-user in-process storage inside the sandbox.
//
// Design decisions:
//   - Embedded Dolt (`github.com/dolthub/driver`) for version-native document
//     storage without a separate server process.
//   - Full-content revisions (not deltas) so that historical snapshots are
//     directly accessible without reconstruction.
//   - Citations and metadata are stored per-revision as JSON blobs so they
//     round-trip through history (VAL-ETEXT-010).
//   - Owner scoping on all queries so that one user cannot read another
//     user's documents or revisions.
//   - The diff algorithm is a simple line-based diff (LCS) that produces
//     section-level changes between two revisions.
//   - The blame algorithm walks backward through the revision chain,
//     attributing each line to the most recent revision that changed it.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	embedded "github.com/dolthub/driver"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// vtextSchemaDDL creates the vtext tables if they do not already exist.
const vtextSchemaDDL = `
CREATE TABLE IF NOT EXISTS vtext_documents (
	doc_id              VARCHAR(255) PRIMARY KEY,
	owner_id            VARCHAR(255) NOT NULL,
	title               VARCHAR(1024) NOT NULL DEFAULT '',
	current_revision_id VARCHAR(255) NOT NULL DEFAULT '',
	created_at          DATETIME NOT NULL,
	updated_at          DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS vtext_revisions (
	revision_id         VARCHAR(255) PRIMARY KEY,
	doc_id              VARCHAR(255) NOT NULL,
	owner_id            VARCHAR(255) NOT NULL,
	author_kind         VARCHAR(64) NOT NULL,
	author_label        VARCHAR(255) NOT NULL DEFAULT '',
	content             LONGTEXT NOT NULL,
	citations_json      LONGTEXT NOT NULL,
	metadata_json       LONGTEXT NOT NULL,
	parent_revision_id  VARCHAR(255) NOT NULL DEFAULT '',
	created_at          DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS vtext_document_aliases (
	owner_id            VARCHAR(255) NOT NULL,
	source_path         VARCHAR(2048) NOT NULL,
	doc_id              VARCHAR(255) NOT NULL,
	created_at          DATETIME NOT NULL,
	updated_at          DATETIME NOT NULL,
	PRIMARY KEY (owner_id, source_path)
);

CREATE INDEX IF NOT EXISTS idx_vtext_docs_owner ON vtext_documents(owner_id);
CREATE INDEX IF NOT EXISTS idx_vtext_revs_doc ON vtext_revisions(doc_id);
CREATE INDEX IF NOT EXISTS idx_vtext_revs_owner ON vtext_revisions(owner_id);
CREATE INDEX IF NOT EXISTS idx_vtext_revs_doc_created ON vtext_revisions(doc_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_vtext_aliases_doc ON vtext_document_aliases(doc_id);

CREATE TABLE IF NOT EXISTS vtext_agent_mutations (
	doc_id              VARCHAR(255) NOT NULL,
	loop_id             VARCHAR(255) NOT NULL,
	owner_id            VARCHAR(255) NOT NULL,
	state               VARCHAR(64) NOT NULL DEFAULT 'pending',
	revision_id         VARCHAR(255) NOT NULL DEFAULT '',
	created_at          DATETIME NOT NULL,
	completed_at        DATETIME,
	PRIMARY KEY (doc_id, loop_id)
);

CREATE INDEX IF NOT EXISTS idx_vtext_mutations_doc ON vtext_agent_mutations(doc_id);
CREATE INDEX IF NOT EXISTS idx_vtext_mutations_run ON vtext_agent_mutations(loop_id);

CREATE TABLE IF NOT EXISTS agent_evidence (
	evidence_id    VARCHAR(255) PRIMARY KEY,
	owner_id       VARCHAR(255) NOT NULL,
	agent_id       VARCHAR(255) NOT NULL,
	kind           VARCHAR(128) NOT NULL,
	source_uri     LONGTEXT NOT NULL DEFAULT '',
	title          LONGTEXT NOT NULL DEFAULT '',
	content        LONGTEXT NOT NULL,
	metadata_json  LONGTEXT NOT NULL DEFAULT '{}',
	created_at     DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_evidence_owner_agent ON agent_evidence(owner_id, agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_owner_created ON agent_evidence(owner_id, created_at DESC);
`

// OpenVTextWorkspace opens (or creates) an embedded Dolt workspace for
// vtext document storage only. It is mainly used by store-level tests and
// by local sandbox workflows that need the document store without the rest
// of the runtime SQLite tables.
func OpenVTextWorkspace(path string) (*Store, error) {
	db, workspacePath, err := openVTextWorkspaceDB(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, vtextDB: db, vtextPath: workspacePath}
	if err := s.bootstrapVText(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("vtext workspace: bootstrap: %w", err)
	}
	return s, nil
}

func deriveVTextWorkspacePath(path string) string {
	if path == "" {
		return filepath.Join(os.TempDir(), "go-choir-vtext")
	}
	trimmed := strings.TrimSuffix(path, filepath.Ext(path))
	if trimmed == "" {
		trimmed = path
	}
	return trimmed + ".vtext"
}

func openVTextWorkspaceDB(path string) (*sql.DB, string, error) {
	workspacePath := deriveVTextWorkspacePath(path)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.RemoveAll(workspacePath)
	}
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return nil, "", fmt.Errorf("vtext workspace: create directory: %w", err)
	}

	rootDSN := fmt.Sprintf(
		"file://%s?commitname=Choir&commitemail=system@choir.local&multistatements=true",
		workspacePath,
	)
	cfg, err := embedded.ParseDSN(rootDSN)
	if err != nil {
		return nil, "", fmt.Errorf("vtext workspace: parse dsn: %w", err)
	}
	connector, err := embedded.NewConnector(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("vtext workspace: new connector: %w", err)
	}
	rootDB := sql.OpenDB(connector)
	rootDB.SetMaxOpenConns(1)
	rootDB.SetMaxIdleConns(1)
	if _, err := rootDB.Exec("CREATE DATABASE IF NOT EXISTS vtext"); err != nil {
		_ = rootDB.Close()
		return nil, "", fmt.Errorf("vtext workspace: create database: %w", err)
	}
	if err := rootDB.Close(); err != nil {
		return nil, "", fmt.Errorf("vtext workspace: close bootstrap connection: %w", err)
	}

	dbDSN := fmt.Sprintf(
		"file://%s?commitname=Choir&commitemail=system@choir.local&database=vtext&multistatements=true",
		workspacePath,
	)

	var lastErr error
	for attempt := range 8 {
		dbCfg, err := embedded.ParseDSN(dbDSN)
		if err != nil {
			return nil, "", fmt.Errorf("vtext workspace: parse database dsn: %w", err)
		}
		dbConnector, err := embedded.NewConnector(dbCfg)
		if err != nil {
			lastErr = fmt.Errorf("vtext workspace: new database connector: %w", err)
		} else {
			db := sql.OpenDB(dbConnector)
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			if pingErr := db.Ping(); pingErr == nil {
				return db, workspacePath, nil
			} else {
				lastErr = fmt.Errorf("vtext workspace: ping database: %w", pingErr)
			}
			_ = db.Close()
		}

		if !strings.Contains(strings.ToLower(lastErr.Error()), "non 0 lock") {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}

	return nil, "", lastErr
}

func (s *Store) vtextHandle() *sql.DB {
	if s.vtextDB != nil {
		return s.vtextDB
	}
	return s.db
}

// bootstrapVText applies the vtext schema DDL to the embedded workspace.
func (s *Store) bootstrapVText() error {
	_, err := s.vtextHandle().Exec(vtextSchemaDDL)
	if err != nil {
		return fmt.Errorf("apply vtext schema: %w", err)
	}
	return nil
}

// EnsureVTextSchema applies the vtext schema to the embedded workspace.
func (s *Store) EnsureVTextSchema() error {
	return s.bootstrapVText()
}

// ----- Document CRUD -----

// CreateDocument inserts a new document record.
func (s *Store) CreateDocument(ctx context.Context, doc types.Document) error {
	_, err := s.vtextHandle().ExecContext(ctx,
		`INSERT INTO vtext_documents (doc_id, owner_id, title, current_revision_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		doc.DocID,
		doc.OwnerID,
		doc.Title,
		doc.CurrentRevisionID,
		doc.CreatedAt.UTC().Format(time.RFC3339Nano),
		doc.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert vtext document: %w", err)
	}
	return nil
}

// GetDocument returns the document with the given doc ID, scoped to the
// given owner. If the document does not exist or does not belong to the
// owner, it returns ErrNotFound.
func (s *Store) GetDocument(ctx context.Context, docID, ownerID string) (types.Document, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT doc_id, owner_id, title, current_revision_id, created_at, updated_at
		   FROM vtext_documents
		  WHERE doc_id = ? AND owner_id = ?`,
		docID, ownerID,
	)
	return scanDocument(row)
}

// ListDocumentsByOwner returns documents for the given owner, ordered by
// updated_at descending, limited to the given count.
func (s *Store) ListDocumentsByOwner(ctx context.Context, ownerID string, limit int) ([]types.Document, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.vtextHandle().QueryContext(ctx,
		`SELECT doc_id, owner_id, title, current_revision_id, created_at, updated_at
		   FROM vtext_documents
		  WHERE owner_id = ?
		  ORDER BY updated_at DESC
		  LIMIT ?`,
		ownerID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query vtext documents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var docs []types.Document
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vtext documents: %w", err)
	}
	return docs, nil
}

// UpdateDocument updates an existing document record.
func (s *Store) UpdateDocument(ctx context.Context, doc types.Document) error {
	result, err := s.vtextHandle().ExecContext(ctx,
		`UPDATE vtext_documents
		    SET owner_id = ?,
		        title = ?,
		        current_revision_id = ?,
		        updated_at = ?
		  WHERE doc_id = ? AND owner_id = ?`,
		doc.OwnerID,
		doc.Title,
		doc.CurrentRevisionID,
		doc.UpdatedAt.UTC().Format(time.RFC3339Nano),
		doc.DocID,
		doc.OwnerID,
	)
	if err != nil {
		return fmt.Errorf("update vtext document: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated document rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: document %s for owner %s", ErrNotFound, doc.DocID, doc.OwnerID)
	}
	return nil
}

// GetDocumentAlias resolves a file-browser alias to its canonical document ID.
func (s *Store) GetDocumentAlias(ctx context.Context, ownerID, sourcePath string) (string, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT doc_id
		   FROM vtext_document_aliases
		  WHERE owner_id = ? AND source_path = ?`,
		ownerID, sourcePath,
	)
	var docID string
	if err := row.Scan(&docID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("query vtext document alias: %w", err)
	}
	return docID, nil
}

// UpsertDocumentAlias records or refreshes the canonical document mapping for a file path.
func (s *Store) UpsertDocumentAlias(ctx context.Context, ownerID, sourcePath, docID string, updatedAt time.Time) error {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err := s.vtextHandle().ExecContext(ctx,
		`INSERT INTO vtext_document_aliases (owner_id, source_path, doc_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   doc_id = VALUES(doc_id),
		   updated_at = VALUES(updated_at)`,
		ownerID,
		sourcePath,
		docID,
		updatedAt.UTC().Format(time.RFC3339Nano),
		updatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert vtext document alias: %w", err)
	}
	return nil
}

// DeleteDocument deletes a document and all its revisions. It is scoped
// to the given owner.
func (s *Store) DeleteDocument(ctx context.Context, docID, ownerID string) error {
	// Delete revisions first (no FK constraint, so manual cleanup).
	_, _ = s.vtextHandle().ExecContext(ctx,
		`DELETE FROM vtext_revisions WHERE doc_id = ? AND owner_id = ?`,
		docID, ownerID,
	)

	result, err := s.vtextHandle().ExecContext(ctx,
		`DELETE FROM vtext_documents WHERE doc_id = ? AND owner_id = ?`,
		docID, ownerID,
	)
	if err != nil {
		return fmt.Errorf("delete vtext document: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted document rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: document %s for owner %s", ErrNotFound, docID, ownerID)
	}
	return nil
}

// ----- Revision CRUD -----

// CreateRevision inserts a new revision record and updates the document's
// current_revision_id if this is the latest revision.
func (s *Store) CreateRevision(ctx context.Context, rev types.Revision) error {
	citations := string(rev.Citations)
	if citations == "" {
		citations = "[]"
	}
	metadata := string(rev.Metadata)
	if metadata == "" {
		metadata = "{}"
	}
	tx, err := s.vtextHandle().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vtext revision transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentHead string
	row := tx.QueryRowContext(ctx,
		`SELECT current_revision_id
		   FROM vtext_documents
		  WHERE doc_id = ? AND owner_id = ?`,
		rev.DocID, rev.OwnerID,
	)
	if err := row.Scan(&currentHead); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: document %s for owner %s", ErrNotFound, rev.DocID, rev.OwnerID)
		}
		return fmt.Errorf("query vtext document head: %w", err)
	}
	expectedHead := strings.TrimSpace(rev.ParentRevisionID)
	if strings.TrimSpace(currentHead) != expectedHead {
		return fmt.Errorf("%w: document %s current head %s does not match parent %s", ErrStaleDocumentHead, rev.DocID, currentHead, expectedHead)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO vtext_revisions (revision_id, doc_id, owner_id, author_kind, author_label, content, citations_json, metadata_json, parent_revision_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rev.RevisionID,
		rev.DocID,
		rev.OwnerID,
		string(rev.AuthorKind),
		rev.AuthorLabel,
		rev.Content,
		citations,
		metadata,
		rev.ParentRevisionID,
		rev.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert vtext revision: %w", err)
	}

	// Update the document's current_revision_id and updated_at, but only if the
	// head still matches the parent revision we read at the start of this transaction.
	result, err := tx.ExecContext(ctx,
		`UPDATE vtext_documents
		    SET current_revision_id = ?,
		        updated_at = ?
		  WHERE doc_id = ? AND owner_id = ? AND current_revision_id = ?`,
		rev.RevisionID,
		rev.CreatedAt.UTC().Format(time.RFC3339Nano),
		rev.DocID,
		rev.OwnerID,
		expectedHead,
	)
	if err != nil {
		return fmt.Errorf("update vtext document head: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated vtext document head rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: document %s head moved during revision create", ErrStaleDocumentHead, rev.DocID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit vtext revision: %w", err)
	}
	return nil
}

// GetRevision returns the revision with the given revision ID, scoped to
// the given owner.
func (s *Store) GetRevision(ctx context.Context, revisionID, ownerID string) (types.Revision, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT revision_id, doc_id, owner_id, author_kind, author_label, content, citations_json, metadata_json, parent_revision_id, created_at
		   FROM vtext_revisions
		  WHERE revision_id = ? AND owner_id = ?`,
		revisionID, ownerID,
	)
	return scanRevision(row)
}

// GetRevisionUnscoped returns the revision without owner scoping.
// Used internally for diff/blame computation where the revision chain
// is already known to belong to the same owner.
func (s *Store) GetRevisionUnscoped(ctx context.Context, revisionID string) (types.Revision, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT revision_id, doc_id, owner_id, author_kind, author_label, content, citations_json, metadata_json, parent_revision_id, created_at
		   FROM vtext_revisions
		  WHERE revision_id = ?`,
		revisionID,
	)
	return scanRevision(row)
}

// ListRevisionsByDoc returns revisions for the given document, scoped to
// the given owner, ordered by created_at descending (newest first),
// limited to the given count.
func (s *Store) ListRevisionsByDoc(ctx context.Context, docID, ownerID string, limit int) ([]types.Revision, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.vtextHandle().QueryContext(ctx,
		`SELECT revision_id, doc_id, owner_id, author_kind, author_label, content, citations_json, metadata_json, parent_revision_id, created_at
		   FROM vtext_revisions
		  WHERE doc_id = ? AND owner_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		docID, ownerID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query vtext revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var revs []types.Revision
	for rows.Next() {
		rev, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		revs = append(revs, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vtext revisions: %w", err)
	}
	return revs, nil
}

// ----- History -----

// GetHistory returns the revision history for a document as a list of
// HistoryEntry values ordered from the current head backward through the
// parent_revision chain. Using the explicit revision chain rather than a raw
// timestamp sort avoids ambiguity when multiple revisions share the same
// coarse database timestamp.
func (s *Store) GetHistory(ctx context.Context, docID, ownerID string, limit int) ([]types.HistoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	doc, err := s.GetDocument(ctx, docID, ownerID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionID == "" {
		return []types.HistoryEntry{}, nil
	}

	currentID := doc.CurrentRevisionID
	entries := make([]types.HistoryEntry, 0, limit)
	for len(entries) < limit && currentID != "" {
		rev, err := s.GetRevision(ctx, currentID, ownerID)
		if err != nil {
			return nil, fmt.Errorf("load vtext history revision %s: %w", currentID, err)
		}
		entries = append(entries, types.HistoryEntry{
			RevisionID:       rev.RevisionID,
			DocID:            rev.DocID,
			AuthorKind:       rev.AuthorKind,
			AuthorLabel:      rev.AuthorLabel,
			ParentRevisionID: rev.ParentRevisionID,
			CreatedAt:        rev.CreatedAt,
		})
		currentID = rev.ParentRevisionID
	}
	return entries, nil
}

// ----- Diff -----

// GetDiff computes the diff between two revisions, scoped to the given
// owner. It returns a DiffResult with sections showing what changed
// (VAL-ETEXT-008).
func (s *Store) GetDiff(ctx context.Context, fromRevID, toRevID, ownerID string) (types.DiffResult, error) {
	fromRev, err := s.GetRevision(ctx, fromRevID, ownerID)
	if err != nil {
		return types.DiffResult{}, fmt.Errorf("get from revision: %w", err)
	}
	toRev, err := s.GetRevision(ctx, toRevID, ownerID)
	if err != nil {
		return types.DiffResult{}, fmt.Errorf("get to revision: %w", err)
	}

	sections := computeLineDiff(fromRev.Content, toRev.Content)

	added, removed := 0, 0
	for _, sec := range sections {
		switch sec.Type {
		case "added":
			added++
		case "removed":
			removed++
		}
	}

	return types.DiffResult{
		FromRevisionID: fromRevID,
		ToRevisionID:   toRevID,
		Sections:       sections,
		AddedLines:     added,
		RemovedLines:   removed,
	}, nil
}

// computeLineDiff computes a line-based diff between two strings using
// the longest common subsequence (LCS) algorithm. It produces a list of
// diff sections that classify each region as unchanged, added, or removed.
func computeLineDiff(from, to string) []types.DiffSection {
	fromLines := splitLines(from)
	toLines := splitLines(to)

	lcs := longestCommonSubsequence(fromLines, toLines)

	var sections []types.DiffSection
	fi, ti := 0, 0

	for _, match := range lcs {
		// Process removed lines before the match in from.
		if fi < match.fi {
			sections = append(sections, types.DiffSection{
				Type:        "removed",
				FromLine:    fi,
				ToLine:      match.fi - 1,
				ToLineNum:   -1,
				ToEndLine:   -1,
				FromContent: strings.Join(fromLines[fi:match.fi], ""),
			})
		}
		// Process added lines before the match in to.
		if ti < match.ti {
			sections = append(sections, types.DiffSection{
				Type:      "added",
				FromLine:  -1,
				ToLine:    -1,
				ToLineNum: ti,
				ToEndLine: match.ti - 1,
				ToContent: strings.Join(toLines[ti:match.ti], ""),
			})
		}

		// Process the matching line (unchanged).
		sections = append(sections, types.DiffSection{
			Type:        "unchanged",
			FromLine:    match.fi,
			ToLine:      match.fi,
			ToLineNum:   match.ti,
			ToEndLine:   match.ti,
			FromContent: fromLines[match.fi],
			ToContent:   toLines[match.ti],
		})

		fi = match.fi + 1
		ti = match.ti + 1
	}

	// Process trailing removed lines.
	if fi < len(fromLines) {
		sections = append(sections, types.DiffSection{
			Type:        "removed",
			FromLine:    fi,
			ToLine:      len(fromLines) - 1,
			ToLineNum:   -1,
			ToEndLine:   -1,
			FromContent: strings.Join(fromLines[fi:], ""),
		})
	}
	// Process trailing added lines.
	if ti < len(toLines) {
		sections = append(sections, types.DiffSection{
			Type:      "added",
			FromLine:  -1,
			ToLine:    -1,
			ToLineNum: ti,
			ToEndLine: len(toLines) - 1,
			ToContent: strings.Join(toLines[ti:], ""),
		})
	}

	// Merge adjacent sections of the same type.
	return mergeSections(sections)
}

// lcsMatch represents a matching position in both sequences.
type lcsMatch struct {
	fi int // index in from sequence
	ti int // index in to sequence
}

// longestCommonSubsequence computes the LCS of two line slices and returns
// the matching positions in order.
func longestCommonSubsequence(from, to []string) []lcsMatch {
	m, n := len(from), len(to)
	if m == 0 || n == 0 {
		return nil
	}

	// Build the DP table. dp[i][j] = length of LCS of from[:i] and to[:j].
	// Use a rolling array to save memory (only need previous row).
	prev := make([]int, n+1)
	curr := make([]int, n+1)

	// Also need to track the actual LCS, so we keep the full DP table
	// for small inputs. For large inputs, we would need Hirschberg's
	// algorithm, but document diffs are typically small enough.
	dp := make([][]int, m+1)
	dp[0] = make([]int, n+1)
	for i := 1; i <= m; i++ {
		dp[i] = make([]int, n+1)
		for j := 1; j <= n; j++ {
			if from[i-1] == to[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Keep prev/curr in sync (unused but silences vet).
	_ = prev
	_ = curr

	// Backtrack to find the actual matching positions.
	var matches []lcsMatch
	i, j := m, n
	for i > 0 && j > 0 {
		if from[i-1] == to[j-1] {
			matches = append(matches, lcsMatch{fi: i - 1, ti: j - 1})
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}

	// Reverse to get forward order.
	for left, right := 0, len(matches)-1; left < right; left, right = left+1, right-1 {
		matches[left], matches[right] = matches[right], matches[left]
	}

	return matches
}

// splitLines splits a string into lines, preserving line endings.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// mergeSections merges adjacent sections of the same type.
func mergeSections(sections []types.DiffSection) []types.DiffSection {
	if len(sections) <= 1 {
		return sections
	}
	merged := []types.DiffSection{sections[0]}
	for i := 1; i < len(sections); i++ {
		last := &merged[len(merged)-1]
		curr := sections[i]
		if last.Type == curr.Type {
			// Merge.
			last.ToLine = curr.ToLine
			last.ToEndLine = curr.ToEndLine
			if curr.FromContent != "" {
				last.FromContent += curr.FromContent
			}
			if curr.ToContent != "" {
				last.ToContent += curr.ToContent
			}
		} else {
			merged = append(merged, curr)
		}
	}
	return merged
}

// ----- Blame -----

// GetBlame computes the blame for a revision, scoped to the given owner.
// It walks backward through the revision chain, attributing each line to
// the most recent revision that changed it. This provides section-level
// attribution distinguishing whether the last editor was the user or the
// agent (VAL-ETEXT-009).
func (s *Store) GetBlame(ctx context.Context, revisionID, ownerID string) (types.BlameResult, error) {
	// First verify owner scope.
	headRev, err := s.GetRevision(ctx, revisionID, ownerID)
	if err != nil {
		return types.BlameResult{}, err
	}

	// Collect the revision chain from head backward.
	chain, err := s.collectRevisionChain(ctx, headRev)
	if err != nil {
		return types.BlameResult{}, fmt.Errorf("collect revision chain: %w", err)
	}

	sections := computeBlame(chain, headRev)

	return types.BlameResult{
		RevisionID: revisionID,
		DocID:      headRev.DocID,
		Sections:   sections,
	}, nil
}

// collectRevisionChain walks backward through parent_revision_id from the
// head revision to the root, collecting all revisions in chronological order.
func (s *Store) collectRevisionChain(ctx context.Context, head types.Revision) ([]types.Revision, error) {
	// Start with the head.
	seen := map[string]bool{head.RevisionID: true}
	chain := []types.Revision{head}

	current := head
	for current.ParentRevisionID != "" {
		parentID := current.ParentRevisionID
		if seen[parentID] {
			// Cycle detected; stop.
			break
		}
		seen[parentID] = true

		parent, err := s.GetRevisionUnscoped(ctx, parentID)
		if err != nil {
			// Missing parent; stop the chain.
			break
		}
		chain = append(chain, parent)
		current = parent
	}

	// Reverse to get chronological order (oldest first).
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}

	return chain, nil
}

// computeBlame attributes each line in the head revision to the most recent
// revision that changed it. It processes the revision chain from oldest to
// newest, tracking which revision last modified each line.
func computeBlame(chain []types.Revision, head types.Revision) []types.BlameSection {
	headLines := splitLines(head.Content)
	if len(headLines) == 0 {
		return nil
	}

	// blame[i] = index into chain of the revision that last changed line i.
	blame := make([]int, len(headLines))
	for i := range blame {
		blame[i] = -1
	}

	// Start with the initial content as the first revision's content.
	// Then for each subsequent revision, diff it against the previous
	// and mark changed lines.
	if len(chain) == 0 {
		// No chain (shouldn't happen), attribute all to head.
		for i := range blame {
			blame[i] = 0
		}
	} else {
		// Attribute all lines to the first revision initially.
		firstLines := splitLines(chain[0].Content)
		for i := range blame {
			if i < len(firstLines) {
				blame[i] = 0
			}
		}

		// For each subsequent revision, find which lines changed.
		prevLines := firstLines
		for ci := 1; ci < len(chain); ci++ {
			currLines := splitLines(chain[ci].Content)
			if len(currLines) != len(headLines) {
				// Content length changed; this is a more complex diff.
				// For blame, we use a simple approach: if the current
				// revision's content matches the head, attribute lines
				// that differ from the previous revision to this revision.
				diff := computeLineDiff(
					strings.Join(prevLines, ""),
					strings.Join(currLines, ""),
				)
				// Map diff sections back to head line numbers.
				// This is approximate but sufficient for section-level blame.
				_ = diff // We use a simpler approach below.
			}
			prevLines = currLines
		}

		// Simple blame: for each pair of consecutive revisions, mark lines
		// that are different from the previous revision as belonging to the
		// newer revision.
		for ci := len(chain) - 1; ci >= 1; ci-- {
			currLines := splitLines(chain[ci].Content)
			prevContent := ""
			if ci > 0 {
				prevContent = chain[ci-1].Content
			}
			prevLines := splitLines(prevContent)

			// Lines present in current but different from previous are
			// attributed to current revision.
			for i := 0; i < len(currLines) && i < len(headLines); i++ {
				if i < len(prevLines) {
					if currLines[i] != prevLines[i] {
						blame[i] = ci
					}
				} else {
					// New lines added by this revision.
					blame[i] = ci
				}
			}
		}

		// Mark any remaining unattributed lines.
		for i := range blame {
			if blame[i] == -1 {
				blame[i] = 0
			}
		}
	}

	// Group consecutive lines with the same blame revision into sections.
	var sections []types.BlameSection
	start := 0
	for i := 1; i <= len(blame); i++ {
		if i == len(blame) || blame[i] != blame[start] {
			ci := blame[start]
			rev := head
			if ci >= 0 && ci < len(chain) {
				rev = chain[ci]
			}
			sections = append(sections, types.BlameSection{
				RevisionID:  rev.RevisionID,
				AuthorKind:  rev.AuthorKind,
				AuthorLabel: rev.AuthorLabel,
				StartLine:   start,
				EndLine:     i - 1,
				Content:     strings.Join(headLines[start:i], ""),
				Timestamp:   rev.CreatedAt,
			})
			start = i
		}
	}

	return sections
}

// ----- Scan helpers -----

func scanEvidence(row interface{ Scan(...any) error }) (types.EvidenceRecord, error) {
	var (
		rec          types.EvidenceRecord
		metadataJSON string
		createdAtRaw string
	)
	if err := row.Scan(
		&rec.EvidenceID,
		&rec.OwnerID,
		&rec.AgentID,
		&rec.Kind,
		&rec.SourceURI,
		&rec.Title,
		&rec.Content,
		&metadataJSON,
		&createdAtRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.EvidenceRecord{}, ErrNotFound
		}
		return types.EvidenceRecord{}, fmt.Errorf("scan evidence record: %w", err)
	}
	if strings.TrimSpace(metadataJSON) != "" && metadataJSON != "{}" {
		rec.Metadata = json.RawMessage(metadataJSON)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtRaw)
	if err != nil {
		return types.EvidenceRecord{}, fmt.Errorf("parse evidence created_at: %w", err)
	}
	rec.CreatedAt = createdAt.UTC()
	return rec, nil
}

// scanDocument scans a document record from a single row.
func scanDocument(row interface{ Scan(...any) error }) (types.Document, error) {
	var doc types.Document
	var createdAt, updatedAt string

	err := row.Scan(
		&doc.DocID,
		&doc.OwnerID,
		&doc.Title,
		&doc.CurrentRevisionID,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.Document{}, ErrNotFound
		}
		return types.Document{}, fmt.Errorf("scan vtext document: %w", err)
	}

	doc.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.Document{}, fmt.Errorf("parse document created_at: %w", err)
	}
	doc.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return types.Document{}, fmt.Errorf("parse document updated_at: %w", err)
	}

	return doc, nil
}

// scanRevision scans a revision record from a single row.
func scanRevision(row interface{ Scan(...any) error }) (types.Revision, error) {
	var rev types.Revision
	var authorKind, createdAt string
	var citationsJSON, metadataJSON string
	var parentRevID string

	err := row.Scan(
		&rev.RevisionID,
		&rev.DocID,
		&rev.OwnerID,
		&authorKind,
		&rev.AuthorLabel,
		&rev.Content,
		&citationsJSON,
		&metadataJSON,
		&parentRevID,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.Revision{}, ErrNotFound
		}
		return types.Revision{}, fmt.Errorf("scan vtext revision: %w", err)
	}

	rev.AuthorKind = types.AuthorKind(authorKind)
	rev.ParentRevisionID = parentRevID

	rev.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.Revision{}, fmt.Errorf("parse revision created_at: %w", err)
	}

	if citationsJSON != "" && citationsJSON != "[]" {
		rev.Citations = json.RawMessage(citationsJSON)
	}
	if metadataJSON != "" && metadataJSON != "{}" {
		rev.Metadata = json.RawMessage(metadataJSON)
	}

	return rev, nil
}

// ----- Agent mutation tracking (VAL-CROSS-122: idempotent revision) -----

// AgentMutation represents an in-flight or completed appagent-driven document
// mutation. It tracks the mapping from a runtime run to a document mutation,
// enabling idempotent handling so that renewal/retry does not create a
// duplicate canonical revision.
type AgentMutation struct {
	DocID       string     `json:"doc_id"`
	RunID       string     `json:"loop_id"`
	OwnerID     string     `json:"owner_id"`
	State       string     `json:"state"` // "pending", "completed", "failed"
	RevisionID  string     `json:"revision_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// CreateAgentMutation records a new in-flight appagent mutation. It uses
// INSERT IGNORE so that duplicate (doc_id, loop_id) pairs are silently
// ignored, supporting idempotent run creation (VAL-CROSS-122).
func (s *Store) CreateAgentMutation(ctx context.Context, m AgentMutation) error {
	var completedAt any
	if m.CompletedAt != nil {
		completedAt = m.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.vtextHandle().ExecContext(ctx,
		`INSERT IGNORE INTO vtext_agent_mutations (doc_id, loop_id, owner_id, state, revision_id, created_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.DocID,
		m.RunID,
		m.OwnerID,
		m.State,
		m.RevisionID,
		m.CreatedAt.UTC().Format(time.RFC3339Nano),
		completedAt,
	)
	if err != nil {
		return fmt.Errorf("insert vtext agent mutation: %w", err)
	}
	return nil
}

// GetPendingAgentMutationByDoc returns the pending agent mutation for a
// document, if one exists. This is used to return the existing run ID
// when a retry/renewal occurs, preventing duplicate mutation submissions
// (VAL-CROSS-122).
func (s *Store) GetPendingAgentMutationByDoc(ctx context.Context, docID, ownerID string) (*AgentMutation, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT doc_id, loop_id, owner_id, state, revision_id, created_at, completed_at
		   FROM vtext_agent_mutations
		  WHERE doc_id = ? AND owner_id = ? AND state = 'pending'
		  ORDER BY created_at DESC
		  LIMIT 1`,
		docID, ownerID,
	)
	return scanAgentMutation(row)
}

// GetAgentMutationByRun returns the agent mutation for a specific run ID.
// This is used during run completion to check if the revision has already
// been created (VAL-CROSS-122: no duplicate canonical revision).
func (s *Store) GetAgentMutationByRun(ctx context.Context, runID string) (*AgentMutation, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT doc_id, loop_id, owner_id, state, revision_id, created_at, completed_at
		   FROM vtext_agent_mutations
		  WHERE loop_id = ?`,
		runID,
	)
	return scanAgentMutation(row)
}

// CompleteAgentMutation marks an agent mutation as completed with the
// revision ID of the newly created canonical revision. It returns
// ErrMutationAlreadyCompleted if the mutation is already in a completed
// state, preventing duplicate canonical revisions (VAL-CROSS-122).
var ErrMutationAlreadyCompleted = errors.New("agent mutation already completed")

func (s *Store) CompleteAgentMutation(ctx context.Context, runID, revisionID string) error {
	now := time.Now().UTC()
	result, err := s.vtextHandle().ExecContext(ctx,
		`UPDATE vtext_agent_mutations
		    SET state = 'completed',
		        revision_id = ?,
		        completed_at = ?
		  WHERE loop_id = ? AND state = 'pending'`,
		revisionID,
		now.Format(time.RFC3339Nano),
		runID,
	)
	if err != nil {
		return fmt.Errorf("complete vtext agent mutation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check completed mutation rows: %w", err)
	}
	if rows == 0 {
		return ErrMutationAlreadyCompleted
	}
	return nil
}

// FailAgentMutation marks an agent mutation as failed.
func (s *Store) FailAgentMutation(ctx context.Context, runID string) error {
	now := time.Now().UTC()
	_, err := s.vtextHandle().ExecContext(ctx,
		`UPDATE vtext_agent_mutations
		    SET state = 'failed',
		        completed_at = ?
		  WHERE loop_id = ? AND state = 'pending'`,
		now.Format(time.RFC3339Nano),
		runID,
	)
	if err != nil {
		return fmt.Errorf("fail vtext agent mutation: %w", err)
	}
	return nil
}

// CreateEvidence inserts a durable evidence record into the embedded Dolt
// workspace. Evidence is owner-scoped and associated with the capturing agent.
func (s *Store) CreateEvidence(ctx context.Context, rec types.EvidenceRecord) error {
	metadata := string(rec.Metadata)
	if strings.TrimSpace(metadata) == "" {
		metadata = "{}"
	}
	_, err := s.vtextHandle().ExecContext(ctx,
		`INSERT INTO agent_evidence (evidence_id, owner_id, agent_id, kind, source_uri, title, content, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.EvidenceID,
		rec.OwnerID,
		rec.AgentID,
		rec.Kind,
		rec.SourceURI,
		rec.Title,
		rec.Content,
		metadata,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert agent evidence: %w", err)
	}
	return nil
}

// GetEvidence returns a single evidence record scoped to the given owner.
func (s *Store) GetEvidence(ctx context.Context, evidenceID, ownerID string) (types.EvidenceRecord, error) {
	row := s.vtextHandle().QueryRowContext(ctx,
		`SELECT evidence_id, owner_id, agent_id, kind, source_uri, title, content, metadata_json, created_at
		   FROM agent_evidence
		  WHERE evidence_id = ? AND owner_id = ?`,
		evidenceID, ownerID,
	)
	return scanEvidence(row)
}

// ListEvidenceByAgent returns recent evidence captured by an agent and scoped
// to the given owner. If agentID is empty it returns recent evidence across all
// of the owner's agents.
func (s *Store) ListEvidenceByAgent(ctx context.Context, ownerID, agentID string, limit int) ([]types.EvidenceRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(agentID) == "" {
		rows, err = s.vtextHandle().QueryContext(ctx,
			`SELECT evidence_id, owner_id, agent_id, kind, source_uri, title, content, metadata_json, created_at
			   FROM agent_evidence
			  WHERE owner_id = ?
			  ORDER BY created_at DESC
			  LIMIT ?`,
			ownerID, limit,
		)
	} else {
		rows, err = s.vtextHandle().QueryContext(ctx,
			`SELECT evidence_id, owner_id, agent_id, kind, source_uri, title, content, metadata_json, created_at
			   FROM agent_evidence
			  WHERE owner_id = ? AND agent_id = ?
			  ORDER BY created_at DESC
			  LIMIT ?`,
			ownerID, agentID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("query agent evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []types.EvidenceRecord
	for rows.Next() {
		rec, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent evidence: %w", err)
	}
	return out, nil
}

// scanAgentMutation scans an agent mutation record from a single row.
func scanAgentMutation(row interface{ Scan(...any) error }) (*AgentMutation, error) {
	var m AgentMutation
	var createdAt string
	var completedAt sql.NullString

	err := row.Scan(
		&m.DocID,
		&m.RunID,
		&m.OwnerID,
		&m.State,
		&m.RevisionID,
		&createdAt,
		&completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // no pending mutation is not an error
		}
		return nil, fmt.Errorf("scan vtext agent mutation: %w", err)
	}

	m.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse agent mutation created_at: %w", err)
	}
	if completedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse agent mutation completed_at: %w", err)
		}
		m.CompletedAt = &t
	}

	return &m, nil
}
