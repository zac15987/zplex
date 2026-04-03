# zplex

A terminal multiplexer desktop app purpose-built for [zpit](https://github.com/zac15987/zpit).

Replaces scattered terminal tabs with a single Electron window — one fixed panel for zpit's TUI, plus dynamically spawned panels for each Claude Code agent.

## Architecture

**Daemon/Client model** (like tmux server/client):

- **Go daemon** — owns PTY sessions, exposes REST API + WebSocket on `localhost:17732`
- **Electron shell** — spawns the daemon, renders terminals via xterm.js
- Close the window, sessions survive. Reopen, resume instantly.

```
┌─────────────────────────────────────┐
│ zplex (Electron)                    │
│ ┌─────────┐ ┌──────────┐ ┌───────┐ │
│ │  zpit   │ │ Claude 1 │ │Claude2│ │
│ │ (fixed) │ │ (dynamic) │ │(dyn.) │ │
│ └─────────┘ └──────────┘ └───────┘ │
└──────────────┬──────────────────────┘
          WebSocket per panel
               │
┌──────────────▼──────────────────────┐
│ Go Daemon (port 17732)              │
│ PTY sessions + ring buffer + REST   │
└─────────────────────────────────────┘
```

## Tech Stack

| Layer | Key Packages |
|-------|-------------|
| Go daemon | `aymanbagabas/go-pty`, `gorilla/websocket`, `net/http` |
| Electron | Electron v41, `electron-builder` |
| Frontend | `@xterm/xterm` v6, TypeScript, CSS Grid |

## License

[MIT](LICENSE)
