/**
 * WorkspaceManager -- manages workspace tabs and their lifecycle.
 *
 * Responsibilities:
 *   - Render and maintain the tab bar UI
 *   - Track workspace list and active workspace
 *   - Fire callbacks on switch/create/close/rename events
 *   - Persist active workspace ID to localStorage
 *
 * Does NOT manage terminals or panel grid directly -- delegates to callbacks.
 */

import type { WorkspaceInfo } from "./types";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const MAX_WORKSPACES = 8;
const ACTIVE_WORKSPACE_KEY = "zplex:activeWorkspace";

// ---------------------------------------------------------------------------
// Callback types
// ---------------------------------------------------------------------------

/** Fired when the user switches to a different workspace. */
export type OnSwitchCallback = (fromId: string, toId: string) => void;

/** Fired when a new workspace is created. */
export type OnCreateCallback = (workspace: WorkspaceInfo) => void;

/** Fired when a workspace close is requested. Returns a promise that resolves when close is complete. */
export type OnCloseCallback = (workspaceId: string) => Promise<void>;

/** Fired when a workspace is renamed. */
export type OnRenameCallback = (workspaceId: string, newName: string) => void;

// ---------------------------------------------------------------------------
// WorkspaceManager
// ---------------------------------------------------------------------------

export class WorkspaceManager {
  private readonly tabsContainer: HTMLElement;
  private readonly addButton: HTMLElement;

  /** Ordered list of workspaces. */
  private readonly workspaces: WorkspaceInfo[] = [];

  /** Currently active workspace ID. */
  private activeId: string = "";

  /** Counter for auto-naming. */
  private nextNumber: number = 1;

  /** Callbacks. */
  private onSwitchCallback: OnSwitchCallback | null = null;
  private onCreateCallback: OnCreateCallback | null = null;
  private onCloseCallback: OnCloseCallback | null = null;
  private onRenameCallback: OnRenameCallback | null = null;

  constructor() {
    const tabsContainer = document.getElementById("tab-bar-tabs");
    if (!tabsContainer) {
      throw new Error("tab-bar-tabs element not found in DOM");
    }
    this.tabsContainer = tabsContainer;

    const addButton = document.getElementById("tab-bar-add");
    if (!addButton) {
      throw new Error("tab-bar-add element not found in DOM");
    }
    this.addButton = addButton;

    // Wire up "+" button
    this.addButton.addEventListener("click", () => {
      this.createWorkspace();
    });
  }

  // -------------------------------------------------------------------------
  // Callback registration
  // -------------------------------------------------------------------------

  onSwitch(callback: OnSwitchCallback): void {
    this.onSwitchCallback = callback;
  }

  onCreate(callback: OnCreateCallback): void {
    this.onCreateCallback = callback;
  }

  onClose(callback: OnCloseCallback): void {
    this.onCloseCallback = callback;
  }

  onRename(callback: OnRenameCallback): void {
    this.onRenameCallback = callback;
  }

  // -------------------------------------------------------------------------
  // Public API
  // -------------------------------------------------------------------------

  /** Get the active workspace ID. */
  getActiveId(): string {
    return this.activeId;
  }

  /** Get the number of workspaces. */
  getCount(): number {
    return this.workspaces.length;
  }

  /** Get all workspace IDs (read-only snapshot). */
  getWorkspaceIds(): readonly string[] {
    return this.workspaces.map((w) => w.id);
  }

  /** Get workspace info by ID. */
  getWorkspace(id: string): WorkspaceInfo | undefined {
    return this.workspaces.find((w) => w.id === id);
  }

  /**
   * Initialize workspaces from a list of known workspace IDs (from daemon).
   * If the list is empty, creates a default "Workspace 1".
   * Restores active workspace from localStorage if available.
   */
  initFromList(workspaceIds: string[]): void {
    console.warn("[workspace] initFromList entry:", workspaceIds);

    if (workspaceIds.length === 0) {
      // No existing workspaces -- create default
      this.addWorkspaceInternal("workspace-1", "Workspace 1");
      this.nextNumber = 2;
    } else {
      // Populate from daemon data
      let maxNum = 0;
      for (const id of workspaceIds) {
        // Try to extract number from ID for auto-naming continuity
        const match = id.match(/^workspace-(\d+)$/);
        const num = match ? parseInt(match[1], 10) : 0;
        if (num > maxNum) {
          maxNum = num;
        }
        const name = match ? `Workspace ${num}` : id;
        this.addWorkspaceInternal(id, name);
      }
      this.nextNumber = maxNum + 1;
    }

    // Determine which workspace to activate
    const savedActiveId = localStorage.getItem(ACTIVE_WORKSPACE_KEY);
    const targetId =
      savedActiveId && this.workspaces.some((w) => w.id === savedActiveId)
        ? savedActiveId
        : this.workspaces[0].id;

    this.setActive(targetId);
    this.renderTabs();

    console.warn("[workspace] initFromList exit, active:", this.activeId);
  }

  /**
   * Create a new workspace with auto-generated name.
   * Returns the new workspace info, or null if at max capacity.
   */
  createWorkspace(): WorkspaceInfo | null {
    console.warn("[workspace] createWorkspace entry");

    if (this.workspaces.length >= MAX_WORKSPACES) {
      console.warn(
        "[workspace] max workspace count (8) reached, ignoring new workspace request",
      );
      return null;
    }

    const num = this.nextNumber;
    this.nextNumber++;
    const id = `workspace-${num}`;
    const name = `Workspace ${num}`;

    const workspace = this.addWorkspaceInternal(id, name);

    if (this.onCreateCallback) {
      this.onCreateCallback(workspace);
    }

    // Switch to the new workspace
    this.switchTo(id);

    console.warn("[workspace] createWorkspace exit:", id);
    return workspace;
  }

  /**
   * Switch to a workspace by ID. Fires the onSwitch callback.
   */
  switchTo(targetId: string): void {
    console.warn("[workspace] switchTo entry:", targetId);

    if (targetId === this.activeId) {
      console.warn("[workspace] switchTo: already active, skipping");
      return;
    }

    const target = this.workspaces.find((w) => w.id === targetId);
    if (!target) {
      console.error("[workspace] switchTo: workspace not found:", targetId);
      return;
    }

    const fromId = this.activeId;
    this.setActive(targetId);
    this.renderTabs();

    if (this.onSwitchCallback) {
      this.onSwitchCallback(fromId, targetId);
    }

    console.warn("[workspace] switchTo exit:", targetId);
  }

  /**
   * Switch to the next workspace (wraps around).
   */
  switchNext(): void {
    if (this.workspaces.length <= 1) return;
    const currentIdx = this.workspaces.findIndex(
      (w) => w.id === this.activeId,
    );
    const nextIdx = (currentIdx + 1) % this.workspaces.length;
    this.switchTo(this.workspaces[nextIdx].id);
  }

  /**
   * Switch to the previous workspace (wraps around).
   */
  switchPrevious(): void {
    if (this.workspaces.length <= 1) return;
    const currentIdx = this.workspaces.findIndex(
      (w) => w.id === this.activeId,
    );
    const prevIdx =
      (currentIdx - 1 + this.workspaces.length) % this.workspaces.length;
    this.switchTo(this.workspaces[prevIdx].id);
  }

  /**
   * Request to close a workspace. Fires onClose callback for session cleanup.
   */
  async closeWorkspace(workspaceId: string): Promise<void> {
    console.warn("[workspace] closeWorkspace entry:", workspaceId);

    // Last workspace cannot be closed
    if (this.workspaces.length <= 1) {
      console.warn("[workspace] closeWorkspace: cannot close last workspace");
      return;
    }

    const idx = this.workspaces.findIndex((w) => w.id === workspaceId);
    if (idx === -1) {
      console.error(
        "[workspace] closeWorkspace: workspace not found:",
        workspaceId,
      );
      return;
    }

    // Fire close callback (app.ts handles session cleanup + dialog)
    if (this.onCloseCallback) {
      await this.onCloseCallback(workspaceId);
    }

    // Remove workspace from list
    this.workspaces.splice(idx, 1);

    // If we closed the active workspace, switch to an adjacent one
    if (this.activeId === workspaceId) {
      const newIdx = Math.min(idx, this.workspaces.length - 1);
      this.setActive(this.workspaces[newIdx].id);
    }

    this.renderTabs();

    console.warn("[workspace] closeWorkspace exit:", workspaceId);
  }

  /**
   * Remove a workspace from the internal list without firing callbacks.
   * Used when the close callback has already handled cleanup externally.
   */
  removeWorkspaceInternal(workspaceId: string): void {
    const idx = this.workspaces.findIndex((w) => w.id === workspaceId);
    if (idx === -1) return;
    this.workspaces.splice(idx, 1);

    if (this.activeId === workspaceId && this.workspaces.length > 0) {
      const newIdx = Math.min(idx, this.workspaces.length - 1);
      this.setActive(this.workspaces[newIdx].id);
    }

    this.renderTabs();
  }

  // -------------------------------------------------------------------------
  // Private methods
  // -------------------------------------------------------------------------

  /** Add a workspace to the internal list (no callbacks, no render). */
  private addWorkspaceInternal(id: string, name: string): WorkspaceInfo {
    const workspace: WorkspaceInfo = { id, name };
    this.workspaces.push(workspace);
    return workspace;
  }

  /** Set the active workspace and persist to localStorage. */
  private setActive(id: string): void {
    this.activeId = id;
    localStorage.setItem(ACTIVE_WORKSPACE_KEY, id);
  }

  /** Render (or re-render) all tabs in the tab bar. */
  private renderTabs(): void {
    // Clear existing tabs
    while (this.tabsContainer.firstChild) {
      this.tabsContainer.removeChild(this.tabsContainer.firstChild);
    }

    const isLastWorkspace = this.workspaces.length <= 1;

    for (const workspace of this.workspaces) {
      const tab = document.createElement("div");
      tab.classList.add("workspace-tab");
      tab.dataset.workspaceId = workspace.id;

      if (workspace.id === this.activeId) {
        tab.classList.add("active");
      }

      // Name span
      const nameSpan = document.createElement("span");
      nameSpan.classList.add("workspace-tab-name");
      nameSpan.textContent = workspace.name;

      // Close button
      const closeBtn = document.createElement("button");
      closeBtn.classList.add("workspace-tab-close");
      if (isLastWorkspace) {
        closeBtn.classList.add("hidden");
      }
      closeBtn.textContent = "\u00d7"; // multiplication sign as close icon
      closeBtn.title = "Close workspace";

      tab.appendChild(nameSpan);
      tab.appendChild(closeBtn);

      // Click tab to switch
      tab.addEventListener("click", (e: Event) => {
        // Don't switch if clicking the close button
        if (
          (e.target as HTMLElement).classList.contains("workspace-tab-close")
        ) {
          return;
        }
        this.switchTo(workspace.id);
      });

      // Double-click name to rename
      nameSpan.addEventListener("dblclick", (e: Event) => {
        e.stopPropagation();
        this.startRename(workspace.id, nameSpan);
      });

      // Close button click
      closeBtn.addEventListener("click", (e: Event) => {
        e.stopPropagation();
        this.closeWorkspace(workspace.id).catch((err: unknown) => {
          console.error("[workspace] closeWorkspace failed:", err);
        });
      });

      this.tabsContainer.appendChild(tab);
    }
  }

  /** Enter inline rename mode for a workspace tab. */
  private startRename(workspaceId: string, nameSpan: HTMLElement): void {
    console.warn("[workspace] startRename:", workspaceId);

    const workspace = this.workspaces.find((w) => w.id === workspaceId);
    if (!workspace) return;

    const input = document.createElement("input");
    input.classList.add("workspace-tab-input");
    input.type = "text";
    input.value = workspace.name;

    // Replace name span with input
    const parent = nameSpan.parentElement;
    if (!parent) return;
    parent.replaceChild(input, nameSpan);
    input.focus();
    input.select();

    // Guard against finishRename being called twice (Enter fires blur as well)
    let finished = false;

    const finishRename = (accept: boolean): void => {
      if (finished) return;
      finished = true;

      const newName = input.value.trim();
      if (accept && newName.length > 0 && newName !== workspace.name) {
        workspace.name = newName;
        if (this.onRenameCallback) {
          this.onRenameCallback(workspaceId, newName);
        }
      }
      // Restore name span with current (possibly updated) name
      nameSpan.textContent = workspace.name;
      parent.replaceChild(nameSpan, input);
    };

    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        finishRename(true);
      } else if (e.key === "Escape") {
        e.preventDefault();
        finishRename(false);
      }
    });

    input.addEventListener("blur", () => {
      // Accept on blur (like VS Code behavior)
      finishRename(true);
    });
  }
}

export { MAX_WORKSPACES, ACTIVE_WORKSPACE_KEY };
