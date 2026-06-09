/**
 * FixedPanel — persistent zpit cockpit panel.
 *
 * Manages the left-hand fixed panel that hosts the zpit TUI session.
 * Unlike dynamic grid panels, this panel is created once at init and
 * survives workspace switches and closures (AC-12).
 *
 * Layout: #main-split (flex row)
 *   ├─ #fixed-panel-area   (this.fixedArea)  width = ratio%
 *   ├─ #v-splitter         (this.splitter)   4px flex-none, draggable
 *   └─ #grid-container     (this.gridContainer)  flex: 1
 *
 * The splitter drag adjusts the fixed area percentage; ratio is persisted to
 * localStorage key "zplex:fixedPanelRatio" (AC-13).
 */

import { TerminalWrapper } from "./terminal";
import type { CreateSessionResponse } from "./types";
import { FIXED_PANEL_RATIO_KEY, DEFAULT_FIXED_PANEL_RATIO } from "./types";

const MIN_PANEL_PX = 200;
const SPLITTER_FALLBACK_PX = 4;

export class FixedPanel {
  private readonly daemonPort: number;

  // TerminalWrapper for the live zpit session; null when exited or unmounted.
  private wrapper: TerminalWrapper | null = null;

  // Panel DOM elements built by mount(); null before mount.
  private panelEl: HTMLElement | null = null;
  private headerEl: HTMLElement | null = null;
  private contentEl: HTMLElement | null = null;

  // Top-level DOM anchors resolved in the constructor.
  private readonly mainSplit: HTMLElement | null;
  private readonly fixedArea: HTMLElement | null;
  private readonly splitter: HTMLElement | null;
  private readonly gridContainer: HTMLElement | null;

  private active = false;
  private focusRequestCb: (() => void) | null = null;

  constructor(daemonPort: number) {
    this.daemonPort = daemonPort;

    this.mainSplit = document.getElementById("main-split");
    this.fixedArea = document.getElementById("fixed-panel-area");
    this.splitter = document.getElementById("v-splitter");
    this.gridContainer = document.getElementById("grid-container");

    if (!this.mainSplit || !this.fixedArea || !this.splitter || !this.gridContainer) {
      console.error("[fixedpanel] required DOM elements not found (#main-split, #fixed-panel-area, #v-splitter, #grid-container)");
    }
  }

  // ---------------------------------------------------------------------------
  // Public API (T13 contract)
  // ---------------------------------------------------------------------------

  /** Whether a fixed session is currently mounted (cockpit active). */
  isActive(): boolean {
    return this.active;
  }

  /**
   * Activate the cockpit for the given fixed session. Removes 'no-fixed' from
   * #main-split, builds the header (label "zpit", no close button) + content,
   * restores the persisted width ratio, and wires the #v-splitter drag.
   * If status === "exited", shows the exited overlay instead of a live terminal.
   * Call this exactly once at app init.
   */
  mount(sessionId: string, status: string): void {
    if (!this.mainSplit || !this.fixedArea || !this.splitter || !this.gridContainer) {
      console.error("[fixedpanel] mount: required DOM elements missing, aborting");
      return;
    }

    // Reveal the fixed area by removing the hiding class.
    this.mainSplit.classList.remove("no-fixed");

    // Build panel DOM inside #fixed-panel-area.
    const panelEl = document.createElement("div");
    panelEl.classList.add("panel", "fixed-panel");

    const headerEl = document.createElement("div");
    headerEl.classList.add("panel-header");

    const titleSpan = document.createElement("span");
    titleSpan.classList.add("panel-title");
    titleSpan.textContent = "zpit";
    // No close button (AC-9).
    headerEl.appendChild(titleSpan);

    const contentEl = document.createElement("div");
    contentEl.classList.add("panel-content");

    panelEl.appendChild(headerEl);
    panelEl.appendChild(contentEl);
    this.fixedArea.appendChild(panelEl);

    this.panelEl = panelEl;
    this.headerEl = headerEl;
    this.contentEl = contentEl;

    // Click on panel → grant focus visual + notify app.ts.
    panelEl.addEventListener("mousedown", () => {
      this.setFocusVisual(true);
      this.focusRequestCb?.();
    });

    // Restore persisted width ratio (AC-13).
    const stored = localStorage.getItem(FIXED_PANEL_RATIO_KEY);
    let ratio = stored !== null ? parseFloat(stored) : DEFAULT_FIXED_PANEL_RATIO;
    if (isNaN(ratio)) {
      ratio = DEFAULT_FIXED_PANEL_RATIO;
    }
    this.applyRatio(ratio);

    // Wire splitter drag (AC-13).
    this.wireSplitterDrag();

    // Show live terminal or exited overlay.
    if (status === "exited") {
      this.showExited();
    } else {
      this.mountTerminal(sessionId);
    }

    this.active = true;
  }

  /** Focus the zpit terminal (used by cross-area Ctrl+Shift navigation). */
  focusTerminal(): void {
    this.wrapper?.focusTerminal();
  }

  /** Add/remove the focused visual style on the fixed panel. */
  setFocusVisual(focused: boolean): void {
    if (focused) {
      this.panelEl?.classList.add("focused");
    } else {
      this.panelEl?.classList.remove("focused");
    }
  }

  /** Register a callback fired when the fixed panel is clicked. */
  onFocusRequest(callback: () => void): void {
    this.focusRequestCb = callback;
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  /**
   * Apply the fixed-area width ratio.
   * Ratio is clamped to [0.05, 0.95] to keep both panes visible.
   */
  private applyRatio(ratio: number): void {
    if (!this.fixedArea) return;
    const clamped = Math.max(0.05, Math.min(0.95, ratio));
    this.fixedArea.style.width = `${clamped * 100}%`;
  }

  /**
   * Create and mount a TerminalWrapper for the given session into contentEl.
   * Wires onFocus and onExit callbacks.
   */
  private mountTerminal(sessionId: string): void {
    if (!this.contentEl) return;

    // Clear any overlay from a previous exited state.
    while (this.contentEl.firstChild) {
      this.contentEl.removeChild(this.contentEl.firstChild);
    }

    const wrapper = new TerminalWrapper(sessionId, this.daemonPort);
    this.wrapper = wrapper;

    wrapper.onFocus(() => {
      this.setFocusVisual(true);
      this.focusRequestCb?.();
      wrapper.focusTerminal();
    });

    wrapper.onExit(() => {
      this.showExited();
    });

    wrapper.mount(this.contentEl);
  }

  /**
   * Replace the terminal area with an exited overlay (AC-15).
   * Disposes any live wrapper and shows a "zpit exited" message + Restart button.
   */
  private showExited(): void {
    this.wrapper?.dispose();
    this.wrapper = null;

    if (!this.contentEl) return;

    // Clear existing children (terminal or previous overlay).
    while (this.contentEl.firstChild) {
      this.contentEl.removeChild(this.contentEl.firstChild);
    }

    const overlay = document.createElement("div");
    overlay.classList.add("fixed-exited-overlay");

    const msg = document.createElement("span");
    msg.textContent = "zpit exited";

    const restartBtn = document.createElement("button");
    restartBtn.textContent = "Restart";
    restartBtn.addEventListener("click", () => {
      this.restart().catch((err: unknown) => {
        console.error("[fixedpanel] restart error:", err);
      });
    });

    overlay.appendChild(msg);
    overlay.appendChild(restartBtn);
    this.contentEl.appendChild(overlay);
  }

  /**
   * POST /api/zpit/restart and remount a new TerminalWrapper on 201 (AC-15).
   */
  private async restart(): Promise<void> {
    const resp = await fetch(`http://localhost:${this.daemonPort}/api/zpit/restart`, {
      method: "POST",
    });

    if (resp.status !== 201) {
      console.error("[fixedpanel] restart failed:", resp.status);
      return;
    }

    const data = await resp.json() as CreateSessionResponse;

    // Dispose any existing wrapper (safety guard) before remounting.
    this.wrapper?.dispose();
    this.wrapper = null;

    // mountTerminal creates a fresh wrapper; TerminalWrapper.mount() performs
    // the initial fit and installs a ResizeObserver, so no explicit fit here.
    this.mountTerminal(data.id);
  }

  /**
   * Wire pointer-event drag on #v-splitter to resize fixed/dynamic ratio (AC-13).
   * Uses document-level pointermove/pointerup listeners, matching layout.ts pattern.
   */
  private wireSplitterDrag(): void {
    if (!this.splitter) return;

    this.splitter.addEventListener("pointerdown", (startEvent: PointerEvent) => {
      if (!this.mainSplit || !this.fixedArea || !this.splitter) return;

      this.splitter.setPointerCapture(startEvent.pointerId);
      startEvent.preventDefault();

      // Visual feedback during drag.
      this.splitter.classList.add("dragging");

      // Track the latest ratio so pointerup can persist it. `moved` stays false
      // until an actual drag occurs, so a bare click on the splitter does not
      // overwrite the stored ratio with the default (AC-13).
      let currentRatio = DEFAULT_FIXED_PANEL_RATIO;
      let moved = false;

      const onPointerMove = (e: PointerEvent): void => {
        if (!this.mainSplit || !this.fixedArea || !this.splitter) return;

        const rect = this.mainSplit.getBoundingClientRect();
        const total = rect.width;
        const splitterW = this.splitter.offsetWidth || SPLITTER_FALLBACK_PX;

        // Guard: container too small to honour both minimum widths.
        if (total - splitterW - 2 * MIN_PANEL_PX <= 0) return;

        let fixedPx = e.clientX - rect.left;
        fixedPx = Math.max(MIN_PANEL_PX, Math.min(fixedPx, total - splitterW - MIN_PANEL_PX));

        currentRatio = fixedPx / total;
        moved = true;
        this.fixedArea.style.width = `${currentRatio * 100}%`;
      };

      const onPointerUp = (e: PointerEvent): void => {
        if (!this.splitter) return;

        this.splitter.releasePointerCapture(e.pointerId);
        this.splitter.classList.remove("dragging");

        document.removeEventListener("pointermove", onPointerMove);
        document.removeEventListener("pointerup", onPointerUp);

        // Only persist + refit when the splitter was actually dragged. A bare
        // click (pointerdown→pointerup with no move) must leave the stored
        // ratio untouched.
        if (!moved) return;
        localStorage.setItem(FIXED_PANEL_RATIO_KEY, String(currentRatio));
        this.wrapper?.fit();
      };

      document.addEventListener("pointermove", onPointerMove);
      document.addEventListener("pointerup", onPointerUp);
    });
  }
}
