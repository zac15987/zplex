/**
 * App entry point -- manages session lifecycle.
 *
 * On DOM ready:
 *   1. Fetch existing sessions from the daemon (GET /api/sessions).
 *   2. If a running session exists, reconnect to it (ring buffer replay).
 *   3. Otherwise, create a new session (POST /api/sessions) and mount it.
 *
 * Maintains a session registry (Map<string, TerminalWrapper>) that is ready
 * for multi-panel expansion in M2 without refactoring.
 */

import { TerminalWrapper } from "./terminal";
import type {
  SessionInfo,
  CreateSessionRequest,
  CreateSessionResponse,
} from "./types";

// ---------------------------------------------------------------------------
// Session registry
// ---------------------------------------------------------------------------

/** Maps session ID to its TerminalWrapper. Supports future multi-panel. */
const sessions = new Map<string, TerminalWrapper>();

// ---------------------------------------------------------------------------
// Daemon communication helpers
// ---------------------------------------------------------------------------

const DEFAULT_DAEMON_PORT = 17732;
const DEFAULT_COLS = 80;
const DEFAULT_ROWS = 24;

/** Resolve the daemon port from the preload bridge, falling back to default. */
function getDaemonPort(): number {
  // window.zplex is injected by the Electron preload script.
  // When running outside Electron (e.g. browser dev), fall back gracefully.
  return window.zplex?.daemonPort ?? DEFAULT_DAEMON_PORT;
}

/** Typed GET against the daemon REST API. */
async function apiGet<T>(path: string): Promise<T> {
  const port = getDaemonPort();
  const resp = await fetch(`http://localhost:${port}${path}`);
  if (!resp.ok) {
    throw new Error(`GET ${path} failed: ${resp.status} ${resp.statusText}`);
  }
  return resp.json() as Promise<T>;
}

/** Typed POST against the daemon REST API. */
async function apiPost<TReq, TRes>(path: string, body: TReq): Promise<TRes> {
  const port = getDaemonPort();
  const resp = await fetch(`http://localhost:${port}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    throw new Error(`POST ${path} failed: ${resp.status} ${resp.statusText}`);
  }
  return resp.json() as Promise<TRes>;
}

// ---------------------------------------------------------------------------
// Terminal mounting
// ---------------------------------------------------------------------------

/**
 * Create a TerminalWrapper for the given session, mount it into the DOM
 * container, and register it in the session map.
 */
function mountTerminal(sessionId: string): TerminalWrapper {
  const port = getDaemonPort();
  const container = document.getElementById("terminal-container");
  if (!container) {
    throw new Error("terminal-container element not found in DOM");
  }

  const wrapper = new TerminalWrapper(sessionId, port);
  wrapper.mount(container);
  sessions.set(sessionId, wrapper);
  return wrapper;
}

// ---------------------------------------------------------------------------
// Initialization
// ---------------------------------------------------------------------------

/**
 * Core initialization sequence:
 *   - Query daemon for existing sessions.
 *   - Reconnect to the first running session, or create a new one.
 */
async function init(): Promise<void> {
  const port = getDaemonPort();
  console.warn("[app] initializing, daemon port:", port);

  // Fetch existing sessions from the daemon.
  const sessionList = await apiGet<SessionInfo[]>("/api/sessions");
  const runningSession = sessionList.find((s) => s.status === "running");

  if (runningSession) {
    // Reconnect -- the daemon's ring buffer will replay recent output (AC-9).
    console.warn("[app] reconnecting to session:", runningSession.id);
    mountTerminal(runningSession.id);
  } else {
    // No running session -- create a fresh one (AC-5).
    // Use default 80x24; FitAddon sends correct dimensions after mount.
    console.warn("[app] no running sessions, creating new session");
    const request: CreateSessionRequest = {
      shell: "pwsh",
      title: "Terminal",
      cols: DEFAULT_COLS,
      rows: DEFAULT_ROWS,
    };
    const result = await apiPost<CreateSessionRequest, CreateSessionResponse>(
      "/api/sessions",
      request,
    );
    console.warn("[app] session created:", result.id);
    mountTerminal(result.id);
  }
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

document.addEventListener("DOMContentLoaded", () => {
  init().catch((err: unknown) => {
    console.error("[app] initialization failed:", err);
  });
});
