# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is zplex?

zplex (zac + multiplex) is a terminal multiplexer desktop app purpose-built for [zpit](https://github.com/zac15987/zpit). It replaces scattered terminal tabs with a single Electron window — one fixed panel for zpit's TUI, plus dynamically spawned panels for each Claude Code agent.

**Daemon/Client architecture**: A Go daemon owns PTY sessions and a WebSocket/REST API. Electron is a display shell only. Closing Electron does NOT kill the daemon or sessions — reopening reconnects and replays via ring buffer.

## Build & Run

### Go Daemon (`daemon/`)

```bash
cd daemon
go build -o zplex-daemon.exe .    # build
go run .                           # run (default port 17732)
go run . --port 17732              # explicit port
go test ./...                      # all tests
go test ./session/                 # single package tests
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

Start the daemon first, then the Electron app. The Electron main process can also auto-spawn the daemon as a child process.

## Architecture

```
Electron (main.ts)
  → spawns Go daemon (child process)
  → creates BrowserWindow → loads frontend

Frontend (TypeScript + xterm.js)
  → one xterm.js instance per panel
  → each connects via WebSocket to /ws/{session_id}
  → layout managed by CSS Grid

Go Daemon (single binary, port 17732)
  → REST API: session CRUD, health, layout state
  → WebSocket: bidirectional PTY I/O
  → PTY via aymanbagabas/go-pty (ConPTY on Windows)
  → ring buffer (100KB default) per session for reconnect replay
```

### Daemon REST API

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/health` | Health check + version |
| GET/POST | `/api/sessions` | List / create sessions |
| GET/DELETE/PATCH | `/api/sessions/{id}` | Get / kill / update session |
| GET/PUT | `/api/layout` | Get / save panel layout |
| GET | `/api/events` | SSE stream for real-time updates |

### WebSocket Protocol (`/ws/{session_id}`)

- Client→Server: `{ type: "input", data: "..." }` or `{ type: "resize", cols: N, rows: N }`
- Server→Client: `{ type: "output", data: "..." }` or `{ type: "exit", code: N }`

## Tech Stack Constraints

- **xterm.js v6**: Use `@xterm/xterm` and `@xterm/addon-*` (scoped packages). The old unscoped `xterm-addon-*` packages are deprecated and must NOT be used.
- **Go PTY**: `aymanbagabas/go-pty` — handles ConPTY (Windows) and /dev/ptmx (Unix).
- **WebSocket**: `gorilla/websocket` v1.5.0 — must be >=v1.4.1 (DoS fix).
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

zplex config lives at `~/.zplex/config.toml`. Key sections: `[daemon]` (port, default_shell, buffer_size), `[zpit]` (auto-create fixed panel), `[electron]` (tray behavior, daemon lifecycle), `[appearance]` (theme, font).

## zpit Integration

zplex is tightly coupled with zpit. In M4+, zpit's `LaunchClaude()` detects a running zplex daemon and POSTs to its API to create agent panels instead of opening new terminal tabs. Sessions carry metadata: `source`, `project_id`, `issue_id`, `role` (coder/reviewer).
