import { contextBridge } from "electron";

const DAEMON_PORT = 17732;

contextBridge.exposeInMainWorld("zplex", {
  daemonPort: DAEMON_PORT,
});
