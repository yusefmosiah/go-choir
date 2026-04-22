package store

import (
	"context"
	"testing"
	"time"

	"github.com/yusefmosiah/go-choir/internal/types"
)

func TestDesktopStateGetEmpty(t *testing.T) {
	db := openTestStore(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	// Getting desktop state for a user with no saved state should return
	// an empty default state.
	state, err := db.GetDesktopState(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetDesktopState: %v", err)
	}

	if state.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", state.OwnerID, "user-1")
	}
	if len(state.Windows) != 0 {
		t.Errorf("Windows count = %d, want 0", len(state.Windows))
	}
	if state.ActiveWindowID != "" {
		t.Errorf("ActiveWindowID = %q, want empty", state.ActiveWindowID)
	}
}

func TestDesktopStateSaveAndGet(t *testing.T) {
	db := openTestStore(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	now := time.Now().UTC()
	state := types.DesktopState{
		OwnerID: "user-1",
		Windows: []types.WindowState{
			{
				WindowID: "win-1",
				AppID:    "vtext",
				Title:    "E-Text Editor",
				Geometry: types.WindowGeometry{X: 100, Y: 100, Width: 600, Height: 400},
				Mode:     types.WindowNormal,
				ZIndex:   1,
				AppContext: map[string]any{
					"document_id": "doc-abc",
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
			{
				WindowID:  "win-2",
				AppID:     "terminal",
				Title:     "Terminal",
				Geometry:  types.WindowGeometry{X: 200, Y: 200, Width: 500, Height: 300},
				Mode:      types.WindowMinimized,
				ZIndex:    0,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		ActiveWindowID: "win-1",
		UpdatedAt:      now,
	}

	if err := db.SaveDesktopState(ctx, state); err != nil {
		t.Fatalf("SaveDesktopState: %v", err)
	}

	got, err := db.GetDesktopState(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetDesktopState: %v", err)
	}

	if got.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", got.OwnerID, "user-1")
	}
	if len(got.Windows) != 2 {
		t.Fatalf("Windows count = %d, want 2", len(got.Windows))
	}
	if got.ActiveWindowID != "win-1" {
		t.Errorf("ActiveWindowID = %q, want %q", got.ActiveWindowID, "win-1")
	}

	// Verify first window.
	w1 := got.Windows[0]
	if w1.WindowID != "win-1" {
		t.Errorf("Window[0].WindowID = %q, want %q", w1.WindowID, "win-1")
	}
	if w1.AppID != "vtext" {
		t.Errorf("Window[0].AppID = %q, want %q", w1.AppID, "vtext")
	}
	if w1.Geometry.X != 100 || w1.Geometry.Y != 100 || w1.Geometry.Width != 600 || w1.Geometry.Height != 400 {
		t.Errorf("Window[0].Geometry = %+v, want {100 100 600 400}", w1.Geometry)
	}
	if w1.Mode != types.WindowNormal {
		t.Errorf("Window[0].Mode = %q, want %q", w1.Mode, types.WindowNormal)
	}
	if w1.AppContext["document_id"] != "doc-abc" {
		t.Errorf("Window[0].AppContext[document_id] = %v, want %q", w1.AppContext["document_id"], "doc-abc")
	}

	// Verify second window.
	w2 := got.Windows[1]
	if w2.Mode != types.WindowMinimized {
		t.Errorf("Window[1].Mode = %q, want %q", w2.Mode, types.WindowMinimized)
	}
}

func TestDesktopStateUpdate(t *testing.T) {
	db := openTestStore(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	// Save initial state.
	now := time.Now().UTC()
	state := types.DesktopState{
		OwnerID: "user-1",
		Windows: []types.WindowState{
			{
				WindowID:  "win-1",
				AppID:     "vtext",
				Title:     "E-Text",
				Geometry:  types.WindowGeometry{X: 100, Y: 100, Width: 600, Height: 400},
				Mode:      types.WindowNormal,
				ZIndex:    1,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		ActiveWindowID: "win-1",
		UpdatedAt:      now,
	}

	if err := db.SaveDesktopState(ctx, state); err != nil {
		t.Fatalf("SaveDesktopState: %v", err)
	}

	// Update the state: maximize the window.
	state.Windows[0].Mode = types.WindowMaximized
	state.Windows[0].RestoredGeometry = &types.WindowGeometry{X: 100, Y: 100, Width: 600, Height: 400}
	state.Windows[0].Geometry = types.WindowGeometry{X: 0, Y: 0, Width: 1920, Height: 1080}
	state.UpdatedAt = time.Now().UTC()

	if err := db.SaveDesktopState(ctx, state); err != nil {
		t.Fatalf("SaveDesktopState update: %v", err)
	}

	// Verify the update.
	got, err := db.GetDesktopState(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetDesktopState: %v", err)
	}

	if got.Windows[0].Mode != types.WindowMaximized {
		t.Errorf("Window[0].Mode = %q, want %q", got.Windows[0].Mode, types.WindowMaximized)
	}
	if got.Windows[0].RestoredGeometry == nil {
		t.Error("Window[0].RestoredGeometry is nil, want saved geometry")
	} else if got.Windows[0].RestoredGeometry.Width != 600 {
		t.Errorf("Window[0].RestoredGeometry.Width = %d, want 600", got.Windows[0].RestoredGeometry.Width)
	}
}

func TestDesktopStateIsolation(t *testing.T) {
	db := openTestStore(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	now := time.Now().UTC()

	// Save state for user-1.
	state1 := types.DesktopState{
		OwnerID: "user-1",
		Windows: []types.WindowState{
			{WindowID: "win-a", AppID: "vtext", Title: "User 1 Doc", Geometry: types.WindowGeometry{X: 10, Y: 10, Width: 400, Height: 300}, Mode: types.WindowNormal, ZIndex: 1, CreatedAt: now, UpdatedAt: now},
		},
		ActiveWindowID: "win-a",
		UpdatedAt:      now,
	}
	if err := db.SaveDesktopState(ctx, state1); err != nil {
		t.Fatalf("SaveDesktopState user-1: %v", err)
	}

	// Save state for user-2.
	state2 := types.DesktopState{
		OwnerID: "user-2",
		Windows: []types.WindowState{
			{WindowID: "win-b", AppID: "terminal", Title: "User 2 Terminal", Geometry: types.WindowGeometry{X: 20, Y: 20, Width: 500, Height: 400}, Mode: types.WindowNormal, ZIndex: 1, CreatedAt: now, UpdatedAt: now},
		},
		ActiveWindowID: "win-b",
		UpdatedAt:      now,
	}
	if err := db.SaveDesktopState(ctx, state2); err != nil {
		t.Fatalf("SaveDesktopState user-2: %v", err)
	}

	// Verify user-1's state is not affected by user-2's save.
	got1, err := db.GetDesktopState(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetDesktopState user-1: %v", err)
	}
	if len(got1.Windows) != 1 || got1.Windows[0].AppID != "vtext" {
		t.Errorf("user-1 desktop state was affected by user-2 save")
	}

	// Verify user-2's state is independent.
	got2, err := db.GetDesktopState(ctx, "user-2")
	if err != nil {
		t.Fatalf("GetDesktopState user-2: %v", err)
	}
	if len(got2.Windows) != 1 || got2.Windows[0].AppID != "terminal" {
		t.Errorf("user-2 desktop state incorrect")
	}
}

func TestDesktopStateWithMaximizedRestore(t *testing.T) {
	db := openTestStore(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	now := time.Now().UTC()
	restored := types.WindowGeometry{X: 50, Y: 50, Width: 800, Height: 600}

	state := types.DesktopState{
		OwnerID: "user-1",
		Windows: []types.WindowState{
			{
				WindowID:         "win-1",
				AppID:            "vtext",
				Title:            "E-Text",
				Geometry:         types.WindowGeometry{X: 0, Y: 0, Width: 1920, Height: 1080},
				RestoredGeometry: &restored,
				Mode:             types.WindowMaximized,
				ZIndex:           1,
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		},
		ActiveWindowID: "win-1",
		UpdatedAt:      now,
	}

	if err := db.SaveDesktopState(ctx, state); err != nil {
		t.Fatalf("SaveDesktopState: %v", err)
	}

	got, err := db.GetDesktopState(ctx, "user-1")
	if err != nil {
		t.Fatalf("GetDesktopState: %v", err)
	}

	if got.Windows[0].RestoredGeometry == nil {
		t.Fatal("RestoredGeometry is nil after save/load round-trip")
	}
	rg := got.Windows[0].RestoredGeometry
	if rg.X != 50 || rg.Y != 50 || rg.Width != 800 || rg.Height != 600 {
		t.Errorf("RestoredGeometry = %+v, want {50 50 800 600}", rg)
	}
}

func TestDesktopStateMultipleDesktopsPerUser(t *testing.T) {
	db := openTestStore(t)
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	now := time.Now().UTC()

	primary := types.DesktopState{
		OwnerID:   "user-1",
		DesktopID: types.PrimaryDesktopID,
		Windows: []types.WindowState{
			{WindowID: "win-primary", AppID: "vtext", Title: "Primary", Geometry: types.WindowGeometry{X: 10, Y: 10, Width: 400, Height: 300}, Mode: types.WindowNormal, ZIndex: 1, CreatedAt: now, UpdatedAt: now},
		},
		ActiveWindowID: "win-primary",
		UpdatedAt:      now,
	}
	branch := types.DesktopState{
		OwnerID:   "user-1",
		DesktopID: "branch-a",
		Windows: []types.WindowState{
			{WindowID: "win-branch", AppID: "terminal", Title: "Branch", Geometry: types.WindowGeometry{X: 20, Y: 20, Width: 500, Height: 400}, Mode: types.WindowNormal, ZIndex: 1, CreatedAt: now, UpdatedAt: now},
		},
		ActiveWindowID: "win-branch",
		UpdatedAt:      now,
	}

	if err := db.SaveDesktopStateForDesktop(ctx, primary); err != nil {
		t.Fatalf("SaveDesktopStateForDesktop primary: %v", err)
	}
	if err := db.SaveDesktopStateForDesktop(ctx, branch); err != nil {
		t.Fatalf("SaveDesktopStateForDesktop branch: %v", err)
	}

	gotPrimary, err := db.GetDesktopStateForDesktop(ctx, "user-1", types.PrimaryDesktopID)
	if err != nil {
		t.Fatalf("GetDesktopStateForDesktop primary: %v", err)
	}
	gotBranch, err := db.GetDesktopStateForDesktop(ctx, "user-1", "branch-a")
	if err != nil {
		t.Fatalf("GetDesktopStateForDesktop branch: %v", err)
	}

	if len(gotPrimary.Windows) != 1 || gotPrimary.Windows[0].WindowID != "win-primary" {
		t.Fatalf("primary desktop mismatch: %+v", gotPrimary.Windows)
	}
	if len(gotBranch.Windows) != 1 || gotBranch.Windows[0].WindowID != "win-branch" {
		t.Fatalf("branch desktop mismatch: %+v", gotBranch.Windows)
	}
	if gotPrimary.DesktopID != types.PrimaryDesktopID {
		t.Errorf("primary DesktopID = %q, want %q", gotPrimary.DesktopID, types.PrimaryDesktopID)
	}
	if gotBranch.DesktopID != "branch-a" {
		t.Errorf("branch DesktopID = %q, want %q", gotBranch.DesktopID, "branch-a")
	}
}
