const { contextBridge, ipcRenderer } = require("electron");
contextBridge.exposeInMainWorld("desktop", {
  systemProxy: (action) => ipcRenderer.invoke("system-proxy", action),
});
