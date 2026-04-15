// Package types defines desktop state types for the go-choir desktop shell.
//
// Desktop state represents the persisted window layout and app context for a
// user's desktop session. It is stored server-side so that desktop restore
// works across fresh browser contexts for the same user (VAL-DESKTOP-007).
//
// Design decisions:
//   - Window IDs are UUID strings, stable across saves and restores.
//   - Geometry is stored as integer pixel values for deterministic layout.
//   - WindowMode captures the three desktop window states: normal, minimized,
//     maximized.
//   - The activeWindowId tracks which window has focus for z-order management.
//   - App context (e.g., document ID for E-Text) is stored per-window so that
//     restored windows can reattach to the correct app state.
package types

import "time"

// WindowMode represents the display mode of a desktop window.
type WindowMode string

const (
	// WindowNormal means the window is displayed at its specified geometry.
	WindowNormal WindowMode = "normal"

	// WindowMinimized means the window is collapsed into the taskbar.
	// The window's app state is preserved (VAL-DESKTOP-005).
	WindowMinimized WindowMode = "minimized"

	// WindowMaximized means the window fills the desktop area.
	// The window's previous geometry is saved for restore (VAL-DESKTOP-005).
	WindowMaximized WindowMode = "maximized"
)

// Valid returns true if the WindowMode value is a recognized mode.
func (m WindowMode) Valid() bool {
	switch m {
	case WindowNormal, WindowMinimized, WindowMaximized:
		return true
	default:
		return false
	}
}

// WindowGeometry stores the position and size of a desktop window.
type WindowGeometry struct {
	// X is the horizontal position in pixels from the left of the desktop.
	X int `json:"x"`

	// Y is the vertical position in pixels from the top of the desktop.
	Y int `json:"y"`

	// Width is the window width in pixels.
	Width int `json:"width"`

	// Height is the window height in pixels.
	Height int `json:"height"`
}

// WindowState represents the persisted state of a single desktop window.
type WindowState struct {
	// WindowID is the unique identifier for this window.
	WindowID string `json:"window_id"`

	// AppID identifies the application running in this window (e.g., "vtext",
	// "terminal", "files").
	AppID string `json:"app_id"`

	// Title is the window title shown in the title bar.
	Title string `json:"title"`

	// Geometry is the window position and size.
	Geometry WindowGeometry `json:"geometry"`

	// RestoredGeometry holds the geometry before maximize, so that restoring
	// from maximized returns to the previous size and position
	// (VAL-DESKTOP-005).
	RestoredGeometry *WindowGeometry `json:"restored_geometry,omitempty"`

	// Mode is the current display mode (normal, minimized, maximized).
	Mode WindowMode `json:"mode"`

	// ZIndex controls the stacking order. Higher values are on top.
	ZIndex int `json:"z_index"`

	// AppContext holds app-specific state (e.g., document ID for E-Text)
	// that should be restored when the window is reopened.
	AppContext map[string]any `json:"app_context,omitempty"`

	// CreatedAt is when the window was first opened.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the window state was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}

// DesktopState represents the full persisted desktop state for a user.
// This is what gets saved and restored for VAL-DESKTOP-007.
type DesktopState struct {
	// OwnerID is the authenticated user who owns this desktop state.
	OwnerID string `json:"owner_id"`

	// Windows is the list of open windows.
	Windows []WindowState `json:"windows"`

	// ActiveWindowID is the ID of the currently focused window, or empty
	// if no window is active.
	ActiveWindowID string `json:"active_window_id,omitempty"`

	// UpdatedAt is when the desktop state was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}
