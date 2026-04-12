/**
 * CSS Grid panel layout engine.
 *
 * PanelGrid manages an auto-tiled grid of terminal panels. Given N panels
 * (1-8), it computes a 1- or 2-row grid layout and places panels using
 * explicit CSS Grid column/row assignments. Gutter elements are inserted
 * between adjacent panels for drag-resize via pointer events.
 *
 * Grid structure example (5 panels = 3 top + 2 bottom):
 *   grid-template-columns: 1fr 4px 1fr 4px 1fr
 *   grid-template-rows:    1fr 4px 1fr
 *   Top row panels occupy grid columns 1, 3, 5 at grid row 1.
 *   Bottom row panels span proportionally across all 5 CSS columns at row 3.
 */

import type { PanelInfo, GridConfig, LayoutState, PanelLayout } from "./types";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const MAX_PANELS = 8;
const MIN_PANEL_WIDTH = 120;
const MIN_PANEL_HEIGHT = 80;
const GUTTER_SIZE_PX = 4;

// ---------------------------------------------------------------------------
// Auto-tile algorithm
// ---------------------------------------------------------------------------

/**
 * Compute grid dimensions for a given panel count.
 *
 * Layout rules:
 *   - 1-2 panels: single row
 *   - 3-8 panels: two rows; top row gets ceil(N/2), bottom gets remainder
 */
export function computeGridConfig(panelCount: number): GridConfig {
  const clamped = Math.max(0, Math.min(panelCount, MAX_PANELS));
  const rows = clamped <= 2 ? 1 : 2;
  const columns = clamped <= 2 ? clamped : Math.ceil(clamped / 2);
  const topRowCount = clamped <= 2 ? clamped : columns;
  const bottomRowCount = clamped <= 2 ? 0 : clamped - columns;

  return { columns, rows, topRowCount, bottomRowCount };
}

// ---------------------------------------------------------------------------
// PanelGrid class
// ---------------------------------------------------------------------------

export class PanelGrid {
  private readonly container: HTMLElement;

  /** Ordered array of session IDs — determines grid placement order. */
  private readonly panelOrder: string[] = [];

  /** Maps session ID to its PanelInfo. */
  private readonly panelMap: Map<string, PanelInfo> = new Map();

  /** Currently focused panel, or null. */
  private focusedPanelId: string | null = null;

  /** Current grid configuration (recomputed on every rebuildGrid call). */
  private currentConfig: GridConfig = { columns: 0, rows: 0, topRowCount: 0, bottomRowCount: 0 };

  /** Column sizes — initially empty; populated by rebuildGrid. */
  private columnSizes: number[] = [];

  /** Row sizes — initially empty; populated by rebuildGrid. */
  private rowSizes: number[] = [];

  /** Optional callback invoked after gutter drag resize or grid rebuild. */
  private onPanelResizeCallback: (() => void) | null = null;

  /** Optional callback invoked when layout should be saved (panel add/remove, gutter drag end). */
  private onLayoutChangeCallback: (() => void) | null = null;

  constructor(containerElement: HTMLElement) {
    this.container = containerElement;
  }

  // -------------------------------------------------------------------------
  // Public methods
  // -------------------------------------------------------------------------

  /**
   * Add a new panel to the grid.
   *
   * Returns the created PanelInfo, or null if the grid is at capacity.
   */
  addPanel(sessionId: string, title: string, workspaceId: string): PanelInfo | null {
    console.warn("[layout] addPanel entry:", sessionId);

    if (this.panelOrder.length >= MAX_PANELS) {
      console.warn("[layout] max panel count (8) reached, ignoring new panel request");
      return null;
    }

    // Build DOM structure: div.panel > (div.panel-header, div.panel-content)
    const element = document.createElement("div");
    element.classList.add("panel");
    element.dataset.sessionId = sessionId;

    const headerElement = document.createElement("div");
    headerElement.classList.add("panel-header");

    const titleSpan = document.createElement("span");
    titleSpan.classList.add("panel-title");
    titleSpan.textContent = title;

    const closeBtn = document.createElement("button");
    closeBtn.classList.add("panel-close-btn");
    closeBtn.textContent = "\u00d7"; // multiplication sign (x)

    headerElement.appendChild(titleSpan);
    headerElement.appendChild(closeBtn);

    const contentElement = document.createElement("div");
    contentElement.classList.add("panel-content");

    element.appendChild(headerElement);
    element.appendChild(contentElement);

    const panelInfo: PanelInfo = {
      sessionId,
      title,
      workspaceId,
      element,
      headerElement,
      contentElement,
    };

    this.panelOrder.push(sessionId);
    this.panelMap.set(sessionId, panelInfo);
    this.rebuildGrid();

    if (this.onLayoutChangeCallback) {
      this.onLayoutChangeCallback();
    }

    console.warn("[layout] addPanel exit:", sessionId);
    return panelInfo;
  }

  /** Remove a panel from the grid by session ID. */
  removePanel(sessionId: string): void {
    console.warn("[layout] removePanel entry:", sessionId);

    const panel = this.panelMap.get(sessionId);
    if (!panel) {
      console.warn("[layout] removePanel: session not found:", sessionId);
      return;
    }

    // Remove DOM element from container (if currently attached)
    if (panel.element.parentNode === this.container) {
      this.container.removeChild(panel.element);
    }

    this.panelMap.delete(sessionId);
    const idx = this.panelOrder.indexOf(sessionId);
    if (idx !== -1) {
      this.panelOrder.splice(idx, 1);
    }

    // Clear focus if the removed panel was focused
    if (this.focusedPanelId === sessionId) {
      this.focusedPanelId = null;
    }

    this.rebuildGrid();

    if (this.onLayoutChangeCallback) {
      this.onLayoutChangeCallback();
    }

    console.warn("[layout] removePanel exit:", sessionId);
  }

  /**
   * Remove all panels from the grid. Used during workspace switching to
   * clear the grid before recreating panels for the target workspace.
   * Does NOT fire onLayoutChange to avoid intermediate saves.
   */
  disposeAllPanels(): void {
    console.warn("[layout] disposeAllPanels entry, panels:", this.panelOrder.length);

    // Remove all children from container
    while (this.container.firstChild) {
      this.container.removeChild(this.container.firstChild);
    }

    // Clear internal state
    this.panelMap.clear();
    this.panelOrder.length = 0;
    this.focusedPanelId = null;
    this.columnSizes = [];
    this.rowSizes = [];
    this.currentConfig = { columns: 0, rows: 0, topRowCount: 0, bottomRowCount: 0 };

    // Reset container grid styles
    this.container.style.display = "";
    this.container.style.gridTemplateColumns = "";
    this.container.style.gridTemplateRows = "";

    console.warn("[layout] disposeAllPanels exit");
  }

  /** Set focus on a panel, removing focus from all others. */
  setFocus(sessionId: string): void {
    console.warn("[layout] setFocus:", sessionId);

    // Remove .focused from all panels
    for (const panel of this.panelMap.values()) {
      panel.element.classList.remove("focused");
    }

    const target = this.panelMap.get(sessionId);
    if (target) {
      target.element.classList.add("focused");
      this.focusedPanelId = sessionId;
    }
  }

  /** Return the currently focused panel's session ID, or null. */
  getFocusedPanelId(): string | null {
    return this.focusedPanelId;
  }

  /** Return the ordered array of panel session IDs. */
  getPanelIds(): readonly string[] {
    return this.panelOrder;
  }

  /** Look up a panel by session ID. */
  getPanelInfo(sessionId: string): PanelInfo | undefined {
    return this.panelMap.get(sessionId);
  }

  /** Return the current number of panels. */
  getPanelCount(): number {
    return this.panelOrder.length;
  }

  /** Return the titles of all current panels. */
  getExistingTitles(): string[] {
    return this.panelOrder.map(id => this.panelMap.get(id)?.title ?? "");
  }

  /**
   * Find the adjacent panel in a given direction from the specified panel.
   *
   * Grid position is derived from the panel's index in the ordered array:
   *   - Top row: indices 0..topRowCount-1
   *   - Bottom row: indices topRowCount..N-1
   *
   * Returns null if there is no adjacent panel in that direction.
   */
  getAdjacentPanelId(
    fromSessionId: string,
    direction: "up" | "down" | "left" | "right",
  ): string | null {
    const idx = this.panelOrder.indexOf(fromSessionId);
    if (idx === -1) {
      return null;
    }

    const config = this.currentConfig;
    const { topRowCount, bottomRowCount } = config;
    const isTopRow = idx < topRowCount;
    const rowIndex = isTopRow ? 0 : 1;
    const colInRow = isTopRow ? idx : idx - topRowCount;
    const rowLength = isTopRow ? topRowCount : bottomRowCount;

    switch (direction) {
      case "left": {
        if (colInRow <= 0) {
          return null;
        }
        const targetIdx = isTopRow ? colInRow - 1 : topRowCount + colInRow - 1;
        return this.panelOrder[targetIdx] ?? null;
      }
      case "right": {
        if (colInRow >= rowLength - 1) {
          return null;
        }
        const targetIdx = isTopRow ? colInRow + 1 : topRowCount + colInRow + 1;
        return this.panelOrder[targetIdx] ?? null;
      }
      case "up": {
        if (rowIndex === 0) {
          return null;
        }
        // Moving up from bottom row: find closest column in top row
        const targetCol = Math.min(
          Math.round((colInRow / bottomRowCount) * topRowCount),
          topRowCount - 1,
        );
        return this.panelOrder[targetCol] ?? null;
      }
      case "down": {
        if (rowIndex === 1 || bottomRowCount === 0) {
          return null;
        }
        // Moving down from top row: find closest column in bottom row
        const targetCol = Math.min(
          Math.round((colInRow / topRowCount) * bottomRowCount),
          bottomRowCount - 1,
        );
        return this.panelOrder[topRowCount + targetCol] ?? null;
      }
    }
  }

  /** Register a callback invoked after gutter drag resizes panels. */
  onPanelResize(callback: () => void): void {
    this.onPanelResizeCallback = callback;
  }

  /** Register a callback invoked when layout changes (panel add/remove, gutter drag end). */
  onLayoutChange(callback: () => void): void {
    this.onLayoutChangeCallback = callback;
  }

  /** Serialize the current layout state for persistence via PUT /api/layout. */
  serializeLayout(): LayoutState {
    const panels: PanelLayout[] = this.panelOrder.map((sessionId, index) => ({
      session_id: sessionId,
      position: index,
    }));

    return {
      panels,
      grid_template_columns: this.container.style.gridTemplateColumns,
      grid_template_rows: this.container.style.gridTemplateRows,
    };
  }

  /** Apply saved grid template values from a persisted layout state. */
  applyLayoutTemplate(gridTemplateColumns: string, gridTemplateRows: string): void {
    this.container.style.gridTemplateColumns = gridTemplateColumns;
    this.container.style.gridTemplateRows = gridTemplateRows;
  }

  /** Return the current grid configuration. */
  getGridConfig(): GridConfig {
    return { ...this.currentConfig };
  }

  /** Return current column sizes (fr or px values). */
  getColumnSizes(): number[] {
    return [...this.columnSizes];
  }

  /** Return current row sizes (fr or px values). */
  getRowSizes(): number[] {
    return [...this.rowSizes];
  }

  /** Set column sizes (used by gutter drag handler). */
  setColumnSizes(sizes: number[]): void {
    this.columnSizes = [...sizes];
  }

  /** Set row sizes (used by gutter drag handler). */
  setRowSizes(sizes: number[]): void {
    this.rowSizes = [...sizes];
  }

  /**
   * Apply current column/row sizes to the container's CSS grid properties.
   *
   * When sizes are set (from gutter drag), uses px values; otherwise falls
   * back to 1fr per panel column with 4px gutters.
   */
  applyGridSizes(): void {
    const config = this.currentConfig;
    if (config.columns === 0) {
      return;
    }

    // Build column template: interleave panel sizes with gutter sizes
    const colParts: string[] = [];
    for (let c = 0; c < config.columns; c++) {
      if (c > 0) {
        colParts.push(`${GUTTER_SIZE_PX}px`);
      }
      const size = this.columnSizes[c];
      // 0 means "not yet measured in px" — use 1fr for equal distribution
      colParts.push(size ? `${size}px` : "1fr");
    }
    this.container.style.gridTemplateColumns = colParts.join(" ");

    // Build row template
    const rowParts: string[] = [];
    for (let r = 0; r < config.rows; r++) {
      if (r > 0) {
        rowParts.push(`${GUTTER_SIZE_PX}px`);
      }
      const size = this.rowSizes[r];
      // 0 means "not yet measured in px" — use 1fr for equal distribution
      rowParts.push(size ? `${size}px` : "1fr");
    }
    this.container.style.gridTemplateRows = rowParts.join(" ");
  }

  // -------------------------------------------------------------------------
  // Private methods
  // -------------------------------------------------------------------------

  /**
   * Clear and rebuild the entire grid layout.
   *
   * Strategy:
   *   - Compute grid config from panel count.
   *   - Set grid-template-columns/rows on the container (panel cols
   *     interleaved with gutter cols).
   *   - Place each panel using explicit grid-column / grid-row.
   *   - Insert gutter divs between adjacent panels and rows.
   */
  private rebuildGrid(): void {
    const panelCount = this.panelOrder.length;
    const config = computeGridConfig(panelCount);
    this.currentConfig = config;

    console.warn("[layout] rebuildGrid: panels=", panelCount, "config=", config);

    // Clear the container
    while (this.container.firstChild) {
      this.container.removeChild(this.container.firstChild);
    }

    if (panelCount === 0) {
      this.container.style.display = "";
      this.container.style.gridTemplateColumns = "";
      this.container.style.gridTemplateRows = "";
      this.columnSizes = [];
      this.rowSizes = [];
      return;
    }

    // Set container to CSS Grid
    this.container.style.display = "grid";
    this.container.style.minWidth = `${MIN_PANEL_WIDTH}px`;
    this.container.style.minHeight = `${MIN_PANEL_HEIGHT}px`;

    // Total CSS columns: panel columns interleaved with gutter columns
    // e.g. 3 panel cols -> 1fr 4px 1fr 4px 1fr = 5 CSS columns
    const totalCSSCols = config.columns > 0 ? config.columns * 2 - 1 : 0;

    // Total CSS rows: panel rows interleaved with gutter rows
    // e.g. 2 panel rows -> 1fr 4px 1fr = 3 CSS rows
    const totalCSSRows = config.rows > 0 ? config.rows * 2 - 1 : 0;

    // Build grid-template-columns
    const colParts: string[] = [];
    for (let c = 0; c < config.columns; c++) {
      if (c > 0) {
        colParts.push(`${GUTTER_SIZE_PX}px`);
      }
      colParts.push("1fr");
    }
    this.container.style.gridTemplateColumns = colParts.join(" ");

    // Build grid-template-rows
    const rowParts: string[] = [];
    for (let r = 0; r < config.rows; r++) {
      if (r > 0) {
        rowParts.push(`${GUTTER_SIZE_PX}px`);
      }
      rowParts.push("1fr");
    }
    this.container.style.gridTemplateRows = rowParts.join(" ");

    // Initialize size tracking arrays (1fr = 0, meaning "not yet measured in px")
    this.columnSizes = new Array<number>(config.columns).fill(0);
    this.rowSizes = new Array<number>(config.rows).fill(0);

    // Place top row panels
    // Panel at logical column c occupies CSS grid column (c * 2 + 1) (1-based)
    // Top row is CSS grid row 1
    for (let c = 0; c < config.topRowCount; c++) {
      const sessionId = this.panelOrder[c];
      const panel = this.panelMap.get(sessionId);
      if (!panel) {
        continue;
      }
      const cssCol = c * 2 + 1; // 1-based
      panel.element.style.gridColumn = `${cssCol}`;
      panel.element.style.gridRow = "1";
      this.container.appendChild(panel.element);
    }

    // Insert column gutters between top-row panels
    for (let c = 0; c < config.topRowCount - 1; c++) {
      const gutter = document.createElement("div");
      gutter.classList.add("gutter-col");
      gutter.dataset.colIndex = String(c);
      const cssCol = c * 2 + 2; // gutter sits between panel cols
      gutter.style.gridColumn = `${cssCol}`;
      // Gutter spans all rows (including row gutter)
      gutter.style.gridRow = `1 / ${totalCSSRows + 1}`;
      this.container.appendChild(gutter);
    }

    // For 2-row layouts: insert row gutter and bottom row panels
    if (config.rows === 2) {
      // Row gutter spanning all CSS columns, at CSS row 2
      const rowGutter = document.createElement("div");
      rowGutter.classList.add("gutter-row");
      rowGutter.dataset.rowIndex = "0";
      rowGutter.style.gridColumn = `1 / ${totalCSSCols + 1}`;
      rowGutter.style.gridRow = "2";
      this.container.appendChild(rowGutter);

      // Place bottom row panels with proportional column spanning
      // Bottom row panels share the full totalCSSCols width evenly
      const bottomRowCSSRow = 3; // 1-based: row1=1, gutter=2, row2=3
      for (let b = 0; b < config.bottomRowCount; b++) {
        const sessionId = this.panelOrder[config.topRowCount + b];
        const panel = this.panelMap.get(sessionId);
        if (!panel) {
          continue;
        }

        // Proportional placement across totalCSSCols
        const startCol = Math.round(b * totalCSSCols / config.bottomRowCount) + 1; // 1-based
        const endCol = Math.round((b + 1) * totalCSSCols / config.bottomRowCount) + 1;
        panel.element.style.gridColumn = `${startCol} / ${endCol}`;
        panel.element.style.gridRow = `${bottomRowCSSRow}`;
        this.container.appendChild(panel.element);
      }
    }

    this.attachGutterListeners();

    if (this.onPanelResizeCallback) {
      this.onPanelResizeCallback();
    }
  }
  /**
   * Build a CSS grid-template-columns string from panel column widths.
   * Interleaves gutter widths (GUTTER_SIZE_PX) between panel columns.
   */
  private buildColumnTemplate(panelWidths: number[]): string {
    const parts: string[] = [];
    for (let i = 0; i < panelWidths.length; i++) {
      if (i > 0) {
        parts.push(`${GUTTER_SIZE_PX}px`);
      }
      parts.push(`${panelWidths[i]}px`);
    }
    return parts.join(" ");
  }

  /**
   * Build a CSS grid-template-rows string from panel row heights.
   * Interleaves gutter heights (GUTTER_SIZE_PX) between panel rows.
   */
  private buildRowTemplate(panelHeights: number[]): string {
    const parts: string[] = [];
    for (let i = 0; i < panelHeights.length; i++) {
      if (i > 0) {
        parts.push(`${GUTTER_SIZE_PX}px`);
      }
      parts.push(`${panelHeights[i]}px`);
    }
    return parts.join(" ");
  }

  /**
   * Extract panel-only sizes from a resolved grid template string.
   *
   * The template alternates: panelSize gutterSize panelSize gutterSize ...
   * So panel sizes are at even indices (0, 2, 4, ...).
   */
  private extractPanelSizes(templateString: string): number[] {
    const allSizes = templateString.split(" ").map(v => parseFloat(v));
    const panelSizes: number[] = [];
    for (let i = 0; i < allSizes.length; i += 2) {
      panelSizes.push(allSizes[i]);
    }
    return panelSizes;
  }

  /**
   * Attach pointer-event-based drag listeners to all gutter elements.
   * Called at the end of rebuildGrid so listeners are fresh after DOM rebuild.
   */
  private attachGutterListeners(): void {
    const colGutters = this.container.querySelectorAll<HTMLElement>(".gutter-col");
    const rowGutters = this.container.querySelectorAll<HTMLElement>(".gutter-row");

    for (const gutter of colGutters) {
      gutter.addEventListener("pointerdown", (e: PointerEvent) => {
        this.handleColumnGutterDragStart(gutter, e);
      });
    }

    for (const gutter of rowGutters) {
      gutter.addEventListener("pointerdown", (e: PointerEvent) => {
        this.handleRowGutterDragStart(gutter, e);
      });
    }
  }

  /**
   * Handle pointerdown on a column gutter — begin horizontal drag resize.
   *
   * The gutter at data-col-index N sits between panel column N and N+1.
   * Dragging adjusts both adjacent columns while respecting MIN_PANEL_WIDTH.
   */
  private handleColumnGutterDragStart(gutter: HTMLElement, startEvent: PointerEvent): void {
    console.warn("[layout] column gutter drag start, col-index:", gutter.dataset.colIndex);

    const colIndex = parseInt(gutter.dataset.colIndex ?? "0", 10);
    gutter.setPointerCapture(startEvent.pointerId);
    startEvent.preventDefault();

    // Read resolved column widths from computed style
    const computed = getComputedStyle(this.container);
    const initialPanelWidths = this.extractPanelSizes(computed.gridTemplateColumns);
    const startX = startEvent.clientX;

    // Visual feedback
    gutter.classList.add("dragging");
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";

    const leftIdx = colIndex;
    const rightIdx = colIndex + 1;
    const initialLeft = initialPanelWidths[leftIdx];
    const initialRight = initialPanelWidths[rightIdx];

    const onPointerMove = (e: PointerEvent): void => {
      e.preventDefault();
      const deltaX = e.clientX - startX;

      // Compute candidate sizes
      let newLeft = initialLeft + deltaX;
      let newRight = initialRight - deltaX;

      // Clamp to minimum width
      if (newLeft < MIN_PANEL_WIDTH) {
        newLeft = MIN_PANEL_WIDTH;
        newRight = initialLeft + initialRight - MIN_PANEL_WIDTH;
      }
      if (newRight < MIN_PANEL_WIDTH) {
        newRight = MIN_PANEL_WIDTH;
        newLeft = initialLeft + initialRight - MIN_PANEL_WIDTH;
      }

      // Update the sizes array
      const updatedWidths = [...initialPanelWidths];
      updatedWidths[leftIdx] = newLeft;
      updatedWidths[rightIdx] = newRight;

      // Apply to DOM
      this.container.style.gridTemplateColumns = this.buildColumnTemplate(updatedWidths);
      this.setColumnSizes(updatedWidths);
    };

    const onPointerUp = (e: PointerEvent): void => {
      console.warn("[layout] column gutter drag end, col-index:", gutter.dataset.colIndex);

      gutter.releasePointerCapture(e.pointerId);
      gutter.classList.remove("dragging");
      document.body.style.cursor = "";
      document.body.style.userSelect = "";

      document.removeEventListener("pointermove", onPointerMove);
      document.removeEventListener("pointerup", onPointerUp);

      if (this.onPanelResizeCallback) {
        this.onPanelResizeCallback();
      }

      if (this.onLayoutChangeCallback) {
        this.onLayoutChangeCallback();
      }
    };

    document.addEventListener("pointermove", onPointerMove);
    document.addEventListener("pointerup", onPointerUp);
  }

  /**
   * Handle pointerdown on a row gutter — begin vertical drag resize.
   *
   * The gutter at data-row-index N sits between panel row N and N+1.
   * Dragging adjusts both adjacent rows while respecting MIN_PANEL_HEIGHT.
   */
  private handleRowGutterDragStart(gutter: HTMLElement, startEvent: PointerEvent): void {
    console.warn("[layout] row gutter drag start, row-index:", gutter.dataset.rowIndex);

    const rowIndex = parseInt(gutter.dataset.rowIndex ?? "0", 10);
    gutter.setPointerCapture(startEvent.pointerId);
    startEvent.preventDefault();

    // Read resolved row heights from computed style
    const computed = getComputedStyle(this.container);
    const initialPanelHeights = this.extractPanelSizes(computed.gridTemplateRows);
    const startY = startEvent.clientY;

    // Visual feedback
    gutter.classList.add("dragging");
    document.body.style.cursor = "row-resize";
    document.body.style.userSelect = "none";

    const topIdx = rowIndex;
    const bottomIdx = rowIndex + 1;
    const initialTop = initialPanelHeights[topIdx];
    const initialBottom = initialPanelHeights[bottomIdx];

    const onPointerMove = (e: PointerEvent): void => {
      e.preventDefault();
      const deltaY = e.clientY - startY;

      // Compute candidate sizes
      let newTop = initialTop + deltaY;
      let newBottom = initialBottom - deltaY;

      // Clamp to minimum height
      if (newTop < MIN_PANEL_HEIGHT) {
        newTop = MIN_PANEL_HEIGHT;
        newBottom = initialTop + initialBottom - MIN_PANEL_HEIGHT;
      }
      if (newBottom < MIN_PANEL_HEIGHT) {
        newBottom = MIN_PANEL_HEIGHT;
        newTop = initialTop + initialBottom - MIN_PANEL_HEIGHT;
      }

      // Update the sizes array
      const updatedHeights = [...initialPanelHeights];
      updatedHeights[topIdx] = newTop;
      updatedHeights[bottomIdx] = newBottom;

      // Apply to DOM
      this.container.style.gridTemplateRows = this.buildRowTemplate(updatedHeights);
      this.setRowSizes(updatedHeights);
    };

    const onPointerUp = (e: PointerEvent): void => {
      console.warn("[layout] row gutter drag end, row-index:", gutter.dataset.rowIndex);

      gutter.releasePointerCapture(e.pointerId);
      gutter.classList.remove("dragging");
      document.body.style.cursor = "";
      document.body.style.userSelect = "";

      document.removeEventListener("pointermove", onPointerMove);
      document.removeEventListener("pointerup", onPointerUp);

      if (this.onPanelResizeCallback) {
        this.onPanelResizeCallback();
      }

      if (this.onLayoutChangeCallback) {
        this.onLayoutChangeCallback();
      }
    };

    document.addEventListener("pointermove", onPointerMove);
    document.addEventListener("pointerup", onPointerUp);
  }
}

export { MAX_PANELS, MIN_PANEL_WIDTH, MIN_PANEL_HEIGHT, GUTTER_SIZE_PX };
