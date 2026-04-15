# Dolt Integration Research for E-Text Versioned Storage

> **Date**: 2026-04-08
> **Context**: Unified multiagent system in Go; vtext app with user + appagent edits, full provenance

---

## 1. Dolt Overview

**Dolt** is the world's first version-controlled SQL database — described as "Git for Data." It is a MySQL-compatible relational database that adds Git-style version control as a first-class feature. Written in Go.

### Key Differentiators
- **Git semantics on SQL data**: branch, merge, diff, clone, push, pull — all operate on structured relational data, not files
- **MySQL wire protocol compatible**: any MySQL client or ORM can connect; standard SQL for reads/writes
- **Content-addressed storage via Prolly Trees**: a novel B-tree variant that enables structural sharing between versions (only changed data uses additional storage) and O(d) diff computation (proportional to diff size, not data size)
- **Commit graph**: every mutation can be wrapped in a commit with author, timestamp, message, and parent pointers — full audit trail queryable via SQL
- **Cell-wise diffs and merges**: unlike file-based VCS, Dolt understands individual cell changes and can merge them intelligently
- **Open source**: Apache 2.0 licensed, `github.com/dolthub/dolt`

### Core Versioning Model
- **Commits**: immutable snapshots of the entire database state. Each commit has a hash, author name/email, timestamp, message, and parent commit(s)
- **Branches**: named mutable pointers to commits (like Git branches). Each branch has an independent working set
- **Tags**: immutable named pointers to commits
- **Merges**: three-way merge at the cell level using the common ancestor. Conflicts are exposed via SQL system tables
- **Working set**: uncommitted changes on the current branch (analogous to Git's working directory)

---

## 2. Go Integration Options

### Option A: Embedded Mode via `github.com/dolthub/driver` (RECOMMENDED)

The official Go `database/sql` driver for Dolt that embeds the entire Dolt engine in-process. This is the best fit for our use case.

```go
import (
    "database/sql"
    embedded "github.com/dolthub/driver"
)

cfg, _ := embedded.ParseDSN(
    "file:///path/to/dbs?commitname=User&commitemail=user@example.com&database=vtext",
)
connector, _ := embedded.NewConnector(cfg)
db := sql.OpenDB(connector)
// Use db like any database/sql connection — full Dolt version control via SQL
```

**How it works:**
- The Dolt SQL engine runs **in-process** inside the Go application — no external server process needed
- Data lives on the local filesystem in Dolt's storage format (`.dolt/` directory)
- Accessed via Go's standard `database/sql` interface
- DSN specifies: filesystem path, commit author name/email, initial database name
- Supports `multistatements=true` for batched SQL
- Supports retry with exponential backoff for lock contention (`Config.BackOff`)
- Actively maintained (latest release v1.84.1, March 2026)

**Advantages for our use case:**
- No external process to manage — simplifies deployment
- SQLite-like simplicity (file-based) but with full version control
- The `commitname`/`commitemail` DSN params naturally map to "who made this edit"
- In-process means lowest possible latency
- Per-user database instances are trivially isolated (separate directories)

### Option B: Server Mode via `dolt sql-server`

Run Dolt as a standalone MySQL-compatible server. Connect from Go using any MySQL driver (e.g., `go-sql-driver/mysql`).

- Best for multi-service architectures where multiple processes need concurrent access
- Adds operational complexity (process management, networking)
- **Not recommended** for per-user embedded vtext storage

### Option C: Direct Programmatic Go API (Internal)

The Dolt codebase (`github.com/dolthub/dolt/go/...`) exposes internal Go packages for low-level access to Prolly Trees, the commit graph, etc. This is not a public/stable API and is not recommended for application use.

### Recommendation: **Embedded mode** via `dolthub/driver`

For a per-user vtext database where the Go application is the sole writer, embedded mode gives us:
- Zero operational overhead (no server process)
- Lowest latency (in-process)
- Standard `database/sql` interface
- Full version control via SQL stored procedures and system tables
- Natural per-user isolation (one Dolt DB directory per user)

---

## 3. Versioning Model

### How Commits Work

Every Dolt commit captures:
- **Full database snapshot** (content-addressed, so only changed data costs storage)
- **Author name + email** (set via DSN in embedded mode, or `--author` flag)
- **Timestamp** (automatic)
- **Commit message** (provided by application)
- **Parent commit hash(es)** (one for normal commits, two for merge commits)

Creating a commit via SQL:
```sql
-- Stage all changes and commit
CALL dolt_add('.');
CALL dolt_commit('-m', 'User edited paragraph 3', '--author', 'Alice <alice@example.com>');

-- Or auto-stage everything:
CALL dolt_commit('-Am', 'Appagent revised section 2', '--author', 'AppAgent <agent@system>');
```

Alternatively, set `@@dolt_transaction_commit = 1` to automatically create a Dolt commit on every SQL `COMMIT`.

### Branching and Merging

```sql
-- Create a branch
CALL dolt_branch('appagent-draft');

-- Switch to it
CALL dolt_checkout('appagent-draft');

-- Do work, then merge back
CALL dolt_checkout('main');
CALL dolt_merge('appagent-draft');
```

**Merge algorithm**: Three-way cell-level merge using the common ancestor commit. For each cell, if only one side changed it, the change is accepted. If both sides changed the same cell differently, a conflict is recorded.

### Representing "User Edit" vs "AppAgent Edit" as Separate Authors

The `--author` parameter on `dolt_commit()` directly supports this:

```sql
-- User edit
CALL dolt_commit('-Am', 'User revised intro',
    '--author', 'Alice <alice@choiros.app>');

-- AppAgent edit
CALL dolt_commit('-Am', 'AppAgent refined paragraph structure',
    '--author', 'ETextAgent <vtext-agent@choiros.app>');
```

In embedded mode, the DSN's `commitname`/`commitemail` serve as defaults, but can be overridden per-commit with `--author`.

### Diff Capabilities

Dolt provides rich diff support via system tables:

```sql
-- What changed between two commits for a specific table?
SELECT * FROM dolt_diff_etext_content
WHERE from_commit = HASHOF('HEAD~1') AND to_commit = HASHOF('HEAD');

-- What tables were modified between any two commits?
SELECT * FROM dolt_diff
WHERE from_commit_hash = 'abc123' AND to_commit_hash = 'def456';

-- Column-level diff (which columns changed?)
SELECT * FROM dolt_column_diff
WHERE from_commit = 'abc123' AND to_commit = 'def456';

-- Blame: who last modified each row?
SELECT * FROM dolt_blame_etext_content;
```

### Querying History / Provenance

```sql
-- Full commit log
SELECT * FROM dolt_log;

-- Commit log filtered by author
SELECT * FROM dolt_log WHERE committer = 'ETextAgent';

-- Historical state of any table at any commit
SELECT * FROM etext_content AS OF 'abc123def';
SELECT * FROM etext_content AS OF TIMESTAMP('2026-04-01');

-- Full history of a specific row across all commits
SELECT * FROM dolt_history_etext_content
WHERE doc_id = 'my-document'
ORDER BY commit_date DESC;
```

---

## 4. Concurrency Model

### How Multiple Writers Are Handled

**Embedded mode (our case)**:
- The embedded Dolt engine uses a lock file on the database directory
- Only **one connection at a time** can hold a write lock
- Multiple concurrent readers are supported
- The `dolthub/driver` supports configurable retry with exponential backoff for lock contention
- For a per-user vtext database, this is fine: the user and the appagent are mediated by the same Go process, which serializes writes

**Server mode**:
- Full MySQL-style transaction isolation (serializable, read committed, etc.)
- Multiple concurrent writers supported with standard SQL transaction semantics
- Each SQL session maintains its own branch/working set state
- `COMMIT` / `ROLLBACK` work as expected

### Conflict Resolution

When merging branches, Dolt performs cell-level three-way merge:

1. **No conflict**: Only one side changed a cell → change is accepted
2. **Conflict**: Both sides changed the same cell differently → conflict recorded in `dolt_conflicts_<table>` system table
3. **Constraint violations**: Merge may create FK or unique key violations → recorded in `dolt_constraint_violations_<table>`

Resolution strategies via SQL:
```sql
-- Take "ours" (discard their changes)
DELETE FROM dolt_conflicts_etext_content;

-- Take "theirs" (accept their changes)
REPLACE INTO etext_content (id, content, ...)
    SELECT their_id, their_content, ...
    FROM dolt_conflicts_etext_content
    WHERE their_id IS NOT NULL;
DELETE FROM dolt_conflicts_etext_content;

-- Or custom: application-level merge logic per row
```

### Implications for Simultaneous User + AppAgent Edits

**Design**: Since we control the Go process, the simplest model is:

1. **User edits** happen on `main` branch → commit with user author
2. **AppAgent edits** happen on a separate branch (e.g., `agent-draft`) → commit with agent author
3. **Merge** agent branch into `main` when ready, resolving conflicts programmatically
4. Alternatively, since writes are serialized through our Go process, both can commit to `main` sequentially (no branching needed for simple cases)

For the common case (user and agent editing different sections), the cell-level merge means no conflicts. For same-cell edits, our application can implement custom resolution logic.

---

## 5. Performance Characteristics

### Read/Write Latency

As of December 2025, **Dolt has reached MySQL parity on Sysbench benchmarks** (mean multiplier of 0.99x):

| Benchmark | MySQL (ms) | Dolt (ms) | Multiplier |
|-----------|-----------|----------|------------|
| oltp_point_select | 0.20 | 0.27 | 1.35x |
| oltp_read_only | 38.20 | 5.28 | 0.14x (faster!) |
| oltp_insert | 4.18 | 3.19 | 0.76x (faster!) |
| oltp_read_write | 9.22 | 11.65 | 1.26x |
| table_scan | 34.95 | 28.16 | 0.81x (faster!) |
| **overall mean** | | | **0.99x** |

For our use case (small per-user databases with individual documents), performance will be far more than adequate. We're talking about microsecond-scale operations on small datasets.

### Storage Overhead vs SQLite

- **Prolly Tree structural sharing**: unchanged data between versions shares storage. Only deltas cost additional space
- **Per-commit overhead**: minimal metadata (hash, author, timestamp, message, parent pointers)
- **Study on structural sharing** (DoltHub, April 2024): For typical mutation patterns, storage overhead for maintaining full history is ~1.1-2x the size of the current data (not N× for N versions)
- **vs SQLite**: Dolt will use more space than a single SQLite database because it maintains the commit graph and content-addressed chunks. But Dolt replaces SQLite + a separate version history system. If you'd implement versioning on top of SQLite (copying rows, storing diffs), Dolt is likely more space-efficient
- **Chunk-based storage**: Dolt stores data in content-addressed chunks using FlatBuffers serialization. Empty databases are ~few hundred KB

### Scalability Considerations

- Dolt is designed for databases up to hundreds of GB
- For per-user vtext (likely < 100MB per user), Dolt is vastly overpowered — this is a sweet spot
- Prolly tree diff is O(d) where d = diff size, not O(n) where n = data size
- The Go embedded engine has the same performance characteristics as the server

---

## 6. E-Text Storage Design Sketch

### Table Structure

```sql
CREATE TABLE etext_documents (
    doc_id VARCHAR(36) DEFAULT (UUID()) PRIMARY KEY,
    title VARCHAR(512) NOT NULL,
    doc_type VARCHAR(64) NOT NULL DEFAULT 'text',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE etext_content (
    doc_id VARCHAR(36) NOT NULL,
    section_id VARCHAR(36) DEFAULT (UUID()),
    section_order INT NOT NULL,
    content LONGTEXT NOT NULL,
    content_hash VARCHAR(64),  -- optional: SHA-256 of content for quick equality checks
    PRIMARY KEY (doc_id, section_id),
    INDEX idx_doc_order (doc_id, section_order),
    FOREIGN KEY (doc_id) REFERENCES etext_documents(doc_id)
);

-- Optional: metadata for each document (tags, status, etc.)
CREATE TABLE etext_metadata (
    doc_id VARCHAR(36) NOT NULL,
    meta_key VARCHAR(128) NOT NULL,
    meta_value TEXT,
    PRIMARY KEY (doc_id, meta_key),
    FOREIGN KEY (doc_id) REFERENCES etext_documents(doc_id)
);
```

**Design notes**:
- UUID primary keys (recommended by Dolt for merge-friendliness — avoids auto-increment conflicts across branches)
- Content is split into sections for granular diffing and merging (e.g., paragraphs, chapters)
- `section_order` provides ordering within a document
- Keeping content in a separate table from document metadata allows Dolt's cell-level diff to work efficiently

### How Versions Map to Dolt Commits

Every meaningful edit → one Dolt commit:

```go
func (s *ETextStore) SaveUserEdit(ctx context.Context, docID, sectionID, newContent, userName, userEmail string) error {
    tx, _ := s.db.BeginTx(ctx, nil)
    
    // Update the content
    tx.ExecContext(ctx, `UPDATE etext_content SET content = ?, updated_at = NOW() 
        WHERE doc_id = ? AND section_id = ?`, newContent, docID, sectionID)
    
    // Stage and commit with user attribution
    tx.ExecContext(ctx, `CALL dolt_add('.')`)
    tx.ExecContext(ctx, `CALL dolt_commit('-m', ?, '--author', ?)`,
        fmt.Sprintf("Edit section %s of doc %s", sectionID, docID),
        fmt.Sprintf("%s <%s>", userName, userEmail))
    
    return tx.Commit()
}

func (s *ETextStore) SaveAppAgentEdit(ctx context.Context, docID, sectionID, newContent string) error {
    tx, _ := s.db.BeginTx(ctx, nil)
    
    tx.ExecContext(ctx, `UPDATE etext_content SET content = ?, updated_at = NOW()
        WHERE doc_id = ? AND section_id = ?`, newContent, docID, sectionID)
    
    tx.ExecContext(ctx, `CALL dolt_add('.')`)
    tx.ExecContext(ctx, `CALL dolt_commit('-m', ?, '--author', ?)`,
        fmt.Sprintf("Agent revised section %s of doc %s", sectionID, docID),
        "ETextAgent <vtext-agent@choiros.local>")
    
    return tx.Commit()
}
```

### How to Attribute Edits to User vs AppAgent vs Worker

- **User edits**: commit with `--author 'UserName <user@email>'` — direct mutations, always canonical
- **AppAgent edits**: commit with `--author 'ETextAgent <agent@system>'` — direct mutations, canonical
- **Worker proposals**: workers do NOT commit to the database directly. Instead:
  - Workers send messages/proposals through the messaging system
  - The appagent or user reviews and applies them, creating a commit attributed appropriately
  - Optionally: store proposals in a separate `etext_proposals` table (not versioned, or on a separate branch)

Query provenance:
```sql
-- Who edited what, when?
SELECT commit_hash, committer, email, date, message 
FROM dolt_log ORDER BY date DESC;

-- Blame: who last touched each row?
SELECT doc_id, section_id, commit_hash, committer, commit_date
FROM dolt_blame_etext_content;

-- Full history of a specific section
SELECT ec.content, dh.commit_hash, dh.committer, dh.commit_date
FROM dolt_history_etext_content ec
JOIN dolt_log dh ON ec.commit_hash = dh.commit_hash
WHERE ec.doc_id = 'my-doc' AND ec.section_id = 'section-1'
ORDER BY dh.commit_date DESC;
```

### How to Query Current State vs Historical State

```sql
-- Current state (just normal SQL)
SELECT * FROM etext_content WHERE doc_id = 'my-doc';

-- State at a specific commit
SELECT * FROM etext_content AS OF 'abc123def' WHERE doc_id = 'my-doc';

-- State at a specific timestamp
SELECT * FROM etext_content AS OF TIMESTAMP('2026-04-01 12:00:00') WHERE doc_id = 'my-doc';

-- Diff between two versions
SELECT * FROM dolt_diff_etext_content
WHERE from_commit = HASHOF('HEAD~5') AND to_commit = HASHOF('HEAD')
AND doc_id = 'my-doc';
```

### How to Handle Concurrent Edits

**Approach 1: Serialized writes (simplest, recommended for v1)**
- The Go process serializes all writes to the embedded Dolt DB
- User edits and appagent edits go through the same code path, sequentially
- Each creates its own commit with appropriate author
- No branching/merging needed
- Works well because embedded mode has a single-writer lock anyway

**Approach 2: Branch-based isolation (for more complex scenarios)**
- User edits commit to `main`
- AppAgent works on `agent/<task-id>` branch
- When agent work is ready, merge into `main`
- Conflicts resolved programmatically by the application

```go
// Agent starts work on a branch
db.Exec(`CALL dolt_branch('agent/revise-doc-123')`)
db.Exec(`CALL dolt_checkout('agent/revise-doc-123')`)
// ... agent makes edits and commits ...

// Merge back when ready
db.Exec(`CALL dolt_checkout('main')`)
rows := db.QueryRow(`CALL dolt_merge('agent/revise-doc-123')`)
// Check for conflicts and resolve
```

---

## 7. Risks and Limitations

### Known Limitations

1. **Embedded mode single-writer constraint**: Only one connection can write at a time in embedded mode. Concurrent writes require lock contention handling (the driver supports retry backoff). This is fine for per-user DBs but would be a problem if many goroutines need simultaneous writes.

2. **Storage overhead**: Dolt databases start larger than SQLite (a few hundred KB vs 4KB for empty SQLite). For per-user databases with minimal content, this baseline overhead is noticeable but not problematic.

3. **Go dependency size**: The `dolthub/driver` pulls in the entire Dolt engine as a Go dependency. This significantly increases binary size and compile time. Expect the compiled binary to be 100MB+.

4. **MySQL compatibility gaps**: While highly MySQL-compatible, some advanced MySQL features may not be supported. Dolt tracks compatibility carefully — for vtext CRUD operations this is not a concern.

5. **No built-in CRDT or OT**: Dolt's merge is cell-level (entire cell contents). If two editors change the same cell (e.g., same paragraph), Dolt flags a conflict — it doesn't attempt character-level merge within a cell. Your application must handle intra-cell conflicts.

6. **Lock file issues**: In embedded mode, crash or ungraceful shutdown can leave stale lock files. The driver has retry logic for this, but it's something to handle in initialization.

7. **Not SQLite-compatible**: Despite similar embedded usage patterns, Dolt uses its own storage format. You can't use SQLite tools on Dolt databases.

### Things That Might Not Work Well

- **Real-time collaborative editing** (Google Docs style): Dolt commits are coarse-grained snapshots, not real-time operation streams. If you need sub-second real-time collaboration, you'd need a CRDT/OT layer on top, with Dolt as the persistence/versioning backend.
- **Very high write throughput**: Not a concern for vtext, but worth noting.
- **Cross-user database sync**: While Dolt supports push/pull to remotes, setting up a DoltHub/DoltLab remote for per-user sync adds infrastructure. However, for our use case (single user per database), this isn't needed.

### Alternatives for Specific Sub-Problems

- **Real-time collaboration**: Use a CRDT library (e.g., Yjs, Automerge) for the real-time editing layer → flush canonical state to Dolt periodically
- **Intra-cell text merge**: Use a text diff/merge library (e.g., go-diff) for merging within a cell when Dolt reports a conflict
- **Remote sync/backup**: Dolt's built-in push/pull to DoltHub or any S3-compatible remote

---

## 8. Recommended Architecture

### Overall Design

```
┌────────────────────────────────────────────────┐
│             Unified Go Application              │
│                                                │
│  ┌──────────┐  ┌──────────┐  ┌──────────────┐ │
│  │  User UI  │  │ AppAgent │  │   Workers    │ │
│  │ (editor)  │  │          │  │ (proposals)  │ │
│  └─────┬─────┘  └─────┬────┘  └──────┬───────┘ │
│        │              │               │         │
│        ▼              ▼               │         │
│  ┌─────────────────────────┐          │         │
│  │   VText Service Layer   │◄─────────┘         │
│  │  (serializes writes,    │   (reads proposals, │
│  │   manages commits)      │    does not write)  │
│  └───────────┬─────────────┘                    │
│              │                                  │
│              ▼                                  │
│  ┌─────────────────────────┐                    │
│  │  Embedded Dolt (driver) │                    │
│  │  database/sql interface │                    │
│  └───────────┬─────────────┘                    │
│              │                                  │
└──────────────┼──────────────────────────────────┘
               │
               ▼
         ~/.choiros/users/<user-id>/vtext/
         └── .dolt/  (Dolt database files)
```

### Concrete Recommendations

1. **Use embedded mode** via `github.com/dolthub/driver` — one Dolt database per user, stored at a well-known filesystem path

2. **Standard `database/sql` interface** — all reads and writes go through Go's standard SQL abstractions, keeping the storage layer swappable

3. **Serialize all writes through a single ETextStore service** — this service manages the embedded Dolt connection, ensures proper commit attribution, and handles any conflict resolution

4. **One commit per logical edit** — user saves, agent revisions, etc. each get their own Dolt commit with the appropriate `--author`

5. **Start with `main`-only** (no branching) for v1 — serialize user and agent edits on a single branch. Add branch-based isolation later if concurrent editing patterns demand it

6. **Use UUID primary keys** throughout — merge-friendly, no auto-increment conflicts

7. **Split content into sections** (paragraphs or logical blocks) — enables granular cell-level diffs and reduces merge conflicts

8. **Expose history via Dolt system tables** — `dolt_log`, `dolt_history_*`, `dolt_blame_*`, and `AS OF` queries give us rich provenance for free

9. **Configure retry backoff** on the embedded connector for graceful handling of lock contention during concurrent access attempts

10. **Optional future**: Add Dolt remote (DoltHub, S3) for backup/sync if multi-device support is needed

### Key Go Dependencies

```
github.com/dolthub/driver  v1.84.1  // Embedded Dolt driver (database/sql compatible)
```

This single dependency brings in the full Dolt engine. No other Dolt-specific dependencies are needed.

---

## Sources

- [Dolt GitHub Repository](https://github.com/dolthub/dolt)
- [Dolt Embedded Driver](https://github.com/dolthub/driver)
- [Embedding Dolt in Go Applications (DoltHub Blog)](https://www.dolthub.com/blog/2022-07-25-embedded/)
- [Writing a Go SQL Driver (DoltHub Blog, Jan 2026)](https://www.dolthub.com/blog/2026-01-23-golang-sql-drivers/)
- [Dolt System Tables Documentation](https://docs.dolthub.com/sql-reference/version-control/dolt-system-tables)
- [Querying History Documentation](https://docs.dolthub.com/sql-reference/version-control/querying-history)
- [Merges Documentation](https://docs.dolthub.com/sql-reference/version-control/merges)
- [Dolt Storage Engine Architecture](https://docs.dolthub.com/architecture/storage-engine)
- [Dolt Concurrency Blog Post](https://www.dolthub.com/blog/2021-03-12-dolt-sql-server-concurrency/)
- [Dolt is as Fast as MySQL (Dec 2025)](https://www.dolthub.com/blog/2025-12-04-dolt-is-as-fast-as-mysql/)
- [Dolt Latency Benchmarks](https://docs.dolthub.com/sql-reference/benchmarks/latency)
- [Structural Sharing Study (Apr 2024)](https://www.dolthub.com/blog/2024-04-12-study-in-structural-sharing/)
- [Dolt Replication Documentation](https://docs.dolthub.com/sql-reference/server/replication)
- [Restoring Beads Classic with Embedded Dolt (Apr 2026)](https://www.dolthub.com/blog/2026-04-02-restoring-beads-classic/)
