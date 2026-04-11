/**
 * App entry point -- manages session lifecycle and multi-panel layout.
 *
 * On DOM ready:
 *   1. Initialize PanelGrid on the grid-container element.
 *   2. Fetch existing sessions from the daemon (GET /api/sessions).
 *   3. Reconnect to all running sessions (ring buffer replay), or create one.
 *   4. Wire up the "+" button for spawning additional panels.
 *
 * Sessions are organized in a workspace-scoped registry:
 *   Map<workspaceId, Map<sessionId, TerminalWrapper>>
 */

import { TerminalWrapper } from "./terminal";
import { PanelGrid } from "./layout";
import type {
  SessionInfo,
  CreateSessionRequest,
  CreateSessionResponse,
  ClosePreference,
  CloseDialogResult,
} from "./types";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const DEFAULT_DAEMON_PORT = 17732;
const DEFAULT_COLS = 80;
const DEFAULT_ROWS = 24;
const DEFAULT_WORKSPACE_ID = "default";
const CLOSE_PREFERENCE_KEY = "zplex:closePreference";

// ---------------------------------------------------------------------------
// Workspace-scoped session registry
// ---------------------------------------------------------------------------

/** Workspace-scoped session registry: Map<workspaceId, Map<sessionId, TerminalWrapper>> */
const workspaces = new Map<string, Map<string, TerminalWrapper>>();

/** Get or create the session map for a workspace. */
function getWorkspaceRegistry(workspaceId: string): Map<string, TerminalWrapper> {
  let registry = workspaces.get(workspaceId);
  if (!registry) {
    registry = new Map<string, TerminalWrapper>();
    workspaces.set(workspaceId, registry);
  }
  return registry;
}

// ---------------------------------------------------------------------------
// Grid instance
// ---------------------------------------------------------------------------

let panelGrid: PanelGrid | null = null;

// ---------------------------------------------------------------------------
// Daemon communication helpers
// ---------------------------------------------------------------------------

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

/** Fire-and-forget DELETE against the daemon REST API. */
async function apiDelete(path: string): Promise<void> {
  const port = getDaemonPort();
  const resp = await fetch(`http://localhost:${port}${path}`, { method: "DELETE" });
  if (!resp.ok) {
    throw new Error(`DELETE ${path} failed: ${resp.status} ${resp.statusText}`);
  }
}

// ---------------------------------------------------------------------------
// Panel creation and mounting
// ---------------------------------------------------------------------------

/**
 * Create a new session on the daemon, add a panel to the grid, mount an xterm
 * instance into it, and register everything.
 */
async function createAndMountPanel(title?: string): Promise<void> {
  console.warn("[app] createAndMountPanel entry");

  if (!panelGrid) {
    console.error("[app] panelGrid not initialized");
    return;
  }

  // Create session on daemon
  const request: CreateSessionRequest = {
    shell: "pwsh", // Uses daemon's default_shell in production
    title: title ?? `Terminal ${panelGrid.getPanelCount() + 1}`,
    cols: DEFAULT_COLS,
    rows: DEFAULT_ROWS,
  };

  const result = await apiPost<CreateSessionRequest, CreateSessionResponse>(
    "/api/sessions",
    request,
  );

  mountSessionToGrid(result.id, request.title);

  console.warn("[app] createAndMountPanel exit, session:", result.id);
}

/**
 * Add a panel to the grid for an existing session, mount xterm, and wire up
 * focus/dispose callbacks.
 */
function mountSessionToGrid(sessionId: string, title: string): void {
  console.warn("[app] mountSessionToGrid entry:", sessionId);

  if (!panelGrid) {
    console.error("[app] panelGrid not initialized");
    return;
  }

  const panelInfo = panelGrid.addPanel(sessionId, title, DEFAULT_WORKSPACE_ID);
  if (!panelInfo) {
    // Max panels reached
    return;
  }

  const port = getDaemonPort();
  const wrapper = new TerminalWrapper(sessionId, port);

  // Register focus callback: when terminal is clicked, set panel focus
  wrapper.onFocus((id: string) => {
    panelGrid?.setFocus(id);
    wrapper.focusTerminal();
  });

  // Register in workspace registry
  const registry = getWorkspaceRegistry(DEFAULT_WORKSPACE_ID);
  registry.set(sessionId, wrapper);

  // Mount xterm into the panel's content area
  wrapper.mount(panelInfo.contentElement);

  // Wire up header click-to-focus
  panelInfo.headerElement.addEventListener("mousedown", () => {
    panelGrid?.setFocus(sessionId);
    wrapper.focusTerminal();
  });

  // Set focus on the newly created panel
  panelGrid.setFocus(sessionId);
  wrapper.focusTerminal();

  console.warn("[app] mountSessionToGrid exit:", sessionId);
}

// ---------------------------------------------------------------------------
// Terminal re-fit
// ---------------------------------------------------------------------------

/**
 * Re-fit all terminal instances to their containers.
 * Called after panel add/remove/resize to update xterm dimensions.
 */
function refitAllTerminals(): void {
  // Use requestAnimationFrame to ensure DOM has settled
  requestAnimationFrame(() => {
    for (const [, registry] of workspaces) {
      for (const [, wrapper] of registry) {
        wrapper.fit();
      }
    }
  });
}

// ---------------------------------------------------------------------------
// Initialization
// ---------------------------------------------------------------------------

/**
 * Core initialization sequence:
 *   - Create PanelGrid on the grid-container element.
 *   - Wire up resize and "+" button callbacks.
 *   - Reconnect to all running sessions, or create a new one.
 */
async function init(): Promise<void> {
  console.warn("[app] init entry");
  const port = getDaemonPort();
  console.warn("[app] daemon port:", port);

  // Initialize the grid
  const container = document.getElementById("grid-container");
  if (!container) {
    throw new Error("grid-container element not found in DOM");
  }
  panelGrid = new PanelGrid(container);

  // Register resize callback for FitAddon
  panelGrid.onPanelResize(() => {
    refitAllTerminals();
  });

  // Wire up the "+" button
  const addBtn = document.getElementById("add-panel-btn");
  if (addBtn) {
    addBtn.addEventListener("click", () => {
      createAndMountPanel().catch((err: unknown) => {
        console.error("[app] failed to create panel:", err);
      });
    });
  }

  // Fetch existing sessions and reconnect
  const sessionList = await apiGet<SessionInfo[]>("/api/sessions");
  const runningSessions = sessionList.filter((s) => s.status === "running");

  if (runningSessions.length > 0) {
    console.warn("[app] reconnecting to", runningSessions.length, "running sessions");
    for (const session of runningSessions) {
      mountSessionToGrid(session.id, session.title);
    }
  } else {
    // No running sessions — create a fresh one
    console.warn("[app] no running sessions, creating initial session");
    await createAndMountPanel("Terminal 1");
  }

  console.warn("[app] init exit");
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

document.addEventListener("DOMContentLoaded", () => {
  init().catch((err: unknown) => {
    console.error("[app] initialization failed:", err);
  });
});
