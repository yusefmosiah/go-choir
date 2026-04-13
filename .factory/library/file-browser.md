# File Browser Frontend Component (M6 Responsive-File-Browser)

## Overview

The FileBrowser.svelte component provides a file/directory browser within the ChoirOS desktop. It renders inside a FloatingWindow when the user clicks the Files icon in the left rail.

## Component

### FileBrowser.svelte
- Located at `frontend/src/lib/FileBrowser.svelte`
- Renders inside the `[data-files-app]` container in Desktop.svelte
- Communicates with the backend via `/api/files/*` endpoints
- Uses `fetchWithRenewal` from `./auth.js` for authenticated API calls

## Features

- **File/directory listing** with folder (📁) and file (📄) icons
- **Breadcrumb navigation** with clickable segments (Root / dir1 / dir2)
- **Click directory** to navigate into it
- **Click file** to trigger download (Content-Disposition: attachment)
- **New Folder** button with inline input (no alert/prompt)
- **Delete** with inline confirmation (no confirm())
- **Empty state** message ("This folder is empty")
- **Error display** for permission issues and other API errors
- **Back/forward navigation** with history tracking
- **Responsive**: full-width in mobile focus mode, >=44px touch targets

## API Endpoints (Backend)

- `GET /api/files` — list root directory
- `GET /api/files/{path}` — list subdirectory or download file
- `POST /api/files/{path}` — create directory (returns 201 or 409)
- `DELETE /api/files/{path}` — delete file/folder (returns 204)

The proxy routes `/api/files*` to the sandbox service (port 8085).

## Data Attributes (Test Selectors)

| Selector | Description |
|----------|-------------|
| `[data-file-list]` | File listing container |
| `[data-file-item]` | Individual file/directory row |
| `[data-entry-type]` | "file" or "directory" on each item |
| `[data-file-icon]` | Folder/file icon span |
| `[data-file-name]` | File/directory name span |
| `[data-file-size]` | File size span |
| `[data-breadcrumb]` | Breadcrumb navigation container |
| `[data-breadcrumb-segment]` | Clickable path segment |
| `[data-new-folder-btn]` | New folder button |
| `[data-new-folder-input]` | Inline folder name input |
| `[data-new-folder-confirm]` | Confirm new folder button |
| `[data-delete-btn]` | Delete button on file item |
| `[data-delete-confirm]` | Confirm delete button |
| `[data-delete-cancel]` | Cancel delete button |
| `[data-empty-state]` | Empty directory message |
| `[data-error-message]` | Error message display |
| `[data-nav-back]` | Back navigation button |
| `[data-nav-forward]` | Forward navigation button |

## Test Coverage

- `file-browser.spec.js`: 12 tests covering VAL-FILES-001, 003, 004, 005, 006, 009, 011, 012, 013, 016, 018
