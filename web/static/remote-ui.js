"use strict";
(() => {
  const escape = (s) =>
    String(s ?? "").replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  const decode = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
  const encode = (b) => {
    let s = "";
    for (const n of b) s += String.fromCharCode(n);
    return btoa(s);
  };
  class Link {
    constructor(id, mode, status, event) {
      this.seq = 0;
      this.pending = new Map();
      this.parts = new Map();
      this.status = status;
      this.event = event;
      this.closed = false;
      this.relay = false;
      this.pc = new RTCPeerConnection({
        iceServers: [{ urls: "stun:stun.cloudflare.com:3478" }],
      });
      this.dc = this.pc.createDataChannel("nc");
      this.ready = new Promise((resolve, reject) => {
        this.resolve = resolve;
        this.reject = reject;
      });
      this.dc.onmessage = (e) => this.receive(JSON.parse(e.data));
      this.dc.onopen = () => {
        if (!this.relay) {
          status("P2P 直连");
          this.resolve();
        }
      };
      this.pc.onconnectionstatechange = () => {
        if (["failed", "disconnected"].includes(this.pc.connectionState))
          this.useRelay();
      };
      this.ws = new WebSocket(
        `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/remote/${encodeURIComponent(id)}?mode=${mode}`,
      );
      this.ws.onmessage = async (e) => {
        try {
          const m = JSON.parse(e.data);
          if (m.type === "answer") {
            if (!this.closed) await this.pc.setRemoteDescription(m.data);
          } else if (m.type === "relay") {
            this.receive(m.data);
          } else if (m.type === "error") {
            throw Error(m.data);
          }
        } catch (err) {
          this.fail(err);
        }
      };
      this.ws.onclose = () =>
        this.fail(Error("连接已关闭，设备可能离线或客户端需要升级"));
      this.ws.onerror = () =>
        this.fail(Error("无法建立远程会话，请检查设备状态和登录权限"));
      this.ws.onopen = async () => {
        try {
          await this.pc.setLocalDescription(await this.pc.createOffer());
          await new Promise((resolve) => {
            if (this.pc.iceGatheringState === "complete") {
              resolve();
              return;
            }
            const timer = setTimeout(resolve, 3000);
            this.pc.addEventListener("icegatheringstatechange", () => {
              if (this.pc.iceGatheringState === "complete") {
                clearTimeout(timer);
                resolve();
              }
            });
          });
          if (this.closed) return;
          this.ws.send(
            JSON.stringify({
              type: "offer",
              data: { mode, sdp: this.pc.localDescription },
            }),
          );
          this.offered = true;
          this.fallback = setTimeout(() => {
            if (this.dc.readyState !== "open") this.useRelay();
          }, 12000);
        } catch (err) {
          this.fail(err);
        }
      };
      this.keepalive = setInterval(() => {
        if (this.ws.readyState === 1)
          this.ws.send(JSON.stringify({ type: "ping" }));
      }, 20000);
      this.stats = setInterval(async () => {
        if (this.relay || this.closed) return;
        try {
          const reports = await this.pc.getStats();
          reports.forEach((r) => {
            if (
              r.type === "candidate-pair" &&
              r.state === "succeeded" &&
              r.nominated
            ) {
              const local = reports.get(r.localCandidateId),
                remote = reports.get(r.remoteCandidateId);
              status(
                `P2P 直连 · ${local?.protocol || "UDP"} · ${Math.round((r.currentRoundTripTime || 0) * 1000)} ms`,
              );
            }
          });
        } catch {}
      }, 2000);
    }
    useRelay() {
      if (this.closed || !this.offered) return;
      this.relay = true;
      this.status("服务器中转 · HTTPS");
      this.resolve();
    }
    receive(m) {
      if (m.type === "chunk") {
        let part = this.parts.get(m.chunk);
        if (!part) {
          part = { buffers: [], size: 0 };
          this.parts.set(m.chunk, part);
        }
        const b = decode(m.data);
        part.size += b.length;
        if (part.size > 4 * 1024 * 1024 || this.parts.size > 4) {
          this.fail(Error("远程响应过大"));
          return;
        }
        part.buffers.push(b);
        if (m.last) {
          this.parts.delete(m.chunk);
          const all = new Uint8Array(part.size);
          let offset = 0;
          for (const b of part.buffers) {
            all.set(b, offset);
            offset += b.length;
          }
          this.receive(JSON.parse(new TextDecoder().decode(all)));
        }
        return;
      }
      if (m.type === "result") {
        const p = this.pending.get(m.id);
        if (p) {
          clearTimeout(p.timer);
          this.pending.delete(m.id);
          m.ok ? p.resolve(m.result) : p.reject(Error(m.error));
        }
      } else this.event(m);
    }
    async call(op, fields = {}) {
      await this.ready;
      if (this.closed) throw Error("会话已关闭");
      const id = ++this.seq;
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          this.pending.delete(id);
          reject(Error("操作超时"));
        }, 30000);
        this.pending.set(id, { resolve, reject, timer });
        const m = { id, op, ...fields };
        try {
          if (!this.relay && this.dc.readyState === "open")
            this.dc.send(JSON.stringify(m));
          else {
            this.useRelay();
            this.ws.send(JSON.stringify({ type: "relay", data: m }));
          }
        } catch (err) {
          clearTimeout(timer);
          this.pending.delete(id);
          reject(err);
        }
      });
    }
    fail(err) {
      if (this.closed) return;
      this.status(err.message);
      this.reject(err);
      this.close(err);
    }
    close(error = Error("会话已关闭")) {
      if (this.closed) return;
      this.closed = true;
      clearTimeout(this.fallback);
      clearInterval(this.keepalive);
      clearInterval(this.stats);
      for (const p of this.pending.values()) {
        clearTimeout(p.timer);
        p.reject(error);
      }
      this.pending.clear();
      this.parts.clear();
      this.ws.close();
      this.pc.close();
    }
  }
  window.openRemote = async (id, mode, name) => {
    const dlg = document.createElement("dialog");
    dlg.className = "remote-dialog";
    dlg.innerHTML = `<header><h2>${{ ssh: "SSH", files: "文件管理", desktop: "远程桌面" }[mode]} · ${escape(name)}</h2><button class="remote-close">关闭</button></header><div class="remote-status">正在尝试 P2P 直连…</div><div class="remote-error" role="alert"></div><div class="remote-body"></div>`;
    document.body.append(dlg);
    dlg.showModal();
    const body = dlg.querySelector(".remote-body"),
      status = dlg.querySelector(".remote-status"),
      error = dlg.querySelector(".remote-error");
    let terminal = null,
      observer = null,
      active = true;
    const report = (e) => {
      if (active) error.textContent = e.message;
    };
    const link = new Link(
      id,
      mode,
      (s) => (status.textContent = s),
      (m) => {
        if (m.type === "terminal") terminal?.write(decode(m.data));
        if (m.type === "terminalEnd") terminal?.writeln("\r\n[终端已结束]");
      },
    );
    dlg.querySelector(".remote-close").onclick = () => dlg.close();
    dlg.addEventListener(
      "close",
      () => {
        active = false;
        link.close();
        terminal?.dispose();
        observer?.disconnect();
        dlg.remove();
      },
      { once: true },
    );
    try {
      if (mode === "ssh") {
        body.innerHTML = '<div class="remote-terminal"></div>';
        terminal = new Terminal({
          cursorBlink: true,
          fontFamily: "Consolas, monospace",
          fontSize: 14,
          theme: { background: "#0c192b", foreground: "#d6e5f6" },
        });
        const fit = new FitAddon.FitAddon();
        terminal.loadAddon(fit);
        terminal.open(body.firstChild);
        fit.fit();
        await link.call("terminal");
        terminal.focus();
        terminal.onData((data) =>
          link
            .call("stdin", { data: encode(new TextEncoder().encode(data)) })
            .catch(report),
        );
        observer = new ResizeObserver(() => {
          if (!active) return;
          fit.fit();
          link
            .call("resize", { cols: terminal.cols, rows: terminal.rows })
            .catch(report);
        });
        observer.observe(body.firstChild);
      } else if (mode === "files") {
        body.innerHTML =
          '<div class="remote-tools"><button class="up">上级</button><input aria-label="远程目录" class="path"><button class="go">打开</button><button class="mkdir">新建目录</button><label class="upload-label">上传文件<input type="file" class="upload"></label></div><p class="muted">操作使用 SSH 用户权限；不覆盖同名文件，删除目录仅支持空目录。单文件传输上限 128 MB。</p><div class="file-table"></div>';
        const path = body.querySelector(".path"),
          table = body.querySelector(".file-table");
        let current = "",
          busy = false,
          loading = false;
        const join = (n) => current.replace(/\/$/, "") + "/" + n;
        const load = async (p = "") => {
          loading = true;
          body.querySelector(".upload").disabled = true;
          try {
            const result = await link.call("list", { path: p });
            current = result.path;
            path.value = current;
            table.dataset.path = current;
            const entries = result.entries.sort(
              (a, b) =>
                Number(b.directory) - Number(a.directory) ||
                a.name.localeCompare(b.name),
            );
            table.innerHTML = `<table><thead><tr><th>名称</th><th>大小</th><th>操作</th></tr></thead><tbody>${entries.map((e, i) => `<tr><td><button class="entry" data-index="${i}">${e.directory ? "📁" : "📄"} ${escape(e.name)}</button></td><td>${e.directory ? "—" : e.size.toLocaleString() + " B"}</td><td><button class="rename" data-index="${i}">重命名</button> <button class="delete" data-index="${i}">删除</button></td></tr>`).join("")}</tbody></table>`;
            table.onclick = async (event) => {
              const btn = event.target.closest("button");
              if (!btn || busy || loading) return;
              const e = entries[Number(btn.dataset.index)];
              try {
                if (btn.classList.contains("entry")) {
                  if (e.directory) await load(join(e.name));
                  else await download(e);
                } else if (btn.classList.contains("rename")) {
                  const n = prompt("新名称", e.name);
                  if (!n) return;
                  if (/[\\/]/.test(n) || n === "." || n === "..")
                    throw Error("请输入单个文件名");
                  await link.call("rename", {
                    path: join(e.name),
                    target: join(n),
                  });
                  await load(current);
                } else if (
                  btn.classList.contains("delete") &&
                  confirm(`永久删除 ${e.name}？`)
                ) {
                  await link.call("delete", { path: join(e.name) });
                  await load(current);
                }
              } catch (e) {
                report(e);
              }
            };
          } finally {
            loading = false;
            body.querySelector(".upload").disabled = false;
          }
        };
        const download = async (e) => {
          if (e.size > 128 * 1024 * 1024) throw Error("单文件上限 128 MB");
          busy = true;
          try {
            const parts = [];
            let offset = 0;
            while (active) {
              const part = await link.call("read", {
                path: join(e.name),
                offset,
              });
              const b = decode(part.data);
              parts.push(b);
              offset += b.length;
              if (offset > 128 * 1024 * 1024) throw Error("文件超过大小上限");
              error.textContent = `下载 ${e.name}：${offset.toLocaleString()} B`;
              if (part.eof) break;
            }
            if (!active) return;
            const url = URL.createObjectURL(new Blob(parts));
            const a = document.createElement("a");
            a.href = url;
            a.download = e.name;
            a.click();
            setTimeout(() => URL.revokeObjectURL(url), 30000);
            error.textContent = "下载完成";
          } finally {
            busy = false;
          }
        };
        body.querySelector(".go").onclick = () => {
          if (!busy && !loading) load(path.value).catch(report);
        };
        body.querySelector(".up").onclick = () => {
          if (!busy && !loading)
            load(current.replace(/\/[^/]+\/?$/, "") || "/").catch(report);
        };
        body.querySelector(".mkdir").onclick = async () => {
          if (busy || loading) return;
          const n = prompt("目录名称");
          if (!n) return;
          try {
            if (/[\\/]/.test(n) || n === "." || n === "..")
              throw Error("请输入单个目录名");
            await link.call("mkdir", { path: join(n) });
            await load(current);
          } catch (e) {
            report(e);
          }
        };
        body.querySelector(".upload").onchange = async (event) => {
          const f = event.target.files[0];
          if (!f || busy || loading) return;
          busy = true;
          const destination = join(f.name);
          try {
            if (f.size > 128 * 1024 * 1024) throw Error("单文件上限 128 MB");
            await link.call("create", { path: destination });
            for (let offset = 0; offset < f.size && active; offset += 24000) {
              const b = new Uint8Array(
                await f.slice(offset, offset + 24000).arrayBuffer(),
              );
              await link.call("write", {
                path: destination,
                offset,
                data: encode(b),
              });
              error.textContent = `上传 ${f.name}：${Math.min(offset + 24000, f.size).toLocaleString()} / ${f.size.toLocaleString()} B`;
            }
            if (active) {
              await load(current);
              error.textContent = "上传完成";
            }
          } catch (e) {
            report(
              Error(e.message + "；如传输中断，请检查并删除未完成文件后重试"),
            );
          } finally {
            busy = false;
            event.target.value = "";
          }
        };
        await load();
      } else {
        body.innerHTML =
          '<p class="muted">当前已登录用户的主屏幕。点击画面后可操作键鼠；不支持锁屏/UAC 安全桌面、声音或剪贴板同步。</p><img class="remote-screen" tabindex="0" alt="远程 Windows 桌面">';
        const img = body.querySelector("img");
        const send = (input) => link.call("input", { input }).catch(report);
        let move = 0;
        img.onpointermove = (e) => {
          if (Date.now() - move < 80) return;
          move = Date.now();
          const r = img.getBoundingClientRect();
          send({
            kind: "move",
            x: (e.clientX - r.left) / r.width,
            y: (e.clientY - r.top) / r.height,
          });
        };
        img.onpointerdown = (e) => {
          e.preventDefault();
          img.focus();
          try {
            img.setPointerCapture(e.pointerId);
          } catch {}
          send({ kind: "button", button: e.button, down: true });
        };
        img.onpointerup = (e) =>
          send({ kind: "button", button: e.button, down: false });
        img.oncontextmenu = (e) => e.preventDefault();
        img.onkeydown = (e) => {
          e.preventDefault();
          send({ kind: "key", code: e.code, down: true });
        };
        img.onkeyup = (e) => {
          e.preventDefault();
          send({ kind: "key", code: e.code, down: false });
        };
        img.onblur = () => send({ kind: "release" });
        img.addEventListener(
          "wheel",
          (e) => {
            e.preventDefault();
            send({ kind: "wheel", delta: e.deltaY > 0 ? -120 : 120 });
          },
          { passive: false },
        );
        await link.ready;
        while (active) {
          const result = await link.call("frame");
          if (!active) break;
          img.src = "data:image/jpeg;base64," + result.image;
          await new Promise((r) => setTimeout(r, 250));
        }
      }
    } catch (e) {
      report(e);
    }
  };
})();
