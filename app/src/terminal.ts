/**
 * Self-contained xterm.js terminal wrapper.
 *
 * Each TerminalWrapper owns one xterm.js Terminal instance and one WebSocket
 * connection to the zplex daemon.  Designed for use inside a
 * Map<string, TerminalWrapper> registry so multi-panel extension (M2) requires
 * no refactoring.
 */

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";
import type { WsClientMessage, WsServerMessage } from "./types";

/** Dark theme colours matching styles.css :root vars. */
const THEME = {
  background: "#1e1e1e",
  foreground: "#d4d4d4",
  cursor: "#d4d4d4",
} as const;

const FONT_FAMILY = "'Cascadia Mono NF', 'Cascadia Code', 'Consolas', monospace";
const FONT_SIZE = 16;

export class TerminalWrapper {
  private readonly sessionId: string;
  private readonly daemonPort: number;
  private readonly terminal: Terminal;
  private readonly fitAddon: FitAddon;
  private ws: WebSocket | null = null;
  private container: HTMLElement | null = null;
  private resizeObserver: ResizeObserver | null = null;
  private onFocusCallback: ((sessionId: string) => void) | null = null;
  private onDisposeCallback: ((sessionId: string) => void) | null = null;
  private onExitCallback: ((code: number) => void) | null = null;

  constructor(sessionId: string, daemonPort: number) {
    this.sessionId = sessionId;
    this.daemonPort = daemonPort;

    this.terminal = new Terminal({
      cursorBlink: true,
      theme: THEME,
      fontFamily: FONT_FAMILY,
      fontSize: FONT_SIZE,
    });

    this.fitAddon = new FitAddon();
    this.terminal.loadAddon(this.fitAddon);
  }

  // ---------------------------------------------------------------------------
  // Public API
  // ---------------------------------------------------------------------------

  /** Register a callback invoked when the terminal receives focus (click). */
  onFocus(callback: (sessionId: string) => void): void {
    this.onFocusCallback = callback;
  }

  /** Register a callback invoked when the terminal is disposed. */
  onDispose(callback: (sessionId: string) => void): void {
    this.onDisposeCallback = callback;
  }

  /** Register a callback invoked when the session's process exits (WS "exit"). */
  onExit(callback: (code: number) => void): void {
    this.onExitCallback = callback;
  }

  /** Trigger a re-fit of the terminal to its container dimensions. */
  fit(): void {
    this.fitAddon.fit();
  }

  /** Programmatically focus the xterm.js terminal input. */
  focusTerminal(): void {
    this.terminal.focus();
  }

  /** Return current terminal dimensions (cols, rows). */
  getDimensions(): { cols: number; rows: number } {
    return { cols: this.terminal.cols, rows: this.terminal.rows };
  }

  /** Mount the terminal into a DOM element and connect its WebSocket. */
  mount(container: HTMLElement): void {
    this.container = container;
    this.terminal.open(container);

    // Click-to-focus: notify layout when this terminal is clicked.
    container.addEventListener("mousedown", () => {
      if (this.onFocusCallback) {
        this.onFocusCallback(this.sessionId);
      }
    });

    this.loadWebGLAddon();

    // Let Ctrl+Shift shortcuts (Arrow, PageUp/PageDown) bubble to document
    // instead of being consumed by xterm.js.
    this.terminal.attachCustomKeyEventHandler((e: KeyboardEvent): boolean => {
      if (e.ctrlKey && e.shiftKey) {
        return false; // Don't handle — let it propagate to app-level handler
      }
      return true; // Let xterm.js handle normally
    });

    // Initial fit so cols/rows are correct before the first resize message.
    this.fitAddon.fit();

    // Forward keyboard input to the daemon.
    this.terminal.onData((data: string) => {
      this.send({ type: "input", data });
    });

    // Observe container size changes for automatic re-fit (AC-12).
    // ResizeObserver fires after layout settles, covering window resize,
    // CSS Grid reflow (panel add/remove), and gutter drag resize.
    this.resizeObserver = new ResizeObserver(() => {
      this.fitAddon.fit();
    });
    this.resizeObserver.observe(container);

    // After fit recalculates, forward the new size to the daemon.
    this.terminal.onResize(({ cols, rows }) => {
      this.send({ type: "resize", cols, rows });
    });

    this.connectWebSocket();
  }

  /** Tear down the terminal, close the WebSocket, and remove event listeners. */
  dispose(): void {
    if (this.resizeObserver) {
      this.resizeObserver.disconnect();
      this.resizeObserver = null;
    }
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.terminal.dispose();
    this.container = null;

    if (this.onDisposeCallback) {
      this.onDisposeCallback(this.sessionId);
    }
  }

  /** Return the session ID this wrapper is bound to. */
  getSessionId(): string {
    return this.sessionId;
  }

  // ---------------------------------------------------------------------------
  // Internals
  // ---------------------------------------------------------------------------

  /** Attempt to load the WebGL renderer; fall back to DOM on failure. */
  private loadWebGLAddon(): void {
    try {
      const webgl = new WebglAddon();
      webgl.onContextLoss(() => {
        console.warn("[terminal] WebGL context lost, disposing addon");
        webgl.dispose();
      });
      this.terminal.loadAddon(webgl);
    } catch (err: unknown) {
      console.warn("[terminal] WebGL not available, using DOM renderer", err);
    }
  }

  /** Open a WebSocket to the daemon and wire up message handlers. */
  private connectWebSocket(): void {
    const url = `ws://localhost:${this.daemonPort}/ws/${this.sessionId}`;
    this.ws = new WebSocket(url);

    this.ws.onopen = () => {
      console.warn("[terminal] WebSocket connected", this.sessionId);
      // Send current dimensions so the daemon sizes the PTY correctly.
      const { cols, rows } = this.terminal;
      this.send({ type: "resize", cols, rows });
    };

    this.ws.onmessage = (event: MessageEvent) => {
      const raw = String(event.data);
      let msg: WsServerMessage;
      try {
        msg = JSON.parse(raw) as WsServerMessage;
      } catch (err: unknown) {
        console.error("[terminal] failed to parse server message", err);
        return;
      }

      switch (msg.type) {
        case "output":
          // Ring buffer replay and live output both arrive as "output" messages.
          this.terminal.write(msg.data);
          break;
        case "exit":
          console.warn(
            "[terminal] session exited",
            this.sessionId,
            "code:",
            msg.code,
          );
          if (this.onExitCallback) {
            this.onExitCallback(msg.code);
          }
          break;
      }
    };

    this.ws.onerror = (event: Event) => {
      console.error("[terminal] WebSocket error", this.sessionId, event);
    };

    this.ws.onclose = () => {
      console.warn("[terminal] WebSocket closed", this.sessionId);
    };
  }

  /** Send a message to the daemon if the WebSocket is open. */
  private send(msg: WsClientMessage): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
    }
  }
}
