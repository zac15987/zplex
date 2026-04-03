# zplex — Project Plan

> Version: 1.0
> Date: 2026-04-03
> Author: Jeff (zac) + Claude
> Repo: github.com/zac15987/zplex
> License: MIT

---

## 1. What is zplex?

zplex (zac + multiplex) is a terminal multiplexer desktop application purpose-built for [zpit](https://github.com/zac15987/zpit). It replaces scattered Windows Terminal tabs / tmux windows with a single unified interface — one fixed panel for zpit's TUI, plus dynamically spawned panels for each Claude Code agent.

**Relationship to zpit:**

```
Before zplex:                          After zplex:
┌──────────┐ ┌──────────┐             ┌──────────────────────────────────┐
│ WT Tab 1 │ │ WT Tab 2 │ ...散亂     │ zplex (single Electron window)   │
│ zpit TUI │ │Claude #1 │             │ ┌────────┐ ┌────────┐ ┌───────┐ │
└──────────┘ └──────────┘             │ │ zpit   │ │Claude 1│ │Claude2│ │
┌──────────┐ ┌──────────┐             │ │ (固定) │ │ (動態) │ │(動態) │ │
│ WT Tab 3 │ │ WT Tab 4 │             │ └────────┘ └────────┘ └───────┘ │
│Claude #2 │ │Claude #3 │             └──────────────────────────────────┘
└──────────┘ └──────────┘
```

**Core design principles:**
1. **Daemon/Client architecture** — Go daemon owns PTY sessions, Electron is just a display shell. Close Electron, sessions survive. Reopen, resume instantly. (Same model as tmux server/client.)
2. **zpit-native** — Not a generic terminal multiplexer. Deeply integrated with zpit's config, loop engine, and agent lifecycle.
3. **Electron from day one** — Web frontend (xterm.js) wrapped in Electron for one-click launch, system tray, and installable `.exe`.

---

## 2. Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    Electron Shell                             │
│  ┌────────────────────────────────────────────────────────┐  │
│  │  main.ts                                               │  │
│  │  - Spawns Go daemon as child process on app start      │  │
│  │  - Creates BrowserWindow loading localhost:{port}      │  │
│  │  - System tray integration (minimize to tray)          │  │
│  │  - Graceful shutdown (daemon keeps running on close)   │  │
│  └────────────────────────┬───────────────────────────────┘  │
│                           │ loads                             │
│  ┌────────────────────────▼───────────────────────────────┐  │
│  │  Frontend (HTML + TypeScript + CSS)                    │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐               │  │
│  │  │ xterm.js │ │ xterm.js │ │ xterm.js │  ...panels    │  │
│  │  │ (zpit)   │ │(Claude 1)│ │(Claude 2)│               │  │
│  │  └─────┬────┘ └────┬─────┘ └────┬─────┘               │  │
│  │        └──── WebSocket per panel ┘                     │  │
│  │                                                        │  │
│  │  layout.ts  — CSS Grid panel management                │  │
│  │  app.ts     — session lifecycle, reconnect logic       │  │
│  └────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
                            │
              HTTP REST + WebSocket (localhost)
                            │
┌───────────────────────────▼──────────────────────────────────┐
│                   Go Daemon (single binary)                   │
│                                                               │
│  server/                                                      │
│  ├─ router.go    — HTTP mux: REST endpoints + WS upgrade     │
│  ├─ api.go       — REST: session CRUD, health, layout state  │
│  └─ ws.go        — WebSocket: bidirectional PTY I/O          │
│                                                               │
│  session/                                                     │
│  ├─ manager.go   — Session registry (create, get, list, kill)│
│  └─ session.go   — Single session: PTY handle + ring buffer  │
│                    + metadata (title, status, created_at)     │
│                                                               │
│  PTY layer: aymanbagabas/go-pty                               │
│  ├─ Windows: ConPTY (CreatePseudoConsole)                    │
│  └─ Unix: /dev/ptmx (for future cross-platform)             │
│                                                               │
│  Persistence: sessions survive daemon restart?                │
│  → NO. PTY dies with daemon. Daemon is long-lived.           │
│  → Electron close ≠ daemon stop. Daemon keeps running.       │
│  → Electron reopen → reconnect to existing sessions.         │
└───────────────────────────────────────────────────────────────┘
```

### 2.1 Session Lifecycle

```
Create session:
  POST /api/sessions { shell: "powershell", cwd: "D:\...", title: "Claude #1" }
  → daemon spawns PTY → returns { id: "abc123", ws_url: "/ws/abc123" }

Connect to session:
  WebSocket /ws/{session_id}
  → daemon pipes: PTY stdout → WS → xterm.js (render)
  →               xterm.js input → WS → PTY stdin
  → On WS disconnect: PTY keeps running (daemon holds reference)
  → On WS reconnect: replay ring buffer → seamless resume

Resize:
  WS message type "resize" { cols: 120, rows: 40 }
  → daemon calls PTY.Resize()

Kill session:
  DELETE /api/sessions/{id}
  → daemon sends SIGTERM/kills PTY process → removes from registry

List sessions:
  GET /api/sessions
  → [{ id, title, status, created_at, pid }]
```

### 2.2 Ring Buffer for Reconnection

Each session maintains a ring buffer (default 100KB) of recent PTY output. When a WebSocket client reconnects, the buffer is replayed so the terminal renders the last screen state without re-running commands.

### 2.3 Daemon Port

Default: **17732** (zpit broker uses 17731, zplex daemon uses 17731 + 1).

---

## 3. Tech Stack

| Layer | Package | Version | Notes |
|---|---|---|---|
| **Go daemon** | | | |
| PTY | `aymanbagabas/go-pty` | v0.2.2 | Cross-platform (Unix PTY + Windows ConPTY) |
| WebSocket | `gorilla/websocket` | v1.5.0 | Stable, must be ≥v1.4.1 (DoS fix) |
| HTTP | `net/http` (stdlib) | — | |
| JSON | `encoding/json` (stdlib) | — | |
| **Electron** | | | |
| Runtime | `electron` | v41 | 2026-03 latest stable |
| Builder | `electron-builder` | latest | Produces `.exe` installer |
| **Frontend** | | | |
| Terminal | `@xterm/xterm` | v6.0.0 | ⚠️ v6 breaking: addons moved to `@xterm/*` scoped packages |
| Fit addon | `@xterm/addon-fit` | v6 compatible | Auto-resize terminal to container |
| WebGL addon | `@xterm/addon-webgl` | v6 compatible | GPU-accelerated rendering |
| Language | TypeScript | 5.x | |
| Bundler | esbuild or vite | latest | Fast, simple |

### 3.1 Important: xterm.js v6 Migration

xterm.js v6.0.0 (released 2025-12) has breaking changes:
- Old `xterm-addon-*` packages are **deprecated**
- Use `@xterm/xterm` and `@xterm/addon-*` (scoped packages)
- 30% bundle size reduction (379KB → 265KB)
- `overviewRulerWidth` moved to `overviewRuler` property

All issues must use the v6 scoped packages. Do NOT use the old unscoped names.

---

## 4. Repo Structure

```
zplex/
├── daemon/                     # Go daemon (standalone binary)
│   ├── go.mod                  # module github.com/zac15987/zplex/daemon
│   ├── go.sum
│   ├── main.go                 # Entry: parse flags, start HTTP server
│   ├── session/
│   │   ├── manager.go          # SessionManager: create/get/list/kill sessions
│   │   ├── session.go          # Session struct: PTY + ring buffer + metadata
│   │   └── manager_test.go
│   ├── server/
│   │   ├── router.go           # HTTP mux setup (REST + WS)
│   │   ├── api.go              # REST handlers: session CRUD, health
│   │   ├── ws.go               # WebSocket handler: PTY I/O relay
│   │   └── api_test.go
│   └── config/
│       └── config.go           # Daemon config (port, default shell, buffer size)
│
├── app/                        # Electron + Frontend
│   ├── package.json            # Dependencies: electron, @xterm/xterm, etc.
│   ├── tsconfig.json
│   ├── electron/
│   │   ├── main.ts             # Electron main process
│   │   └── preload.ts          # Context bridge (if needed)
│   └── src/
│       ├── index.html          # Single page shell
│       ├── app.ts              # App init, session management, event handling
│       ├── terminal.ts         # xterm.js wrapper: create, connect, reconnect
│       ├── layout.ts           # Panel grid: add/remove/resize panels
│       ├── styles.css          # CSS Grid layout + dark theme
│       └── types.ts            # Shared TypeScript types
│
├── scripts/
│   └── build-daemon.sh         # Cross-compile Go daemon for packaging
│
├── .claude/
│   ├── agents/
│   │   └── reviewer.md         # zplex-specific reviewer agent
│   └── docs/
│       └── tracker.md          # GitHub Issues tracker setup
│
├── CLAUDE.md                   # Claude Code agent guidance
├── LICENSE                     # MIT
└── README.md
```

---

## 5. Milestones

### M1: Skeleton — single terminal works end-to-end

**Goal:** Launch zplex → see one terminal → type commands → close window → reopen → session resumes.

| Issue | Title | Description | Depends |
|---|---|---|---|
| #1 | Go daemon: PTY session manager + WebSocket server | Session CRUD, PTY spawn (ConPTY), WebSocket bidirectional I/O, ring buffer for reconnect, REST API, health endpoint | — |
| #2 | Electron shell + xterm.js single panel | Electron main process spawns daemon, BrowserWindow loads frontend, xterm.js connects via WebSocket, basic resize handling | #1 |
| #3 | Session persistence: close Electron, reopen, resume | Electron close → daemon keeps running (not child process kill). Electron reopen → detect running daemon → reconnect. Ring buffer replay for screen restore | #2 |

**Demo scenario after M1:**
1. Run zplex → Electron window opens → single terminal panel (PowerShell)
2. Run some commands (dir, git status, etc.)
3. Close Electron window
4. Run zplex again → same terminal session, same output visible

---

### M2: Multi-panel — parallel terminals with layout management

**Goal:** Open multiple terminal panels side-by-side, resize, navigate.

| Issue | Title | Description | Depends |
|---|---|---|---|
| #4 | Frontend: multi-panel layout with CSS Grid | Add panel (button + keyboard shortcut), remove panel, CSS Grid dynamic columns/rows, each panel is an xterm.js instance connected to a separate daemon session | #3 |
| #5 | Panel resize + keyboard navigation | Drag-to-resize panel borders, keyboard shortcuts (Ctrl+Shift+Arrow to navigate, Ctrl+Shift+N to new panel), focus indicator (highlighted border) | #4 |
| #6 | Layout persistence | Save panel layout + session mapping to daemon (GET/PUT /api/layout). On reconnect, restore exact panel arrangement | #5 |

**Demo scenario after M2:**
1. Open zplex → one panel
2. Ctrl+Shift+N → second panel appears (side by side)
3. Ctrl+Shift+N → third panel → auto-arranges grid
4. Drag border to resize
5. Close and reopen → same layout restored

---

### M3: zpit fixed panel — the cockpit is always visible

**Goal:** zplex auto-launches zpit in a fixed left panel. Other panels are for agents.

| Issue | Title | Description | Depends |
|---|---|---|---|
| #7 | Daemon: auto-create zpit session on startup | Read zpit config location (env `ZPIT_CONFIG` or `~/.zpit/config.toml`). On daemon start, create a special session running `zpit` binary. Mark as "fixed" (cannot be killed from UI) | #6 |
| #8 | Frontend: fixed panel vs dynamic panels | Left panel always shows zpit session (not closable, distinct border/header). Right area is dynamic panel grid for agents. Layout: `[fixed 35%] [dynamic 65%]` adjustable | #7 |

**Demo scenario after M3:**
1. Open zplex → left panel shows zpit TUI, right area is empty
2. Manually create panels in right area → run Claude Code
3. zpit panel cannot be closed

---

### M4: zpit deep integration — agents spawn into zplex automatically

**Goal:** When zpit's loop engine spawns a Claude Code agent, it appears as a new panel in zplex instead of a new Windows Terminal tab.

| Issue | Title | Description | Depends |
|---|---|---|---|
| #9 | Daemon: session creation API with metadata | Extend POST /api/sessions to accept `source: "zpit"`, `project_id`, `issue_id`, `role` (coder/reviewer). Store metadata for UI display | #8 |
| #10 | [zpit repo] New launcher backend: zplex | Add `platform.EnvZplex` detection (check if zplex daemon is running on port 17732). Add `launchZplex()` / `launchZplexInDir()` functions that POST to daemon API instead of exec wt.exe. Add `zplex_mode` to `TerminalConfig`. Fallback: if daemon unreachable, fall back to Windows Terminal | M3 |
| #11 | Frontend: panel status sync with zpit loop | Daemon exposes SSE endpoint `/api/events` for panel status updates. When zpit loop transitions state (coding → reviewing → done), update panel header color/icon. Green = active, yellow = waiting permission, grey = done | #10 |
| #12 | Frontend: permission focus navigation | When zpit detects agent needs permission (existing signal file mechanism), zplex highlights that panel's border (pulsing yellow) and provides keyboard shortcut to jump to it | #11 |

**Integration architecture (M4 complete):**

```
zplex daemon (Go, port 17732)
  ├─ Session: zpit TUI [fixed, auto-start]
  ├─ Session: Claude Code #1 [created via REST API from zpit]
  ├─ Session: Claude Code #2 [created via REST API from zpit]
  └─ ...
       ▲
       │ POST /api/sessions
       │ { shell: "claude", args: [...], source: "zpit",
       │   project_id: "my-project", issue_id: "42", role: "coder" }
       │
zpit (running inside zplex's fixed panel)
  └─ LaunchClaudeInDir()
       └─ case platform.EnvZplex:
            → HTTP POST to zplex daemon
            → returns session ID
            → panel auto-appears in zplex UI
```

**Changes required in zpit repo (issue #10):**

| File | Change |
|---|---|
| `internal/platform/detect.go` | Add `EnvZplex` constant. Detection: try HTTP GET `localhost:17732/api/health` |
| `internal/terminal/launcher.go` | Add `case platform.EnvZplex:` branch in both `LaunchClaude()` and `LaunchClaudeInDir()` |
| `internal/terminal/launcher_zplex.go` | New file: `launchZplex()`, `launchZplexInDir()` — HTTP POST to daemon |
| `internal/config/config.go` | Add `ZplexPort int` to `TerminalConfig` (default 17732) |

---

### M5: Polish — production-ready desktop app

**Goal:** System tray, installer, one-click experience.

| Issue | Title | Description | Depends |
|---|---|---|---|
| #13 | Electron: system tray + minimize to tray | Tray icon with context menu (Show/Hide, Quit). Close button minimizes to tray (daemon keeps running). Double-click tray icon restores window | #12 |
| #14 | Electron: auto-start daemon lifecycle | On app launch: check if daemon already running → connect. If not → spawn daemon. On app "Quit" (not close): offer to stop daemon or keep running | #13 |
| #15 | electron-builder: Windows installer (.exe) | Build script: compile Go daemon for Windows amd64, bundle with Electron, produce NSIS installer. Include desktop shortcut and start menu entry | #14 |
| #16 | UX polish: theme, fonts, welcome screen | Dark theme matching zpit aesthetic. Monospace font selection. Welcome screen when no sessions exist (instructions + quick-start button) | #15 |

---

## 6. Daemon REST API Reference

### Endpoints

| Method | Path | Description | Request Body | Response |
|---|---|---|---|---|
| GET | `/api/health` | Health check + version | — | `{ status: "ok", version: "0.1.0", uptime: 3600, sessions: 3 }` |
| GET | `/api/sessions` | List all sessions | — | `[{ id, title, status, created_at, pid, source, project_id, issue_id, role }]` |
| POST | `/api/sessions` | Create new session | `{ shell, args[], cwd, title, cols, rows, env{}, source?, project_id?, issue_id?, role? }` | `{ id, ws_url }` |
| GET | `/api/sessions/{id}` | Get session detail | — | `{ id, title, status, created_at, pid, ... }` |
| DELETE | `/api/sessions/{id}` | Kill session + PTY | — | `204 No Content` |
| PATCH | `/api/sessions/{id}` | Update metadata (title, status) | `{ title?, status? }` | `200 OK` |
| GET | `/api/layout` | Get saved layout state | — | `{ panels: [{ session_id, position, size }] }` |
| PUT | `/api/layout` | Save layout state | `{ panels: [...] }` | `200 OK` |
| GET | `/api/events` | SSE stream for real-time updates | — | SSE: session.created, session.closed, session.updated |

### WebSocket

| Path | Description | Messages |
|---|---|---|
| `/ws/{session_id}` | Bidirectional PTY I/O | Client→Server: `{ type: "input", data: "..." }` or `{ type: "resize", cols: N, rows: N }` |
| | | Server→Client: `{ type: "output", data: "..." }` or `{ type: "exit", code: N }` |

---

## 7. Key Design Decisions

### 7.1 Why Electron instead of pure browser?

- **One-click launch**: User clicks zplex icon → daemon auto-starts + window opens. No "start daemon first, then open browser" friction.
- **System tray**: Close window ≠ quit. Daemon keeps running. Tray icon always accessible.
- **Installable .exe**: For distribution. Can set as startup app.
- **Future: global hotkeys**: Even when zplex is not focused.

Frontend code is pure web (HTML/TS/CSS). Could run in browser too — Electron is just the shell. No Electron-specific APIs in frontend code (except through preload bridge).

### 7.2 Why Go daemon instead of Node.js (Electron native)?

- **Same language as zpit** — can share config parsing, types, conventions.
- **Single binary** — no Node.js runtime needed for daemon. Easy to distribute.
- **Session persistence** — daemon as standalone process naturally outlives Electron window.
- **Performance** — Go handles concurrent PTY I/O + WebSocket efficiently.

### 7.3 Why gorilla/websocket over nhooyr.io/websocket?

- More examples in training data → higher Claude Code implementation accuracy.
- Stable API (v1.5.0). In maintenance mode but no known issues.
- Performance adequate for terminal I/O (not high-frequency trading).

### 7.4 Why not embed terminal in zpit TUI directly?

- "TUI inside TUI" problem: rendering Claude Code (itself a rich TUI) inside another Bubble Tea TUI via `charmbracelet/x/vt` (pre-v1 experimental) would be extremely fragile.
- xterm.js is a proven, VS Code-grade terminal emulator. Zero rendering risk.
- Separation of concerns: zpit = dispatch logic, zplex = display layer.

### 7.5 Daemon port: 17732

- zpit broker: 17731
- zplex daemon: 17732 (17731 + 1)
- Configurable via CLI flag `--port` or env `ZPLEX_PORT`.

---

## 8. Configuration

zplex reads its own config file: `~/.zplex/config.toml`

```toml
# zplex Configuration

[daemon]
port = 17732                     # HTTP + WebSocket port
default_shell = "powershell"     # Default shell for new sessions
buffer_size = 102400             # Ring buffer size per session (bytes, default 100KB)

[zpit]
enabled = true                   # Auto-create zpit fixed panel
bin = "zpit"                     # Path to zpit binary (or "zpit" if in PATH)
config = ""                      # Custom zpit config path (empty = default ~/.zpit/config.toml)
args = []                        # Extra args to pass to zpit

[electron]
minimize_to_tray = true          # Close button → minimize to tray
start_daemon = true              # Auto-start daemon on app launch

[appearance]
theme = "dark"                   # dark | light (future)
font_family = "Cascadia Code"   # Terminal font
font_size = 14                   # Terminal font size
```

---

## 9. zpit Config Changes (M4)

When zplex integration is added to zpit, the config.toml gains:

```toml
[terminal]
windows_mode = "new_tab"
tmux_mode = "new_window"
zplex_port = 17732               # NEW: zplex daemon port (0 = disabled/auto-detect)
```

Detection priority in `LaunchClaude()`:
1. If zplex daemon reachable on `zplex_port` → use zplex
2. Else if Windows Terminal → use wt.exe
3. Else if tmux → use tmux
4. Else → error

---

## 10. Development Plan

### 10.1 Bootstrapping

1. Create GitHub repo: `github.com/zac15987/zplex`
2. Write `CLAUDE.md` (build/run instructions, package structure, conventions)
3. Write `.claude/docs/tracker.md` (GitHub Issues setup)
4. Add zplex as a project in zpit's `~/.zpit/config.toml`
5. Use zpit to create issues from this plan (dogfooding!)

### 10.2 Conventions (same as zpit where applicable)

- **Branch naming**: `feat/ISSUE-ID-slug`
- **Git model**: `main` ← `dev` ← feature branches
- **Commit messages**: `[ISSUE-ID] short description`
- **Go code style**: Follow standard Go conventions. `gofmt`. No global state.
- **TypeScript**: Strict mode. No `any`. Prefer `const`.
- **Logging**: Go daemon: `log/slog` (structured logging). Frontend: `console.warn/error` only.

### 10.3 Dogfooding Timeline

| Phase | What | Using |
|---|---|---|
| M1-M2 | Build zplex basic functionality | zpit + Windows Terminal (existing flow) |
| M3 | zplex can display zpit | Start using zplex to monitor zpit |
| M4 | Full integration | Switch entirely to zplex as the development interface |
| M5 | Polish | Zplex developing itself inside zplex |

---

## 11. References

### Technologies
- [xterm.js v6.0.0](https://github.com/xtermjs/xterm.js/releases/tag/6.0.0) — Breaking changes, scoped packages
- [go-pty](https://github.com/aymanbagabas/go-pty) — Cross-platform PTY (MIT)
- [gorilla/websocket](https://github.com/gorilla/websocket) — Go WebSocket (BSD-2)
- [Electron v41](https://releases.electronjs.org/) — Desktop runtime
- [Windows ConPTY](https://devblogs.microsoft.com/commandline/windows-command-line-introducing-the-windows-pseudo-console-conpty/) — Windows pseudo console API

### Inspiration
- [BridgeSpace](https://www.bridgemind.ai/products/bridgespace) — Commercial agent workspace (multi-panel + kanban)
- [claude-squad](https://github.com/smtg-ai/claude-squad) — Go TUI, tmux-based, 6.8k stars (AGPL)
- [agent-deck](https://github.com/asheshgoplani/agent-deck) — Go, tmux + web UI, 1.9k stars (MIT)
- [psmux](https://github.com/marlocarlo/psmux) — Native Windows tmux alternative in Rust (MIT)

### zpit Integration Points
- `internal/terminal/launcher.go` — Current launch dispatch (wt.exe / tmux switch)
- `internal/platform/detect.go` — Environment detection
- `internal/config/config.go` — TerminalConfig struct
- `docs/architecture/03-system-architecture.md` — Terminal Launcher module design
