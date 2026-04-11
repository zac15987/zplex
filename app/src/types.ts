/**
 * Shared TypeScript type definitions for the zplex frontend.
 *
 * These types mirror the Go daemon's JSON API contracts defined in:
 *   - daemon/session/session.go  (SessionInfo)
 *   - daemon/server/api.go       (REST request/response payloads)
 *   - daemon/server/ws.go        (WebSocket message envelopes)
 */

// ---------------------------------------------------------------------------
// REST API types
// ---------------------------------------------------------------------------

/** Matches the daemon's SessionInfo JSON response (session/session.go). */
export interface SessionInfo {
  id: string;
  title: string;
  status: string; // "running" | "exited"
  created_at: string; // ISO 8601 timestamp
  pid: number;
  exit_code: number;
}

/** Request body for POST /api/sessions (server/api.go createSessionRequest). */
export interface CreateSessionRequest {
  shell: string;
  title: string;
  cols: number;
  rows: number;
}

/** Response body from POST /api/sessions (server/api.go createSessionResponse). */
export interface CreateSessionResponse {
  id: string;
  ws_url: string;
}

/** Response body from GET /api/health (server/api.go healthResponse). */
export interface HealthResponse {
  status: string;
  version: string;
  uptime: number;
  sessions: number;
}

// ---------------------------------------------------------------------------
// WebSocket message types (server/ws.go)
// ---------------------------------------------------------------------------

/** Client -> Server: keystroke / paste data. */
export interface WsInputMessage {
  type: "input";
  data: string;
}

/** Client -> Server: terminal resize event. */
export interface WsResizeMessage {
  type: "resize";
  cols: number;
  rows: number;
}

/** Union of all messages the client can send to the daemon. */
export type WsClientMessage = WsInputMessage | WsResizeMessage;

/** Server -> Client: PTY output chunk. */
export interface WsOutputMessage {
  type: "output";
  data: string;
}

/** Server -> Client: process exit notification. */
export interface WsExitMessage {
  type: "exit";
  code: number;
}

/** Union of all messages the client can receive from the daemon. */
export type WsServerMessage = WsOutputMessage | WsExitMessage;

// ---------------------------------------------------------------------------
// Preload bridge (exposed via Electron contextBridge)
// ---------------------------------------------------------------------------

/** Shape of the object exposed on window.zplex by the preload script. */
export interface ZplexBridge {
  daemonPort: number;
}

/** Augment the global Window interface so window.zplex is strongly typed. */
declare global {
  interface Window {
    zplex: ZplexBridge;
  }
}

// ---------------------------------------------------------------------------
// Panel & Layout types
// ---------------------------------------------------------------------------

/** User preference for what happens when closing a panel. */
export type ClosePreference = "detach" | "kill";

/** A single panel in the CSS Grid layout. */
export interface PanelInfo {
  /** The daemon session ID. */
  sessionId: string;
  /** Display title shown in the header bar. */
  title: string;
  /** Which workspace this panel belongs to. */
  workspaceId: string;
  /** The panel's root DOM element. */
  element: HTMLElement;
  /** The header bar element. */
  headerElement: HTMLElement;
  /** The terminal content area element. */
  contentElement: HTMLElement;
}

/** Computed grid layout dimensions. */
export interface GridConfig {
  /** Number of columns. */
  columns: number;
  /** Number of rows (1 or 2). */
  rows: number;
  /** Number of panels in the top row. */
  topRowCount: number;
  /** Number of panels in the bottom row. */
  bottomRowCount: number;
}

/** Runtime state for a single workspace. */
export interface WorkspaceState {
  /** Maps session ID to PanelInfo. */
  panels: Map<string, PanelInfo>;
  /** Currently focused panel's session ID, or null if none. */
  focusedPanelId: string | null;
  /** Current column sizes in pixels (for drag resize). */
  columnSizes: number[];
  /** Current row sizes in pixels (for drag resize). */
  rowSizes: number[];
}

/** Return value from the close-panel confirmation dialog. */
export interface CloseDialogResult {
  /** Which action was chosen. */
  action: ClosePreference;
  /** Whether to save the preference for future closes. */
  remember: boolean;
}

// ---------------------------------------------------------------------------
// Layout persistence API types (daemon/server/api.go)
// ---------------------------------------------------------------------------

/** A single panel's position in the saved layout. */
export interface PanelLayout {
  /** The daemon session ID. */
  session_id: string;
  /** Zero-based position index in the grid. */
  position: number;
}

/** Layout state saved to/loaded from the daemon via GET/PUT /api/layout. */
export interface LayoutState {
  /** Ordered list of panels with their positions. */
  panels: PanelLayout[];
  /** CSS grid-template-columns value (e.g. "1fr 6px 1fr"). */
  grid_template_columns: string;
  /** CSS grid-template-rows value (e.g. "1fr"). */
  grid_template_rows: string;
}

// Ensure this file is treated as a module (required when using `declare global`).
export {};
