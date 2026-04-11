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
import { WorkspaceManager } from "./workspace";
import type {
  SessionInfo,
  CreateSessionRequest,
  CreateSessionResponse,
  ClosePreference,
  CloseDialogResult,
  LayoutState,
  WorkspaceInfo,
  WorkspaceListResponse,
} from "./types";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const DEFAULT_DAEMON_PORT = 17732;
const DEFAULT_COLS = 80;
const DEFAULT_ROWS = 24;
const DEFAULT_WORKSPACE_ID = "default";
const PANEL_CLOSE_PREF_KEY = "zplex:panelClosePreference";
const WORKSPACE_CLOSE_PREF_KEY = "zplex:workspaceClosePreference";

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
let wsManager: WorkspaceManager | null = null;

/** Return the active workspace ID, falling back to DEFAULT_WORKSPACE_ID. */
function activeWorkspaceId(): string {
  return wsManager?.getActiveId() ?? DEFAULT_WORKSPACE_ID;
}

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

/** Typed PUT against the daemon REST API. */
async function apiPut<TReq>(path: string, body: TReq): Promise<void> {
  const port = getDaemonPort();
  const resp = await fetch(`http://localhost:${port}${path}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    throw new Error(`PUT ${path} failed: ${resp.status} ${resp.statusText}`);
  }
}

// ---------------------------------------------------------------------------
// Layout persistence
// ---------------------------------------------------------------------------

/**
 * Save the current layout state to the daemon via PUT /api/layout.
 * Called automatically on panel add/remove and gutter drag end.
 */
function saveLayout(): void {
  if (!panelGrid || !wsManager) {
    return;
  }

  const workspaceId = wsManager.getActiveId();
  const state: LayoutState = panelGrid.serializeLayout();
  console.warn("[app] saveLayout: saving", state.panels.length, "panels for workspace:", workspaceId);

  apiPut(`/api/layout?workspace=${encodeURIComponent(workspaceId)}`, state).catch((err: unknown) => {
    console.error("[app] saveLayout: failed to save layout:", err);
  });
}

// ---------------------------------------------------------------------------
// Panel creation and mounting
// ---------------------------------------------------------------------------

/** Return the next available "Terminal N" name that doesn't collide with existing panels. */
function nextTerminalName(): string {
  const titles = new Set(panelGrid?.getExistingTitles() ?? []);
  let n = 1;
  while (titles.has(`Terminal ${n}`)) n++;
  return `Terminal ${n}`;
}

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
    title: title ?? nextTerminalName(),
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

  const currentWorkspace = activeWorkspaceId();
  const panelInfo = panelGrid.addPanel(sessionId, title, currentWorkspace);
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
  const registry = getWorkspaceRegistry(currentWorkspace);
  registry.set(sessionId, wrapper);

  // Mount xterm into the panel's content area
  wrapper.mount(panelInfo.contentElement);

  // Wire up header click-to-focus
  panelInfo.headerElement.addEventListener("mousedown", () => {
    panelGrid?.setFocus(sessionId);
    wrapper.focusTerminal();
  });

  // Wire up close button [x] (AC-8, AC-10)
  const closeBtn = panelInfo.headerElement.querySelector(".panel-close-btn");
  if (closeBtn) {
    closeBtn.addEventListener("click", (e: Event) => {
      const mouseEvent = e as MouseEvent;
      // Shift+click forces dialog even if preference is saved (AC-10)
      const forceDialog = mouseEvent.shiftKey;
      closePanelFlow(sessionId, forceDialog).catch((err: unknown) => {
        console.error("[app] close panel failed:", err);
      });
    });
  }

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
// Close panel flow
// ---------------------------------------------------------------------------

/**
 * Show the close confirmation dialog and return the user's choice.
 * Returns null if cancelled.
 */
function showCloseDialog(savedPreference?: ClosePreference): Promise<CloseDialogResult | null> {
  return new Promise<CloseDialogResult | null>((resolve) => {
    const dialog = document.getElementById("close-dialog") as HTMLDialogElement | null;
    if (!dialog) {
      console.error("[app] close-dialog element not found");
      resolve(null);
      return;
    }

    // Reset dialog state
    const detachRadio = document.getElementById("close-detach") as HTMLInputElement | null;
    const killRadio = document.getElementById("close-kill") as HTMLInputElement | null;
    const rememberCheckbox = document.getElementById("close-remember") as HTMLInputElement | null;
    const cancelBtn = document.getElementById("close-cancel");
    const confirmBtn = document.getElementById("close-confirm");

    // Pre-select saved preference if available (for Shift+click override)
    if (savedPreference === "kill" && killRadio) {
      killRadio.checked = true;
    } else if (detachRadio) {
      detachRadio.checked = true;
    }
    if (rememberCheckbox) rememberCheckbox.checked = false;

    // Clean up function to remove listeners
    const cleanup = (): void => {
      dialog.close();
      cancelBtn?.removeEventListener("click", onCancel);
      confirmBtn?.removeEventListener("click", onConfirm);
    };

    const onCancel = (): void => {
      cleanup();
      resolve(null);
    };

    const onConfirm = (): void => {
      const action: ClosePreference = killRadio?.checked ? "kill" : "detach";
      const remember = rememberCheckbox?.checked ?? false;
      cleanup();
      resolve({ action, remember });
    };

    cancelBtn?.addEventListener("click", onCancel);
    confirmBtn?.addEventListener("click", onConfirm);

    dialog.showModal();
  });
}

/**
 * Execute the close action on a panel: remove from grid, dispose terminal,
 * and optionally kill the daemon session.
 */
async function executeClose(sessionId: string, action: ClosePreference): Promise<void> {
  console.warn("[app] executeClose entry:", sessionId, "action:", action);

  // Kill session on daemon if requested
  if (action === "kill") {
    try {
      await apiDelete(`/api/sessions/${sessionId}`);
      console.warn("[app] session killed on daemon:", sessionId);
    } catch (err: unknown) {
      console.error("[app] failed to kill session:", sessionId, err);
    }
  }

  // Dispose terminal wrapper
  const currentWorkspace = activeWorkspaceId();
  const registry = getWorkspaceRegistry(currentWorkspace);
  const wrapper = registry.get(sessionId);
  if (wrapper) {
    wrapper.dispose();
    registry.delete(sessionId);
  }

  // Remove panel from grid
  if (panelGrid) {
    panelGrid.removePanel(sessionId);

    // Auto-focus the next available panel
    const remainingIds = panelGrid.getPanelIds();
    if (remainingIds.length > 0) {
      const nextId = remainingIds[remainingIds.length - 1];
      panelGrid.setFocus(nextId);
      const nextWrapper = registry.get(nextId);
      if (nextWrapper) {
        nextWrapper.focusTerminal();
      }
    }
  }

  // Refit remaining terminals after layout change
  refitAllTerminals();

  console.warn("[app] executeClose exit:", sessionId);
}

/**
 * Initiate the close flow for a panel.
 *
 * If a saved preference exists and forceDialog is false, skip the dialog.
 * Otherwise show the HTML <dialog> element for user choice.
 */
async function closePanelFlow(sessionId: string, forceDialog: boolean): Promise<void> {
  console.warn("[app] closePanelFlow entry:", sessionId, "forceDialog:", forceDialog);

  // Read saved preference once for both skip-dialog and pre-select paths
  const saved = localStorage.getItem(PANEL_CLOSE_PREF_KEY) as ClosePreference | null;
  const validSaved = (saved === "detach" || saved === "kill") ? saved : undefined;

  // Use saved preference directly if not forcing dialog (AC-9)
  if (!forceDialog && validSaved) {
    await executeClose(sessionId, validSaved);
    return;
  }

  // Show dialog, pre-selecting saved preference if available (AC-10)
  const result = await showCloseDialog(validSaved);
  if (!result) {
    // User cancelled
    console.warn("[app] closePanelFlow cancelled by user");
    return;
  }

  // Save preference if requested (AC-9)
  if (result.remember) {
    localStorage.setItem(PANEL_CLOSE_PREF_KEY, result.action);
    console.warn("[app] close preference saved:", result.action);
  }

  await executeClose(sessionId, result.action);

  console.warn("[app] closePanelFlow exit:", sessionId);
}

// ---------------------------------------------------------------------------
// Focus navigation
// ---------------------------------------------------------------------------

/**
 * Move focus to the adjacent panel in the given direction.
 * If no adjacent panel exists, focus stays on the current panel.
 */
function moveFocus(direction: "up" | "down" | "left" | "right"): void {
  if (!panelGrid) return;

  const currentId = panelGrid.getFocusedPanelId();
  if (!currentId) return;

  const adjacentId = panelGrid.getAdjacentPanelId(currentId, direction);
  if (!adjacentId) return;

  panelGrid.setFocus(adjacentId);
  const registry = getWorkspaceRegistry(activeWorkspaceId());
  const wrapper = registry.get(adjacentId);
  if (wrapper) {
    wrapper.focusTerminal();
  }
}

// ---------------------------------------------------------------------------
// Workspace switching
// ---------------------------------------------------------------------------

/**
 * Handle workspace switch: dispose current panels, fetch target layout,
 * recreate panels for the target workspace.
 */
async function handleWorkspaceSwitch(fromId: string, toId: string): Promise<void> {
  console.warn("[app] handleWorkspaceSwitch entry:", fromId, "->", toId);

  if (!panelGrid) return;

  // 1. Dispose all xterm.js instances in the previous workspace (if any remain).
  // Use workspaces.get (not getWorkspaceRegistry) to avoid re-creating an entry
  // for a workspace that was already cleaned up by handleWorkspaceClose.
  const fromRegistry = workspaces.get(fromId);
  if (fromRegistry) {
    for (const [, wrapper] of fromRegistry) {
      wrapper.dispose();
    }
    fromRegistry.clear();
  }

  // 2. Clear the grid (remove all panel DOM elements)
  panelGrid.disposeAllPanels();

  // 3. Fetch target workspace layout from daemon
  const layoutState = await apiGet<LayoutState>(
    `/api/layout?workspace=${encodeURIComponent(toId)}`,
  );

  // 4. Fetch running sessions for title lookup
  const sessionList = await apiGet<SessionInfo[]>("/api/sessions");
  const runningSessions = sessionList.filter((s) => s.status === "running");
  const runningIds = new Set(runningSessions.map((s) => s.id));
  const titleMap = new Map<string, string>();
  for (const s of runningSessions) {
    titleMap.set(s.id, s.title);
  }

  const hasLayout = layoutState.panels.length > 0;

  if (hasLayout) {
    // Mount panels from saved layout
    const sortedPanels = [...layoutState.panels].sort((a, b) => a.position - b.position);
    const mountedIds = new Set<string>();

    for (const panel of sortedPanels) {
      if (runningIds.has(panel.session_id)) {
        const title = titleMap.get(panel.session_id) ?? "Terminal";
        mountSessionToGrid(panel.session_id, title);
        mountedIds.add(panel.session_id);
      }
    }

    // Apply saved grid template if panel count matches
    if (mountedIds.size === layoutState.panels.length && mountedIds.size > 0) {
      panelGrid.applyLayoutTemplate(
        layoutState.grid_template_columns,
        layoutState.grid_template_rows,
      );
    }
  }

  // 5. Refit all terminals
  refitAllTerminals();

  console.warn("[app] handleWorkspaceSwitch exit:", toId);
}

/**
 * Handle workspace close: check for running sessions, show dialog if needed,
 * dispose terminals, and clean up daemon layout data.
 */
async function handleWorkspaceClose(workspaceId: string): Promise<void> {
  console.warn("[app] handleWorkspaceClose entry:", workspaceId);

  const runningSessions: string[] = [];

  // Check which sessions in this workspace are still running.
  // Use daemon layout API instead of in-memory registry, because the registry
  // is cleared when switching away from a workspace (dispose/recreate).
  try {
    const layoutState = await apiGet<LayoutState>(
      `/api/layout?workspace=${encodeURIComponent(workspaceId)}`,
    );
    const sessionList = await apiGet<SessionInfo[]>("/api/sessions");
    const runningIds = new Set(
      sessionList.filter((s) => s.status === "running").map((s) => s.id),
    );
    for (const panel of layoutState.panels) {
      if (runningIds.has(panel.session_id)) {
        runningSessions.push(panel.session_id);
      }
    }
  } catch (err: unknown) {
    console.error("[app] handleWorkspaceClose: failed to fetch sessions:", err);
  }

  // If there are running sessions, show close dialog
  if (runningSessions.length > 0) {
    const saved = localStorage.getItem(WORKSPACE_CLOSE_PREF_KEY) as ClosePreference | null;
    const validSaved = (saved === "detach" || saved === "kill") ? saved : undefined;

    let action: ClosePreference = "detach";

    if (validSaved) {
      action = validSaved;
    } else {
      // Update dialog text for workspace context
      const dialogTitle = document.querySelector("#close-dialog .dialog-title");
      const originalText = dialogTitle?.textContent ?? "Close Panel";
      if (dialogTitle) {
        dialogTitle.textContent = `此 workspace 有 ${runningSessions.length} 個執行中的 session`;
      }

      const result = await showCloseDialog(validSaved);

      // Restore dialog text
      if (dialogTitle) {
        dialogTitle.textContent = originalText;
      }

      if (!result) {
        // User cancelled — abort workspace close
        throw new Error("workspace close cancelled by user");
      }

      if (result.remember) {
        localStorage.setItem(WORKSPACE_CLOSE_PREF_KEY, result.action);
      }
      action = result.action;
    }

    // Kill sessions if requested
    if (action === "kill") {
      for (const sessionId of runningSessions) {
        try {
          await apiDelete(`/api/sessions/${sessionId}`);
        } catch (err: unknown) {
          console.error("[app] handleWorkspaceClose: failed to kill session:", sessionId, err);
        }
      }
    }
  }

  // Dispose all xterm.js instances in this workspace (if any are still mounted)
  const registry = workspaces.get(workspaceId);
  if (registry) {
    for (const [, wrapper] of registry) {
      wrapper.dispose();
    }
    registry.clear();
  }
  workspaces.delete(workspaceId);

  // If this was the active workspace, clear the grid
  if (wsManager && workspaceId === wsManager.getActiveId()) {
    panelGrid?.disposeAllPanels();
  }

  // Delete workspace layout from daemon
  try {
    await apiDelete(`/api/workspaces/${encodeURIComponent(workspaceId)}`);
  } catch (err: unknown) {
    console.error("[app] handleWorkspaceClose: failed to delete workspace layout:", err);
  }

  console.warn("[app] handleWorkspaceClose exit:", workspaceId);
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

  // NOTE: onLayoutChange callback is registered AFTER reconciliation completes
  // to prevent intermediate saves from overwriting the daemon's saved custom
  // grid template with auto-tiled values during mount.

  // Initialize WorkspaceManager
  wsManager = new WorkspaceManager();

  // Register workspace callbacks
  wsManager.onSwitch((fromId: string, toId: string) => {
    handleWorkspaceSwitch(fromId, toId).catch((err: unknown) => {
      console.error("[app] workspace switch failed:", err);
    });
  });

  wsManager.onCreate((_workspace: WorkspaceInfo) => {
    // New workspace starts empty — panel will be created after switch
  });

  wsManager.onClose(async (workspaceId: string) => {
    await handleWorkspaceClose(workspaceId);
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

  // Keyboard shortcuts (AC-5, AC-6, AC-7, workspace shortcuts)
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    // All shortcuts require Ctrl+Shift
    if (!e.ctrlKey || !e.shiftKey) {
      return;
    }

    switch (e.key) {
      // Ctrl+Shift+N: New panel (AC-5)
      case "N":
      case "n": {
        e.preventDefault();
        createAndMountPanel().catch((err: unknown) => {
          console.error("[app] failed to create panel via shortcut:", err);
        });
        break;
      }

      // Ctrl+Shift+W: Close focused panel (AC-6, AC-8)
      case "W":
      case "w": {
        e.preventDefault();
        const focusedId = panelGrid?.getFocusedPanelId();
        if (focusedId) {
          // AC-8: Don't close the last panel in the last workspace
          const panelCount = panelGrid?.getPanelCount() ?? 0;
          const workspaceCount = wsManager?.getCount() ?? 1;
          if (panelCount <= 1 && workspaceCount <= 1) {
            console.warn("[app] cannot close last panel in last workspace");
            break;
          }
          closePanelFlow(focusedId, false).catch((err: unknown) => {
            console.error("[app] failed to close panel via shortcut:", err);
          });
        }
        break;
      }

      // Ctrl+Shift+T: New workspace (AC-4)
      case "T":
      case "t": {
        e.preventDefault();
        wsManager?.createWorkspace();
        break;
      }

      // Ctrl+Shift+PageUp: Previous workspace (AC-6)
      case "PageUp": {
        e.preventDefault();
        wsManager?.switchPrevious();
        break;
      }

      // Ctrl+Shift+PageDown: Next workspace (AC-6)
      case "PageDown": {
        e.preventDefault();
        wsManager?.switchNext();
        break;
      }

      // Ctrl+Shift+Arrow: Move focus (AC-7)
      case "ArrowUp": {
        e.preventDefault();
        moveFocus("up");
        break;
      }
      case "ArrowDown": {
        e.preventDefault();
        moveFocus("down");
        break;
      }
      case "ArrowLeft": {
        e.preventDefault();
        moveFocus("left");
        break;
      }
      case "ArrowRight": {
        e.preventDefault();
        moveFocus("right");
        break;
      }
    }
  });

  // Fetch workspace list from daemon
  let workspaceIds: string[] = [];
  try {
    workspaceIds = await apiGet<WorkspaceListResponse>("/api/workspaces");
  } catch (err: unknown) {
    console.warn("[app] failed to fetch workspace list, starting fresh:", err);
  }

  // Initialize WorkspaceManager (sets active workspace from localStorage or first)
  wsManager.initFromList(workspaceIds);

  const activeWorkspace = wsManager.getActiveId();
  console.warn("[app] active workspace:", activeWorkspace);

  // Fetch sessions and layout state for the active workspace
  const sessionList = await apiGet<SessionInfo[]>("/api/sessions");
  const runningSessions = sessionList.filter((s) => s.status === "running");
  const layoutState = await apiGet<LayoutState>(
    `/api/layout?workspace=${encodeURIComponent(activeWorkspace)}`,
  );

  const hasLayout = layoutState.panels.length > 0;
  const runningIds = new Set(runningSessions.map((s) => s.id));

  if (hasLayout) {
    console.warn("[app] restore: layout found with", layoutState.panels.length, "panels");

    // Build a title lookup from running sessions
    const titleMap = new Map<string, string>();
    for (const s of runningSessions) {
      titleMap.set(s.id, s.title);
    }

    // (a) Mount panels from layout in position order (only if session still exists)
    const sortedPanels = [...layoutState.panels].sort((a, b) => a.position - b.position);
    const mountedIds = new Set<string>();

    for (const panel of sortedPanels) {
      if (runningIds.has(panel.session_id)) {
        const title = titleMap.get(panel.session_id) ?? "Terminal";
        mountSessionToGrid(panel.session_id, title);
        mountedIds.add(panel.session_id);
      } else {
        // (c) Layout references a dead session — skip
        console.warn("[app] restore: skipping dead session:", panel.session_id);
      }
    }

    // (b) Append sessions that exist but are not in layout
    for (const s of runningSessions) {
      if (!mountedIds.has(s.id)) {
        console.warn("[app] restore: appending unlisted session:", s.id);
        mountSessionToGrid(s.id, s.title);
        mountedIds.add(s.id);
      }
    }

    // AC-8: If actual panel count differs from layout record count
    // (due to skipped or appended sessions), use auto-tiled grid template.
    // Only apply saved grid template when counts match exactly.
    const layoutPanelCount = layoutState.panels.length;
    const actualPanelCount = mountedIds.size;

    if (actualPanelCount === layoutPanelCount && actualPanelCount > 0) {
      // Panel count matches — apply saved grid template values
      console.warn("[app] restore: applying saved grid template");
      panelGrid.applyLayoutTemplate(
        layoutState.grid_template_columns,
        layoutState.grid_template_rows,
      );
    } else if (actualPanelCount !== layoutPanelCount) {
      // Count mismatch — auto-tiled grid was already set by rebuildGrid
      console.warn("[app] restore: panel count mismatch (layout:", layoutPanelCount,
        "actual:", actualPanelCount, "), using auto-tiled grid");
    }

    // If no panels were mounted at all, create a fresh one
    if (mountedIds.size === 0) {
      console.warn("[app] restore: no sessions survived reconciliation, creating initial session");
      await createAndMountPanel("Terminal 1");
    }
  } else if (runningSessions.length > 0) {
    // AC-9: No saved layout — fall back to default behavior
    console.warn("[app] no layout saved, reconnecting to", runningSessions.length, "running sessions");
    for (const session of runningSessions) {
      mountSessionToGrid(session.id, session.title);
    }
  } else {
    // No sessions and no layout — create a fresh one
    console.warn("[app] no running sessions, creating initial session");
    await createAndMountPanel("Terminal 1");
  }

  // AC-10: Refit all terminals after reconciliation
  refitAllTerminals();

  // Register layout auto-save callback AFTER reconciliation is complete.
  // During reconciliation, each addPanel() triggers rebuildGrid() which would
  // fire this callback with auto-tiled grid template values, overwriting the
  // daemon's saved custom template. Deferring registration prevents this.
  panelGrid.onLayoutChange(() => {
    saveLayout();
  });

  // Persist the final post-reconciliation layout state (with correct grid
  // template — either restored custom values or auto-tiled) in a single save.
  saveLayout();

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
