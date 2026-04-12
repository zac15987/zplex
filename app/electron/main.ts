import { app, BrowserWindow } from "electron";
import { spawn } from "child_process";
import * as path from "path";
import * as http from "http";

const DAEMON_PORT = 17732;
const HEALTH_URL = `http://localhost:${DAEMON_PORT}/api/health`;
const HEALTH_TIMEOUT_MS = 2000;
const HEALTH_MAX_RETRIES = 5;
const HEALTH_RETRY_INTERVAL_MS = 500;

/** Check if the daemon is running by hitting its health endpoint. */
function checkDaemonHealth(): Promise<boolean> {
  return new Promise((resolve) => {
    const req = http.get(HEALTH_URL, { timeout: HEALTH_TIMEOUT_MS }, (res) => {
      // Consume response data to free up memory
      res.resume();
      resolve(res.statusCode === 200);
    });
    req.on("error", () => resolve(false));
    req.on("timeout", () => {
      req.destroy();
      resolve(false);
    });
  });
}

/** Wait for daemon to become healthy, retrying up to HEALTH_MAX_RETRIES times. */
async function waitForDaemon(): Promise<boolean> {
  for (let attempt = 0; attempt < HEALTH_MAX_RETRIES; attempt++) {
    const healthy = await checkDaemonHealth();
    if (healthy) return true;
    if (attempt < HEALTH_MAX_RETRIES - 1) {
      await new Promise<void>((r) => setTimeout(r, HEALTH_RETRY_INTERVAL_MS));
    }
  }
  return false;
}

/**
 * Spawn the daemon as a detached process that survives Electron exit.
 * In dev mode the binary is resolved relative to the source tree;
 * in production it lives inside Electron's resource directory.
 */
function spawnDaemon(): void {
  const isDev = !app.isPackaged;
  const binaryPath = isDev
    ? path.resolve(__dirname, "../../daemon/zplex-daemon.exe")
    : path.join(process.resourcesPath, "zplex-daemon.exe");

  console.warn("[main] spawning daemon:", binaryPath);

  const child = spawn(binaryPath, [], {
    detached: true,
    stdio: "ignore",
  });
  child.unref();

  console.warn("[main] daemon spawned, pid:", child.pid);
}

/**
 * Ensure the daemon is running. If the health check fails, spawn a new
 * daemon process and wait for it to become healthy.
 */
async function ensureDaemon(): Promise<void> {
  console.warn("[main] checking daemon health...");

  const alreadyRunning = await checkDaemonHealth();
  if (alreadyRunning) {
    console.warn("[main] daemon already running, skipping spawn");
    return;
  }

  console.warn("[main] daemon not detected, spawning...");
  spawnDaemon();

  const healthy = await waitForDaemon();
  if (!healthy) {
    console.error("[main] daemon failed to start after retries");
  } else {
    console.warn("[main] daemon is now healthy");
  }
}

function createWindow(): BrowserWindow {
  const preloadPath = path.join(__dirname, "preload.js");

  const win = new BrowserWindow({
    width: 1200,
    height: 800,
    webPreferences: {
      preload: preloadPath,
      contextIsolation: true,
      nodeIntegration: false,
    },
    backgroundColor: "#1e1e1e",
    title: "zplex",
  });

  const isDev = !app.isPackaged;
  if (isDev) {
    win.loadURL("http://localhost:5173");
  } else {
    win.loadFile(path.join(__dirname, "../dist/index.html"));
  }

  return win;
}

app.whenReady().then(async () => {
  console.warn("[main] app ready, ensuring daemon...");
  await ensureDaemon();
  createWindow();
});

// macOS: recreate window when dock icon is clicked and no windows exist
app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    createWindow();
  }
});

// Quit when all windows are closed (except macOS where apps stay active)
app.on("window-all-closed", () => {
  if (process.platform !== "darwin") {
    app.quit();
  }
});
