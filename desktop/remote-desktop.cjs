const { desktopCapturer, screen, powerMonitor } = require("electron");
const { spawn } = require("node:child_process");
const readline = require("node:readline");
const path = require("node:path");
exports.start = ({ readConfig, tray }) => {
  let stopped = false,
    locked = false,
    helper = null,
    pending = null,
    lastInput = 0,
    lastFrame = 0;
  powerMonitor.on("lock-screen", () => {
    locked = true;
    helper?.stdin.end();
    helper = null;
  });
  powerMonitor.on("unlock-screen", () => (locked = false));
  const input = (data) =>
    new Promise((resolve, reject) => {
      if (!helper) {
        helper = spawn(
          path.join(
            process.env.ProgramFiles,
            "NetConductor",
            "netconductor.exe",
          ),
          ["-mode", "desktop-input"],
          { windowsHide: true, stdio: ["pipe", "pipe", "ignore"] },
        );
        readline
          .createInterface({ input: helper.stdout })
          .on("line", (line) => {
            if (helper !== child) return;
            const p = pending;
            pending = null;
            if (p) {
              try {
                const r = JSON.parse(line);
                r.ok ? p.resolve(null) : p.reject(Error(r.error));
              } catch {
                p.reject(Error("Invalid input reply"));
              }
            }
          });
        const child = helper;
        child.stdin.on("error", () => {});
        helper.on("error", () => {
          if (helper !== child) return;
          pending?.reject(Error("Input helper unavailable"));
          pending = null;
          helper = null;
        });
        helper.on("exit", () => {
          if (helper !== child) return;
          pending?.reject(Error("Input helper stopped"));
          pending = null;
          helper = null;
        });
      }
      if (pending) {
        reject(Error("Input busy"));
        return;
      }
      const timer = setTimeout(() => {
        pending = null;
        helper?.kill();
        helper = null;
        reject(Error("Input timeout"));
      }, 2000);
      pending = {
        resolve: (v) => {
          clearTimeout(timer);
          resolve(v);
        },
        reject: (e) => {
          clearTimeout(timer);
          reject(e);
        },
      };
      helper.stdin.write(JSON.stringify(data) + "\n");
      lastInput = Date.now();
    });
  const timer = setInterval(() => {
    if (helper && !pending && Date.now() - lastInput > 3000) {
      input({ kind: "release" }).catch(() => {});
    }
    if (Date.now() - lastFrame > 4000)
      tray.setToolTip("Net Conductor · 后台组网持续运行");
  }, 2000);
  async function api(route, data) {
    const c = readConfig();
    const r = await fetch(
      "http://" + c.localListen + "/api/local/desktop/" + route,
      {
        method: "POST",
        headers: {
          Authorization: "Bearer " + c.localToken,
          "Content-Type": "application/json",
        },
        body: JSON.stringify(data),
        signal: AbortSignal.timeout(25000),
      },
    );
    if (!r.ok) throw Error("Desktop bridge unavailable");
    return r.json();
  }
  (async () => {
    while (!stopped) {
      try {
        const job = await api("next", {});
        if (!job.id) continue;
        let result = null,
          error = "";
        try {
          if (locked || powerMonitor.getSystemIdleState(1) === "locked")
            throw Error("Windows 已锁屏，请在本机解锁");
          if (job.request.op === "frame") {
            const sources = await desktopCapturer.getSources({
              types: ["screen"],
              thumbnailSize: { width: 1280, height: 720 },
            });
            const primary = screen.getPrimaryDisplay();
            const source = sources.find(
              (s) => s.display_id === String(primary.id),
            );
            if (!source || source.thumbnail.isEmpty())
              throw Error("无法捕获当前 Windows 桌面");
            result = {
              image: source.thumbnail.toJPEG(55).toString("base64"),
              width: source.thumbnail.getSize().width,
              height: source.thumbnail.getSize().height,
            };
            lastFrame = Date.now();
            tray.setToolTip("Net Conductor · 管理员正在查看远程桌面");
          } else if (job.request.op === "input") {
            await input(job.request.input);
            result = { ok: true };
          } else throw Error("Unknown desktop operation");
        } catch (e) {
          error = e.message;
        }
        await api("reply", { id: job.id, result, error });
      } catch {
        tray.setToolTip("Net Conductor · 桌面连接中断，正在重试");
        await new Promise((r) => setTimeout(r, 2000));
      }
    }
  })();
  return () => {
    stopped = true;
    clearInterval(timer);
    helper?.stdin.end();
  };
};
