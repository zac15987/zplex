# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is zplex?

zplex (zac + multiplex) is a terminal multiplexer desktop app purpose-built for [zpit](https://github.com/zac15987/zpit). It replaces scattered terminal tabs with a single Electron window — one fixed panel for zpit's TUI, plus dynamically spawned panels for each Claude Code agent.

**Daemon/Client architecture**: A Go daemon owns PTY sessions and a WebSocket/REST API. Electron is a display shell only. Closing Electron does NOT kill the daemon or sessions — reopening reconnects and replays via ring buffer.

## Build & Run

### Convenience Scripts (repo root, PowerShell)

```powershell
./build.ps1                # compile both components (daemon exe + frontend)
./build.ps1 -DaemonOnly    # rebuild only the Go daemon
./build.ps1 -AppOnly       # build only the frontend
./build.ps1 -Install       # force npm install, then build both

./dev.ps1                  # one-click: rebuild daemon, then `npm run dev`
./dev.ps1 -Install         # refresh npm deps first, then rebuild + launch

./kill-daemon.ps1          # stop the daemon left running after closing the app
./kill-daemon.ps1 -Port N  # target a custom --port / ZPLEX_PORT
```

`dev.ps1` is the day-to-day UI test loop (single terminal). It rebuilds
`daemon/zplex-daemon.exe` first so the binary `npm run dev` auto-spawns contains
the latest Go code — without this, dev mode silently runs a stale daemon. The
auto-spawned daemon's output is discarded (`stdio: "ignore"`); to see daemon
logs, run `go run .` manually in a separate terminal (the app detects the
already-running daemon and skips spawning).

The daemon is spawned detached and survives Electron exit by design (see
`app/electron/main.ts`), so closing the app leaves it running. During the test
loop, run `./kill-daemon.ps1` after closing the app to stop it — it finds the
process owning the daemon port and kills it (no Task Manager needed).

### Go Daemon (`daemon/`)

```bash
cd daemon
go build -o zplex-daemon.exe .    # build
go run .                           # run (default port 17732)
go run . --port 17732              # explicit port
go test ./...                      # all tests
go test ./session/                 # session package tests
go test ./server/                  # server package tests (REST + WebSocket)
go test -run TestSessionCreate ./session/  # single test
```

### Electron App (`app/`)

```bash
cd app
npm install                        # install deps
npm run dev                        # dev mode (launches Electron + frontend)
npm run build                      # production build
npx electron-builder               # produce .exe installer
```

### Full Stack Dev

Easiest path: `./dev.ps1` (rebuilds the daemon, then launches the app, which auto-spawns the freshly built daemon).

Manual path: start the daemon first (`cd daemon; go run .`), then the Electron app (`cd app; npm run dev`). The Electron main process auto-spawns the daemon as a child process only when one isn't already running — running it manually lets you see daemon logs.

## Architecture

```
Electron (main.ts)
  → spawns Go daemon (child process)
  → creates BrowserWindow → loads frontend

Frontend (TypeScript + xterm.js)
  → one xterm.js instance per panel
  → each connects via WebSocket to /ws/{session_id}
  → layout managed by CSS Grid + workspace tabs

Go Daemon (single binary, port 17732)
  → REST API: session CRUD, health, layout state
  → WebSocket: bidirectional PTY I/O
  → PTY via aymanbagabas/go-pty (ConPTY on Windows)
  → ring buffer (100KB default) per session for reconnect replay
```

### Multi-Panel Layout

The frontend uses an auto-tiled CSS Grid layout engine (`app/src/layout.ts`) that manages terminal panels dynamically:

- **PanelGrid class** (`layout.ts`): Manages panel placement, gutter resize, and focus tracking.
- **Auto-tile algorithm**: Panels auto-arrange in 1-2 rows. Columns = ceil(N/2). Bottom row panels span to fill width when fewer than top row.
- **Maximum 8 panels** per workspace. Attempts to add more are silently ignored with a console warning.
- **Workspace-scoped registry**: Sessions are organized as `Map<workspaceId, Map<sessionId, TerminalWrapper>>` in `app.ts`. Current default workspace is `"default"`.
- **Gutter drag resize**: Panels can be resized by dragging gutter bars between them. Minimum panel size: 120px wide, 80px tall.

### Workspace Tabs

The frontend supports multiple workspaces, each with its own set of terminal panels:

- **WorkspaceManager** (`workspace.ts`): Manages workspace lifecycle — create, switch, close, rename. Renders the tab bar UI. Fires callbacks that `app.ts` handles for terminal dispose/recreate.
- **Dispose/Recreate mechanism**: When switching workspaces, all xterm.js instances in the current workspace are disposed (freeing memory + WebGL contexts). The target workspace's panels are recreated from daemon layout data, with ring buffer replay providing near-instant recovery.
- **Maximum 8 workspaces**. Attempts to add more are ignored with a console warning.
- **Auto-naming**: New workspaces are named "Workspace N" (N auto-increments). Double-click tab to rename.
- **Active workspace persistence**: Current workspace ID stored in `localStorage` key `zplex:activeWorkspace` (pure UI state — kept in the frontend). Restored on app restart.
- **Close workspace**: Shows confirmation dialog if workspace has running sessions. The remembered detach/kill choice for both panel close and workspace close is owned by the daemon and persisted to `~/.zplex/preferences.json` via `GET/PUT /api/preferences` (loaded once at startup into a frontend cache). Last workspace cannot be closed.

### Fixed zpit Cockpit Panel

The terminal area is wrapped in `#main-split` = `[#fixed-panel-area] [#v-splitter] [#grid-container]`. The zpit cockpit terminal mounts into `#fixed-panel-area` and is never part of `PanelGrid` — auto-tile, gutter resize, focus navigation, and layout persistence are entirely untouched.

Key behaviors:

- **Global persistence**: the fixed panel is created once at init and survives workspace switch and workspace close — it is visible in every workspace.
- **Exit overlay**: when zpit exits, the fixed panel shows a `zpit exited` overlay with a manual `Restart` button. There is NO auto-restart.
- **Graceful fallback**: when `[zpit].enabled` is false or the binary is missing, the daemon creates no fixed session and the frontend falls back to the pure dynamic grid (M2 behavior). `#main-split` carries class `no-fixed`, which hides `#fixed-panel-area` and `#v-splitter`.
- **Split ratio**: the fixed/dynamic split ratio is pure UI state stored in `localStorage` key `zplex:fixedPanelRatio` (default `0.35`). It is NOT in daemon preferences.

### Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Ctrl+Shift+N` | Create new terminal panel |
| `Ctrl+Shift+T` | Create new workspace tab |
| `Ctrl+Shift+W` | Close focused panel (shows dialog or uses saved preference) |
| `Ctrl+Shift+Arrow` | Move focus to adjacent panel (Up/Down/Left/Right); `Ctrl+Shift+Left` from the leftmost grid panel crosses into the zpit fixed panel, and `Ctrl+Shift+Right` from the zpit fixed panel crosses back to the leftmost grid panel |
| `Ctrl+Shift+PageUp` | Switch to previous workspace (wraps around) |
| `Ctrl+Shift+PageDown` | Switch to next workspace (wraps around) |
| `Shift+Click [×]` | Force close dialog (override saved preference) |

### Daemon REST API

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/health` | Health check + version |
| GET/POST | `/api/sessions` | List / create sessions |
| GET/DELETE/PATCH | `/api/sessions/{id}` | Get / kill / update session; DELETE returns HTTP 403 for the fixed (`kind=="fixed"`) session — it cannot be killed from the UI |
| GET/PUT | `/api/layout` | Get / save panel layout (per workspace) |
| GET | `/api/workspaces` | List workspace IDs with layout data |
| DELETE | `/api/workspaces/{id}` | Delete workspace layout data |
| GET/PUT | `/api/preferences` | Get / save app-managed user preferences (close behavior), persisted to `~/.zplex/preferences.json` |
| GET | `/api/events` | SSE stream for real-time updates |
| POST | `/api/zpit/restart` | Kill (if running) and relaunch the fixed zpit session |

### WebSocket Protocol (`/ws/{session_id}`)

- Client→Server: `{ type: "input", data: "..." }` or `{ type: "resize", cols: N, rows: N }`
- Server→Client: `{ type: "output", data: "..." }` or `{ type: "exit", code: N }`

### SessionInfo `kind` field

`SessionInfo` (and `Session`) carry a `kind` field in all `GET /api/sessions` and `GET /api/sessions/{id}` responses: `""` for normal/agent sessions, `"fixed"` for the zpit cockpit session.

## Tech Stack Constraints

- **xterm.js v6**: Use `@xterm/xterm` and `@xterm/addon-*` (scoped packages). The old unscoped `xterm-addon-*` packages are deprecated and must NOT be used.
- **Go PTY**: `aymanbagabas/go-pty` — handles ConPTY (Windows) and /dev/ptmx (Unix).
- **WebSocket**: `gorilla/websocket` v1.5.3 — must be >=v1.4.1 (DoS fix).
- **Go logging**: `log/slog` (structured). No `fmt.Println` for logging.
- **Frontend logging**: `console.warn`/`console.error` only. No `console.log` in production code.
- **TypeScript**: Strict mode. No `any`. Prefer `const`.
- **Frontend code must be Electron-agnostic** — no Electron APIs in `app/src/`. Only use Electron APIs through the preload bridge in `app/electron/`.

## Conventions

- **Branch naming**: `feat/ISSUE-ID-slug` (e.g., `feat/1-pty-session-manager`)
- **Git model**: `main` <- `dev` <- feature branches
- **Commit messages**: `[ISSUE-ID] short description` (e.g., `[#1] add session manager with PTY spawn`)
- **Go style**: `gofmt`. No global state.
- **Go module**: `github.com/zac15987/zplex/daemon`

## Key Port Numbers

- zpit broker: **17731**
- zplex daemon: **17732** (configurable via `--port` flag or `ZPLEX_PORT` env)

## Configuration

zplex config lives at `~/.zplex/config.toml`. Key sections: `[daemon]` (port, default_shell, buffer_size), `[zpit]` (see below), `[electron]` (tray behavior, daemon lifecycle), `[appearance]` (theme, font). This file is **read-only** to the daemon (loaded once at startup; user hand-edited).

**`[zpit]` section keys:**

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Auto-launch the cockpit fixed panel on daemon startup |
| `bin` | string | `"zpit"` | Binary name or path; resolved via PATH when not absolute |
| `config` | string | `""` | Optional path injected as the `ZPIT_CONFIG` env var; omitted when empty |
| `args` | string array | `[]` | Extra arguments passed to the binary |

The daemon does NOT parse zpit's own TOML — it only spawns the binary and optionally sets `ZPIT_CONFIG`.

App-managed mutable preferences (currently close behavior) live separately in `~/.zplex/preferences.json`, owned by the daemon's `prefs` package and read/written at runtime via `GET/PUT /api/preferences`. Kept apart from `config.toml` so runtime writes never clobber the user's hand-edited TOML.

## zpit Integration

zplex is tightly coupled with zpit. In M4+, zpit's `LaunchClaude()` detects a running zplex daemon and POSTs to its API to create agent panels instead of opening new terminal tabs. Sessions carry metadata: `source`, `project_id`, `issue_id`, `role` (coder/reviewer).

## Interaction Rules

- **Do NOT modify code unless explicitly asked.** When the user asks a question, answer it — do not edit files. Wait for clear instruction (e.g., "fix it", "change it", "update it") before making changes.
