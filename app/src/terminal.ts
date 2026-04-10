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

const FONT_FAMILY = "'Cascadia Code', 'Consolas', monospace";
const FONT_SIZE = 14;

export class TerminalWrapper {
  private readonly sessionId: string;
  private readonly daemonPort: number;
  private readonly terminal: Terminal;
  private readonly fitAddon: FitAddon;
  private ws: WebSocket | null = null;
  private container: HTMLElement | null = null;
  private resizeHandler: (() => void) | null = null;

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

  /** Mount the terminal into a DOM element and connect its WebSocket. */
  mount(container: HTMLElement): void {
    this.container = container;
    this.terminal.open(container);

    this.loadWebGLAddon();

    // Initial fit so cols/rows are correct before the first resize message.
    this.fitAddon.fit();

    // Forward keyboard input to the daemon.
    this.terminal.onData((data: string) => {
      this.send({ type: "input", data });
    });

    // Recalculate dimensions when the browser window resizes.
    this.resizeHandler = () => {
      this.fitAddon.fit();
    };
    window.addEventListener("resize", this.resizeHandler);

    // After fit recalculates, forward the new size to the daemon.
    this.terminal.onResize(({ cols, rows }) => {
      this.send({ type: "resize", cols, rows });
    });

    this.connectWebSocket();
  }

  /** Tear down the terminal, close the WebSocket, and remove event listeners. */
  dispose(): void {
    if (this.resizeHandler) {
      window.removeEventListener("resize", this.resizeHandler);
      this.resizeHandler = null;
    }
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.terminal.dispose();
    this.container = null;
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
