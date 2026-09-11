const {
  app,
  BrowserWindow,
  Tray,
  Menu,
  nativeImage,
  session,
  ipcMain,
} = require("electron");
const fs = require("node:fs"),
  path = require("node:path");
const { execFile } = require("node:child_process");
app.setAppUserModelId("io.github.eliotnash.netconductor");
ipcMain.handle("system-proxy", (event, action) => {
  if (
    event.senderFrame?.url !== origin + "/#desktop" ||
    !["enable", "restore", "status"].includes(action)
  )
    throw Error("Request denied");
  return new Promise((resolve, reject) =>
    execFile(
      "powershell.exe",
      [
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy",
        "Bypass",
        "-File",
        path.join(__dirname, "system-proxy.ps1"),
        "-Action",
        action,
      ],
      { windowsHide: true, timeout: 15000 },
      (e, out) => {
        if (e) {
          reject(Error("系统代理操作失败，请检查备份和当前用户权限"));
          return;
        }
        try {
          resolve(JSON.parse(out));
        } catch {
          reject(Error("Invalid system proxy response"));
        }
      },
    ),
  );
});
let window,
  tray,
  quitting = false;
const configPath =
  process.env.NC_AGENT_CONFIG ||
  path.join(
    process.env.ProgramData || "C:\\ProgramData",
    "NetConductor",
    "agent.json",
  );
let origin = "http://127.0.0.1:18765";
let reloadTimer;
let stopRemoteDesktop;
function readConfig() {
  const c = JSON.parse(fs.readFileSync(configPath, "utf8"));
  if (!/^127\.0\.0\.1:\d+$/.test(c.localListen))
    throw Error("Invalid local agent address");
  return c;
}
function show() {
  if (window) {
    window.show();
    window.focus();
    return;
  }
  window = new BrowserWindow({
    width: 1200,
    height: 820,
    minWidth: 760,
    minHeight: 600,
    title: "Net Conductor",
    icon: path.join(__dirname, "assets", "icon.ico"),
    backgroundColor: "#f5f7fb",
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, "preload.cjs"),
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
    },
  });
  window.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  window.webContents.on(
    "did-fail-load",
    (event, code, description, url, isMainFrame) => {
      if (!isMainFrame || code === -3 || quitting) return;
      clearTimeout(reloadTimer);
      reloadTimer = setTimeout(() => {
        if (window && !window.isDestroyed())
          window.loadURL(origin + "/#desktop").catch(() => {});
      }, 5000);
    },
  );
  window.webContents.on("did-finish-load", () => clearTimeout(reloadTimer));
  window.webContents.on("will-navigate", (e, url) => {
    if (new URL(url).origin !== origin) e.preventDefault();
  });
  window.on("close", (e) => {
    if (!quitting) {
      e.preventDefault();
      window.hide();
    }
  });
  window.on("closed", () => {
    clearTimeout(reloadTimer);
    window = null;
  });
  window.loadURL(origin + "/#desktop").catch(() => {});
}
if (!app.requestSingleInstanceLock()) app.quit();
else {
  app.on("second-instance", () => show());
  app.whenReady().then(() => {
    let c;
    try {
      c = readConfig();
      origin = "http://" + c.localListen;
    } catch (e) {
      require("electron").dialog.showErrorBox(
        "Net Conductor",
        "请先安装并启动后台服务。\n" + e.message,
      );
      app.quit();
      return;
    }
    session.defaultSession.setPermissionRequestHandler((wc, p, cb) =>
      cb(false),
    );
    session.defaultSession.webRequest.onBeforeSendHeaders(
      { urls: [origin + "/api/*"] },
      (details, cb) => {
        try {
          const c = readConfig();
          details.requestHeaders.Authorization = "Bearer " + c.localToken;
        } catch {}
        cb({ requestHeaders: details.requestHeaders });
      },
    );
    const image = nativeImage.createFromPath(
      path.join(__dirname, "assets", "icon.ico"),
    );
    tray = new Tray(image);
    tray.setToolTip("Net Conductor · 后台组网持续运行");
    tray.setContextMenu(
      Menu.buildFromTemplate([
        { label: "打开 Net Conductor", click: show },
        { type: "separator" },
        {
          label: "退出界面（后台服务继续运行）",
          click: () => {
            quitting = true;
            app.quit();
          },
        },
      ]),
    );
    tray.on("double-click", show);
    stopRemoteDesktop = require("./remote-desktop.cjs").start({
      readConfig,
      tray,
    });
    if (!process.argv.includes("--startup")) show();
  });
  app.on("before-quit", () => {
    quitting = true;
    stopRemoteDesktop?.();
  });
  app.on("window-all-closed", () => {});
}
