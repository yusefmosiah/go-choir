package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// openWorkRegistryTestStore creates a fresh store for work registry tests.
func openWorkRegistryTestStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "go-choir-work-registry-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, t.Name()+".db")
	_ = os.Remove(path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = os.Remove(path)
	})
	return s
}

func TestWorkRegistryCreateAndGet(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	item := types.WorkItem{
		ID:        "work-001",
		ParentID:  "",
		OwnerID:   "user-alice",
		Objective: "research topic X",
		State:     types.TaskPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.CreateWorkItem(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "work-001")
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}

	if got.ID != item.ID {
		t.Errorf("id: got %q, want %q", got.ID, item.ID)
	}
	if got.ParentID != "" {
		t.Errorf("parent_id: got %q, want empty for root item", got.ParentID)
	}
	if got.OwnerID != item.OwnerID {
		t.Errorf("owner_id: got %q, want %q", got.OwnerID, item.OwnerID)
	}
	if got.Objective != item.Objective {
		t.Errorf("objective: got %q, want %q", got.Objective, item.Objective)
	}
	if got.State != types.TaskPending {
		t.Errorf("state: got %q, want %q", got.State, types.TaskPending)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("created_at: got %v, want %v", got.CreatedAt, now)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("updated_at: got %v, want %v", got.UpdatedAt, now)
	}
}

func TestWorkRegistryGetNotFound(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	_, err := s.GetWorkItem(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestWorkRegistryCreateWithParent(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create parent work item.
	parent := types.WorkItem{
		ID:        "parent-001",
		ParentID:  "",
		OwnerID:   "user-alice",
		Objective: "parent task objective",
		State:     types.TaskRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateWorkItem(ctx, parent); err != nil {
		t.Fatalf("create parent work item: %v", err)
	}

	// Create child work item.
	child := types.WorkItem{
		ID:        "child-001",
		ParentID:  "parent-001",
		OwnerID:   "user-alice",
		Objective: "child task objective",
		State:     types.TaskPending,
		CreatedAt: now.Add(1 * time.Second),
		UpdatedAt: now.Add(1 * time.Second),
	}
	if err := s.CreateWorkItem(ctx, child); err != nil {
		t.Fatalf("create child work item: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "child-001")
	if err != nil {
		t.Fatalf("get child work item: %v", err)
	}
	if got.ParentID != "parent-001" {
		t.Errorf("parent_id: got %q, want %q", got.ParentID, "parent-001")
	}
}

func TestWorkRegistryStateTransitions(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	item := types.WorkItem{
		ID:        "work-state-test",
		ParentID:  "",
		OwnerID:   "user-alice",
		Objective: "test state transitions",
		State:     types.TaskPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateWorkItem(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	// Transition: pending → running
	item.State = types.TaskRunning
	item.UpdatedAt = now.Add(1 * time.Second)
	if err := s.UpdateWorkItem(ctx, item); err != nil {
		t.Fatalf("update work item to running: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "work-state-test")
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}
	if got.State != types.TaskRunning {
		t.Errorf("state after running: got %q, want %q", got.State, types.TaskRunning)
	}

	// Transition: running → completed with result
	finishedAt := now.Add(10 * time.Second)
	item.State = types.TaskCompleted
	item.Result = "research complete: found 5 relevant papers"
	item.UpdatedAt = finishedAt
	if err := s.UpdateWorkItem(ctx, item); err != nil {
		t.Fatalf("update work item to completed: %v", err)
	}

	got, err = s.GetWorkItem(ctx, "work-state-test")
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}
	if got.State != types.TaskCompleted {
		t.Errorf("state after completed: got %q, want %q", got.State, types.TaskCompleted)
	}
	if got.Result != "research complete: found 5 relevant papers" {
		t.Errorf("result: got %q, want research result", got.Result)
	}
}

func TestWorkRegistryTransitionToFailed(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	item := types.WorkItem{
		ID:        "work-fail-test",
		ParentID:  "",
		OwnerID:   "user-alice",
		Objective: "task that will fail",
		State:     types.TaskPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateWorkItem(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	item.State = types.TaskRunning
	item.UpdatedAt = now.Add(1 * time.Second)
	if err := s.UpdateWorkItem(ctx, item); err != nil {
		t.Fatalf("update work item to running: %v", err)
	}

	// Transition: running → failed
	item.State = types.TaskFailed
	item.Error = "provider timeout after 30s"
	item.UpdatedAt = now.Add(5 * time.Second)
	if err := s.UpdateWorkItem(ctx, item); err != nil {
		t.Fatalf("update work item to failed: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "work-fail-test")
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}
	if got.State != types.TaskFailed {
		t.Errorf("state: got %q, want %q", got.State, types.TaskFailed)
	}
	if got.Error != "provider timeout after 30s" {
		t.Errorf("error: got %q, want provider timeout", got.Error)
	}
	if got.Result != "" {
		t.Errorf("result: got %q, want empty for failed item", got.Result)
	}
}

func TestWorkRegistryUpdateNotFound(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	item := types.WorkItem{
		ID:    "nonexistent",
		State: types.TaskRunning,
	}

	err := s.UpdateWorkItem(ctx, item)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestWorkRegistryListByOwner(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create work items for two owners.
	items := []types.WorkItem{
		{ID: "w1", ParentID: "", OwnerID: "alice", Objective: "task 1", State: types.TaskPending, CreatedAt: now, UpdatedAt: now},
		{ID: "w2", ParentID: "", OwnerID: "bob", Objective: "task 2", State: types.TaskPending, CreatedAt: now.Add(1 * time.Second), UpdatedAt: now.Add(1 * time.Second)},
		{ID: "w3", ParentID: "w1", OwnerID: "alice", Objective: "task 3", State: types.TaskPending, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
	}

	for _, item := range items {
		if err := s.CreateWorkItem(ctx, item); err != nil {
			t.Fatalf("create work item %s: %v", item.ID, err)
		}
	}

	aliceItems, err := s.ListWorkItemsByOwner(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("list work items by owner: %v", err)
	}
	if len(aliceItems) != 2 {
		t.Errorf("alice items: got %d, want 2", len(aliceItems))
	}
	for _, item := range aliceItems {
		if item.OwnerID != "alice" {
			t.Errorf("owner_id: got %q, want alice", item.OwnerID)
		}
	}

	bobItems, err := s.ListWorkItemsByOwner(ctx, "bob", 10)
	if err != nil {
		t.Fatalf("list work items by owner: %v", err)
	}
	if len(bobItems) != 1 {
		t.Errorf("bob items: got %d, want 1", len(bobItems))
	}
}

func TestWorkRegistryListByParent(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create a parent with multiple children.
	parent := types.WorkItem{
		ID: "parent-001", ParentID: "", OwnerID: "alice",
		Objective: "parent objective", State: types.TaskRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateWorkItem(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	children := []types.WorkItem{
		{ID: "child-001", ParentID: "parent-001", OwnerID: "alice", Objective: "child 1", State: types.TaskPending, CreatedAt: now.Add(1 * time.Second), UpdatedAt: now.Add(1 * time.Second)},
		{ID: "child-002", ParentID: "parent-001", OwnerID: "alice", Objective: "child 2", State: types.TaskRunning, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
		{ID: "child-003", ParentID: "parent-001", OwnerID: "alice", Objective: "child 3", State: types.TaskCompleted, CreatedAt: now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second)},
	}
	for _, child := range children {
		if err := s.CreateWorkItem(ctx, child); err != nil {
			t.Fatalf("create child %s: %v", child.ID, err)
		}
	}

	// List children of parent-001.
	childrenOfParent, err := s.ListWorkItemsByParent(ctx, "parent-001", 10)
	if err != nil {
		t.Fatalf("list work items by parent: %v", err)
	}
	if len(childrenOfParent) != 3 {
		t.Errorf("children of parent-001: got %d, want 3", len(childrenOfParent))
	}
	for _, child := range childrenOfParent {
		if child.ParentID != "parent-001" {
			t.Errorf("parent_id: got %q, want parent-001", child.ParentID)
		}
	}
}

func TestWorkRegistryListByState(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	items := []types.WorkItem{
		{ID: "w1", ParentID: "", OwnerID: "alice", Objective: "obj 1", State: types.TaskPending, CreatedAt: now, UpdatedAt: now},
		{ID: "w2", ParentID: "", OwnerID: "alice", Objective: "obj 2", State: types.TaskRunning, CreatedAt: now.Add(1 * time.Second), UpdatedAt: now.Add(1 * time.Second)},
		{ID: "w3", ParentID: "", OwnerID: "alice", Objective: "obj 3", State: types.TaskCompleted, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
		{ID: "w4", ParentID: "", OwnerID: "alice", Objective: "obj 4", State: types.TaskPending, CreatedAt: now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second)},
	}
	for _, item := range items {
		if err := s.CreateWorkItem(ctx, item); err != nil {
			t.Fatalf("create work item %s: %v", item.ID, err)
		}
	}

	pendingItems, err := s.ListWorkItemsByState(ctx, types.TaskPending, 10)
	if err != nil {
		t.Fatalf("list pending work items: %v", err)
	}
	if len(pendingItems) != 2 {
		t.Errorf("pending items: got %d, want 2", len(pendingItems))
	}
	for _, item := range pendingItems {
		if item.State != types.TaskPending {
			t.Errorf("state: got %q, want pending", item.State)
		}
	}

	completedItems, err := s.ListWorkItemsByState(ctx, types.TaskCompleted, 10)
	if err != nil {
		t.Fatalf("list completed work items: %v", err)
	}
	if len(completedItems) != 1 {
		t.Errorf("completed items: got %d, want 1", len(completedItems))
	}
}

func TestWorkRegistryListByOwnerEmpty(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	items, err := s.ListWorkItemsByOwner(ctx, "nonexistent-user", 10)
	if err != nil {
		t.Fatalf("list work items: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty list for nonexistent user, got %d items", len(items))
	}
}

func TestWorkRegistryListByParentNoChildren(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create a parent with no children.
	parent := types.WorkItem{
		ID: "lonely-parent", ParentID: "", OwnerID: "alice",
		Objective: "no children", State: types.TaskRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateWorkItem(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	children, err := s.ListWorkItemsByParent(ctx, "lonely-parent", 10)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 0 {
		t.Errorf("expected no children, got %d", len(children))
	}
}

func TestWorkRegistryPersistenceAcrossReopen(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "go-choir-work-registry-reopen-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "persistence.db")
	_ = os.Remove(path)

	now := time.Now().UTC().Truncate(time.Microsecond)
	ctx := context.Background()

	// Open store, create work items, close.
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	parent := types.WorkItem{
		ID: "parent-persist", ParentID: "", OwnerID: "user-alice",
		Objective: "persistent parent", State: types.TaskRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s1.CreateWorkItem(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	child := types.WorkItem{
		ID: "child-persist", ParentID: "parent-persist", OwnerID: "user-alice",
		Objective: "persistent child", State: types.TaskPending,
		CreatedAt: now.Add(1 * time.Second), UpdatedAt: now.Add(1 * time.Second),
	}
	if err := s1.CreateWorkItem(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("close store 1: %v", err)
	}

	// Reopen and verify work items survived.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer func() {
		_ = s2.Close()
		_ = os.Remove(path)
	}()

	gotParent, err := s2.GetWorkItem(ctx, "parent-persist")
	if err != nil {
		t.Fatalf("get parent after reopen: %v", err)
	}
	if gotParent.ID != "parent-persist" {
		t.Errorf("parent id: got %q, want parent-persist", gotParent.ID)
	}
	if gotParent.State != types.TaskRunning {
		t.Errorf("parent state: got %q, want running", gotParent.State)
	}

	gotChild, err := s2.GetWorkItem(ctx, "child-persist")
	if err != nil {
		t.Fatalf("get child after reopen: %v", err)
	}
	if gotChild.ParentID != "parent-persist" {
		t.Errorf("child parent_id: got %q, want parent-persist", gotChild.ParentID)
	}

	// Verify parent-child listing works after reopen.
	children, err := s2.ListWorkItemsByParent(ctx, "parent-persist", 10)
	if err != nil {
		t.Fatalf("list children after reopen: %v", err)
	}
	if len(children) != 1 {
		t.Errorf("children after reopen: got %d, want 1", len(children))
	}
}

func TestWorkRegistryTableSchema(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	// Verify the work_items table exists with expected columns by
	// inserting and reading a fully populated record.
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := types.WorkItem{
		ID:        "schema-test",
		ParentID:  "parent-of-schema-test",
		OwnerID:   "user-schema",
		Objective: "verify schema",
		State:     types.TaskFailed,
		Result:    "",
		Error:     "schema verification error",
		CreatedAt: now,
		UpdatedAt: now.Add(1 * time.Second),
	}

	if err := s.CreateWorkItem(ctx, item); err != nil {
		t.Fatalf("create work item for schema test: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "schema-test")
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}

	// Verify all fields round-trip correctly.
	if got.ID != item.ID {
		t.Errorf("id: got %q, want %q", got.ID, item.ID)
	}
	if got.ParentID != item.ParentID {
		t.Errorf("parent_id: got %q, want %q", got.ParentID, item.ParentID)
	}
	if got.OwnerID != item.OwnerID {
		t.Errorf("owner_id: got %q, want %q", got.OwnerID, item.OwnerID)
	}
	if got.Objective != item.Objective {
		t.Errorf("objective: got %q, want %q", got.Objective, item.Objective)
	}
	if got.State != item.State {
		t.Errorf("state: got %q, want %q", got.State, item.State)
	}
	if got.Result != item.Result {
		t.Errorf("result: got %q, want %q", got.Result, item.Result)
	}
	if got.Error != item.Error {
		t.Errorf("error: got %q, want %q", got.Error, item.Error)
	}
	if !got.CreatedAt.Equal(item.CreatedAt) {
		t.Errorf("created_at: got %v, want %v", got.CreatedAt, item.CreatedAt)
	}
	if !got.UpdatedAt.Equal(item.UpdatedAt) {
		t.Errorf("updated_at: got %v, want %v", got.UpdatedAt, item.UpdatedAt)
	}
}

func TestWorkRegistryMultipleChildrenOfDifferentParents(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Two parents, each with children.
	parents := []types.WorkItem{
		{ID: "p1", ParentID: "", OwnerID: "alice", Objective: "parent 1", State: types.TaskRunning, CreatedAt: now, UpdatedAt: now},
		{ID: "p2", ParentID: "", OwnerID: "bob", Objective: "parent 2", State: types.TaskRunning, CreatedAt: now, UpdatedAt: now},
	}
	for _, p := range parents {
		if err := s.CreateWorkItem(ctx, p); err != nil {
			t.Fatalf("create parent %s: %v", p.ID, err)
		}
	}

	children := []types.WorkItem{
		{ID: "c1", ParentID: "p1", OwnerID: "alice", Objective: "child of p1", State: types.TaskPending, CreatedAt: now.Add(1 * time.Second), UpdatedAt: now.Add(1 * time.Second)},
		{ID: "c2", ParentID: "p1", OwnerID: "alice", Objective: "child of p1", State: types.TaskPending, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
		{ID: "c3", ParentID: "p2", OwnerID: "bob", Objective: "child of p2", State: types.TaskPending, CreatedAt: now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second)},
	}
	for _, c := range children {
		if err := s.CreateWorkItem(ctx, c); err != nil {
			t.Fatalf("create child %s: %v", c.ID, err)
		}
	}

	// Verify correct parent-child isolation.
	p1Children, err := s.ListWorkItemsByParent(ctx, "p1", 10)
	if err != nil {
		t.Fatalf("list p1 children: %v", err)
	}
	if len(p1Children) != 2 {
		t.Errorf("p1 children: got %d, want 2", len(p1Children))
	}

	p2Children, err := s.ListWorkItemsByParent(ctx, "p2", 10)
	if err != nil {
		t.Fatalf("list p2 children: %v", err)
	}
	if len(p2Children) != 1 {
		t.Errorf("p2 children: got %d, want 1", len(p2Children))
	}
	if p2Children[0].ID != "c3" {
		t.Errorf("p2 child id: got %q, want c3", p2Children[0].ID)
	}
}

func TestWorkRegistryListOrderByCreatedAt(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create items with distinct timestamps.
	items := []types.WorkItem{
		{ID: "w-oldest", ParentID: "", OwnerID: "alice", Objective: "oldest", State: types.TaskPending, CreatedAt: now, UpdatedAt: now},
		{ID: "w-middle", ParentID: "", OwnerID: "alice", Objective: "middle", State: types.TaskPending, CreatedAt: now.Add(5 * time.Second), UpdatedAt: now.Add(5 * time.Second)},
		{ID: "w-newest", ParentID: "", OwnerID: "alice", Objective: "newest", State: types.TaskPending, CreatedAt: now.Add(10 * time.Second), UpdatedAt: now.Add(10 * time.Second)},
	}
	for _, item := range items {
		if err := s.CreateWorkItem(ctx, item); err != nil {
			t.Fatalf("create work item %s: %v", item.ID, err)
		}
	}

	got, err := s.ListWorkItemsByOwner(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("list work items: %v", err)
	}

	// Should be ordered by created_at descending (newest first).
	if len(got) != 3 {
		t.Fatalf("items: got %d, want 3", len(got))
	}
	if got[0].ID != "w-newest" {
		t.Errorf("first item: got %q, want w-newest", got[0].ID)
	}
	if got[1].ID != "w-middle" {
		t.Errorf("second item: got %q, want w-middle", got[1].ID)
	}
	if got[2].ID != "w-oldest" {
		t.Errorf("third item: got %q, want w-oldest", got[2].ID)
	}
}

func TestWorkRegistryListWithLimit(t *testing.T) {
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	for i := 0; i < 5; i++ {
		item := types.WorkItem{
			ID:        "w-limit-" + string(rune('0'+i)),
			ParentID:  "",
			OwnerID:   "alice",
			Objective: "task",
			State:     types.TaskPending,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateWorkItem(ctx, item); err != nil {
			t.Fatalf("create work item %d: %v", i, err)
		}
	}

	items, err := s.ListWorkItemsByOwner(ctx, "alice", 3)
	if err != nil {
		t.Fatalf("list work items: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("items with limit 3: got %d, want 3", len(items))
	}
}

func TestWorkRegistryConcurrentParentChild(t *testing.T) {
	// Simulate the choir-in-choir pattern: a parent spawns multiple children,
	// each transitions through states independently.
	s := openWorkRegistryTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	parent := types.WorkItem{
		ID: "parent-concurrent", ParentID: "", OwnerID: "alice",
		Objective: "coordinate workers", State: types.TaskRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateWorkItem(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// Spawn 3 child workers.
	childIDs := []string{"worker-1", "worker-2", "worker-3"}
	for i, id := range childIDs {
		child := types.WorkItem{
			ID: id, ParentID: "parent-concurrent", OwnerID: "alice",
			Objective: "worker task " + id,
			State:     types.TaskPending,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := s.CreateWorkItem(ctx, child); err != nil {
			t.Fatalf("create child %s: %v", id, err)
		}
	}

	// Transition worker-1 to running then completed.
	w1, _ := s.GetWorkItem(ctx, "worker-1")
	w1.State = types.TaskRunning
	w1.UpdatedAt = now.Add(10 * time.Second)
	if err := s.UpdateWorkItem(ctx, w1); err != nil {
		t.Fatalf("update worker-1: %v", err)
	}
	w1.State = types.TaskCompleted
	w1.Result = "worker-1 done"
	w1.UpdatedAt = now.Add(15 * time.Second)
	if err := s.UpdateWorkItem(ctx, w1); err != nil {
		t.Fatalf("complete worker-1: %v", err)
	}

	// Transition worker-2 to failed.
	w2, _ := s.GetWorkItem(ctx, "worker-2")
	w2.State = types.TaskRunning
	w2.UpdatedAt = now.Add(10 * time.Second)
	if err := s.UpdateWorkItem(ctx, w2); err != nil {
		t.Fatalf("update worker-2: %v", err)
	}
	w2.State = types.TaskFailed
	w2.Error = "worker-2 crashed"
	w2.UpdatedAt = now.Add(12 * time.Second)
	if err := s.UpdateWorkItem(ctx, w2); err != nil {
		t.Fatalf("fail worker-2: %v", err)
	}

	// Worker-3 still pending.

	// Verify all states.
	children, err := s.ListWorkItemsByParent(ctx, "parent-concurrent", 10)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("children: got %d, want 3", len(children))
	}

	stateMap := map[string]types.TaskState{}
	for _, c := range children {
		stateMap[c.ID] = c.State
	}
	if stateMap["worker-1"] != types.TaskCompleted {
		t.Errorf("worker-1 state: got %q, want completed", stateMap["worker-1"])
	}
	if stateMap["worker-2"] != types.TaskFailed {
		t.Errorf("worker-2 state: got %q, want failed", stateMap["worker-2"])
	}
	if stateMap["worker-3"] != types.TaskPending {
		t.Errorf("worker-3 state: got %q, want pending", stateMap["worker-3"])
	}

	// Verify results/errors are correctly stored.
	w1got, _ := s.GetWorkItem(ctx, "worker-1")
	if w1got.Result != "worker-1 done" {
		t.Errorf("worker-1 result: got %q, want worker-1 done", w1got.Result)
	}
	w2got, _ := s.GetWorkItem(ctx, "worker-2")
	if w2got.Error != "worker-2 crashed" {
		t.Errorf("worker-2 error: got %q, want worker-2 crashed", w2got.Error)
	}
}
