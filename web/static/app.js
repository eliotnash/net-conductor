"use strict";
const $ = (s) => document.querySelector(s),
  esc = (s) =>
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
const icons = {
  home: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
  devices:
    '<rect x="3" y="3" width="18" height="13" rx="2"/><path d="M8 21h8m-4-5v5"/>',
  proxy: '<path d="M3 7h17m-4-4 4 4-4 4M21 17H4m4-4-4 4 4 4"/>',
  maps: '<path d="M8 3H3v5m0-5 7 7m6 11h5v-5m0 5-7-7"/><rect x="8" y="8" width="8" height="8" rx="2"/>',
  logs: '<path d="M8 3H4v18h16V3h-4M9 2h6v4H9zM8 11h8M8 15h6"/>',
  repair:
    '<path d="M14 5a6 6 0 0 0-7 8L2 18l4 4 5-5a6 6 0 0 0 8-7l-4 4-5-5z"/>',
  cloud: '<path d="M6 18a5 5 0 0 1 0-10 7 7 0 0 1 13 2 4 4 0 0 1 0 8z"/>',
  ssh: '<path d="m5 7 5 5-5 5m8 0h6"/>',
  shield: '<path d="m12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6zM8 12l3 3 5-6"/>',
};
const ico = (n) =>
  `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[n] || icons.devices}</svg>`;
let state = null,
  tab = "home",
  agent = location.hash === "#desktop",
  authenticated = false,
  term = null,
  socket = null,
  refreshing = false;
const names = {
  home: agent ? "连接概览" : "网络概览",
  devices: "组网设备",
  proxy: "代理策略",
  maps: "端口映射",
  logs: "事件日志",
  repair: "诊断与修复",
};
async function api(path, method = "GET", data) {
  const r = await fetch(path, {
    method,
    headers: data ? { "Content-Type": "application/json" } : {},
    body: data ? JSON.stringify(data) : undefined,
  });
  const v = await r.json();
  if (!r.ok) {
    if (r.status === 401 && !agent) {
      authenticated = false;
      login();
    }
    throw Error(v.error || `请求失败 ${r.status}`);
  }
  return v;
}
function toast(s) {
  $("#toast").textContent = s;
  $("#toast").style.display = "block";
  clearTimeout(toast.t);
  toast.t = setTimeout(() => ($("#toast").style.display = "none"), 4500);
}
function badge(on, text) {
  return `<span class="tag ${on ? "" : "off"}"><i class="dot"></i>${esc(text || (on ? "在线" : "离线"))}</span>`;
}
const bytes = (n) =>
  n > 1073741824
    ? (n / 1073741824).toFixed(2) + " GB"
    : n > 1048576
      ? (n / 1048576).toFixed(1) + " MB"
      : Math.round((n || 0) / 1024) + " KB";
const date = (s) =>
  !s || s.startsWith("0001")
    ? "尚无记录"
    : new Date(s).toLocaleString("zh-CN", { hour12: false });
const device = (id) => state?.devices?.find((d) => d.id === id),
  deviceName = (id) =>
    id === "DIRECT" ? "直接连接" : device(id)?.name || id || "暂无";
function shell() {
  const nav = agent
    ? ["home", "repair", "logs"]
    : ["home", "devices", "proxy", "maps", "logs"];
  $("#app").innerHTML =
    `<div class="shell"><aside class="side"><div class="brand"><span class="logo">N</span>Net Conductor</div><small>${agent ? "WINDOWS CLIENT" : "NETWORK CONTROL"}</small><nav>${nav.map((n) => `<button data-tab="${n}" class="${tab === n ? "active" : ""}">${ico(n)}<span>${names[n]}</span></button>`).join("")}</nav><footer><strong>${agent ? "Windows 节点客户端" : "自托管网络管理"}</strong><br>v0.2.0 · WireGuard 私网${!agent ? '<br><button data-action="logout">退出登录</button>' : ""}</footer></aside><main class="main"><header class="top"><div><h1 id="title">${names[tab]}</h1><p>${agent ? "你的设备、连接与网络出口" : "一台公网服务器，连接你的所有设备"}</p></div><span class="live" id="live">正在连接…</span></header><div id="content"></div></main></div>`;
}
function panel(title, body, action = "", subtitle = "") {
  return `<section class="panel"><div class="panel-head"><div><h2>${title}</h2>${subtitle ? `<p>${subtitle}</p>` : ""}</div>${action}</div>${body}</section>`;
}
function stat(title, value, sub, icon) {
  return `<div class="stat"><label>${title}</label><strong>${value}</strong><span>${sub}</span><div class="ico">${ico(icon)}</div></div>`;
}
function events() {
  return state.events?.length
    ? [...state.events]
        .reverse()
        .slice(0, tab === "logs" ? 100 : 5)
        .map(
          (e) =>
            `<div class="event"><time>${date(e.at)}</time><div><strong>${esc(e.action)}</strong><small>${esc(e.detail)}</small></div></div>`,
        )
        .join("")
    : '<div class="empty">暂无事件记录</div>';
}
function render() {
  if (!state) return;
  if (!$("#content")) shell();
  $("#title").textContent = names[tab];
  document
    .querySelectorAll("[data-tab]")
    .forEach((x) => x.classList.toggle("active", x.dataset.tab === tab));
  $("#live").innerHTML =
    `<i class="dot"></i>${agent ? (state.status === "connected" ? "服务器已连接" : "服务器未连接") : "实时同步 · " + new Date().toLocaleTimeString("zh-CN", { hour12: false })}`;
  $("#live").style.color =
    agent && state.status !== "connected" ? "#bd7a11" : "";
  if (agent) {
    renderAgent();
    return;
  }
  const online = state.devices.filter((d) => d.status === "online").length;
  if (tab === "home") {
    $("#content").innerHTML =
      `<div class="stats">${stat("在线设备", `${online}<span> / ${state.devices.length}</span>`, "包括公网服务器", "devices")}${stat("可用代理出口", state.devices.filter((d) => d.proxyHealthy).length, "真实出口请求检测", "proxy")}${stat("端口映射", state.mappings.filter((m) => m.enabled && !m.error).length, "正在监听的转发规则", "maps")}${stat("代理策略", state.policies.length, "按来源设备独立配置", "shield")}</div>${panel(
        "私网拓扑",
        `<div class="topology"><div class="node">${ico("cloud")}<div><strong>公网服务器</strong><small>${esc(state.serverIP)} · 中心节点</small></div></div><div class="stem"></div><div class="peers">${
          state.devices
            .filter((d) => d.id !== "cloud")
            .map(
              (d) =>
                `<div class="node">${ico("devices")}<div><strong>${esc(d.name)}</strong><small>${esc(d.ip)} · ${d.status === "online" ? "在线" : "离线"}</small></div></div>`,
            )
            .join("") || '<span class="muted">添加第一台设备，开始组网</span>'
        }</div></div>`,
        '<button class="primary" data-action="enroll">＋ 添加设备</button>',
      )}<div class="split">${panel("设备连接", deviceTable(true))}${panel("最近事件", events())}</div>`;
  }
  if (tab === "devices")
    $("#content").innerHTML = panel(
      "全部设备",
      deviceTable(),
      '<button class="primary" data-action="enroll">＋ 添加设备</button>',
      "在线状态来自客户端心跳；隧道流量来自服务器 WireGuard",
    );
  if (tab === "proxy")
    $("#content").innerHTML =
      `<div class="notice">按顺序尝试出口，新连接在故障时切换。客户端应用使用本地 HTTP 代理 127.0.0.1:17891；未配置策略时阻止代理出站。</div>${panel(
        "设备出口策略",
        `<div class="table-wrap"><table><thead><tr><th>来源设备</th><th>出口优先级</th><th>最近成功出口</th><th>全部失败时</th><th></th></tr></thead><tbody>${state.devices
          .map((d) => {
            const p = state.policies.find((p) => p.source === d.id);
            return `<tr><td><strong>${esc(d.name)}</strong><small>${esc(d.ip)}</small></td><td><div class="ordering">${p?.exits?.map((id) => `<span class="tag">${esc(deviceName(id))}</span>`).join('<span class="arrow">→</span>') || '<span class="muted">未配置</span>'}</div></td><td>${esc(deviceName(p?.active))}</td><td>${p?.onFailure === "direct" ? "直接连接" : "停止代理"}</td><td><button data-action="policy" data-id="${d.id}">配置</button></td></tr>`;
          })
          .join("")}</tbody></table></div>`,
      )}`;
  if (tab === "maps")
    $("#content").innerHTML =
      `<div class="notice">映射由公网服务器转发至设备私网端口。公网访问还需云安全组放行对应端口；HTTP 域名需解析到服务器。</div>${panel("公网端口映射", state.mappings.length ? `<div class="table-wrap"><table><thead><tr><th>名称 / 协议</th><th>公网入口</th><th>目标设备</th><th>状态</th><th></th></tr></thead><tbody>${state.mappings.map((m) => `<tr><td><strong>${esc(m.name)}</strong><small>${m.protocol.toUpperCase()}</small></td><td class="mono">${esc(m.domain || "*")}:${m.listenPort}</td><td>${esc(deviceName(m.device))}<small>${esc(device(m.device)?.ip)}:${m.targetPort}</small></td><td>${badge(m.enabled && !m.error, m.error ? "启动失败" : m.enabled ? "监听中" : "未启用")}${m.error ? `<small>${esc(m.error)}</small>` : ""}</td><td><button class="danger" data-action="delete-map" data-id="${m.id}">删除</button></td></tr>`).join("")}</tbody></table></div>` : '<div class="empty">尚无映射。添加设备端口，让服务从公网可访问。</div>', '<button class="primary" data-action="mapping">＋ 添加映射</button>')}`;
  if (tab === "logs")
    $("#content").innerHTML = panel("操作与连接事件", events());
}
function deviceTable(compact = false) {
  return `<div class="table-wrap"><table><thead><tr><th>设备</th><th>状态</th>${compact ? "" : "<th>往返延迟</th><th>隧道流量 ↓ / ↑</th><th>代理出口</th>"}<th>操作</th></tr></thead><tbody>${state.devices.map((d) => `<tr><td><strong>${esc(d.name)}</strong><small class="mono">${esc(d.ip)} · ${esc(d.os)}</small></td><td>${badge(d.status === "online")}</td>${compact ? "" : `<td>${d.id === "cloud" ? "—" : d.status === "online" ? d.latencyMs + " ms" : "—"}<small>${d.agentVersion ? "v" + esc(d.agentVersion) : "无客户端心跳"}</small></td><td class="mono">${bytes(d.tx)} / ${bytes(d.rx)}<small>${d.handshake ? "握手 " + date(new Date(d.handshake * 1000).toISOString()) : "暂无握手"}</small></td><td>${d.proxyPort ? badge(d.proxyHealthy, d.proxyHealthy ? "可用" : "不可用") : "未配置"}<small>${d.proxyPort ? esc(d.proxyType) + ":" + d.proxyPort : ""}</small></td>`}<td><div class="toolbar"><button data-action="ssh" data-id="${d.id}" ${d.sshUser && d.sshFingerprint ? "" : "disabled"}>SSH</button><button data-action="files" data-id="${d.id}" ${canRemote(d) ? "" : 'disabled title="请升级客户端到 v0.2.0"'}>文件</button>${d.os === "windows" ? `<button data-action="desktop" data-id="${d.id}" ${canRemote(d) ? "" : 'disabled title="请升级客户端到 v0.2.0"'}>桌面</button>` : ""}${compact ? "" : `<button data-action="device" data-id="${d.id}">设置</button>`}</div></td></tr>`).join("")}</tbody></table></div>`;
}
function renderAgent() {
  const connected = state.status === "connected";
  if (tab === "home")
    $("#content").innerHTML =
      `${!state.enrolled ? '<div class="notice">尚未加入网络。填写服务器地址、入网码和服务器证书即可开始。</div>' : ""}${state.lastError ? `<div class="notice error">${esc(state.lastError)}</div>` : ""}<section class="panel"><div class="status-banner"><div><h2>${state.tunnelReady ? "私网已连接" : connected ? "私网待恢复" : "等待连接"}</h2><p>${esc(state.name || "Windows 设备")} · ${state.enrolled ? "后台服务会自动重试连接" : "完成入网后自动连接"}</p></div>${ico("shield")}</div><div class="panel-body detail-grid">${[
        ["虚拟 IP", state.ip || "尚未分配"],
        ["服务器", state.server || "尚未配置"],
        ["隧道接口", state.interface],
        ["本地 HTTP 代理", state.proxyListen],
        ["心跳往返延迟", connected ? state.latencyMs + " ms" : "—"],
        ["最近同步", date(state.lastSync)],
        ["隧道接收 / 发送", bytes(state.rx) + " / " + bytes(state.tx)],
        ["最近成功出口", state.exitName || "暂无"],
        [
          "共享代理端口",
          (state.shares || [])
            .map((p) => p.listenPort + " → 127.0.0.1:" + p.localPort)
            .join(", ") || "未配置",
        ],
      ]
        .map(
          ([k, v]) =>
            `<div><label>${k}</label><strong class="mono">${esc(v)}</strong></div>`,
        )
        .join(
          "",
        )}</div><div class="panel-head"><span class="muted">窗口关闭后，后台服务继续运行</span><div class="toolbar">${!state.enrolled ? '<button class="primary" data-action="join">加入网络</button>' : '<button data-action="system-proxy">系统代理</button><button data-action="share">共享本机代理</button><button class="primary" data-action="diagnose">检查连接</button>'}</div></div></section>${panel("连接检查", checks())}`;
  if (tab === "repair")
    $("#content").innerHTML =
      `<div class="notice">诊断检查真实端口和隧道状态。一键修复会先备份本客户端配置，再重启当前 WireGuard 隧道，连接会短暂中断。</div>${panel("网络诊断", checks(), '<div class="toolbar"><button data-action="diagnose">重新检测</button><button class="primary" data-action="repair">一键修复</button></div>')}${panel("修复与连接记录", events())}`;
  if (tab === "logs") $("#content").innerHTML = panel("客户端事件", events());
}
function checks() {
  return (
    state.checks
      ?.map(
        (c) =>
          `<div class="check"><div>${esc(c.name)}<small>${esc(c.detail)}</small></div>${badge(c.ok, c.ok ? "正常" : "需检查")}</div>`,
      )
      .join("") || '<div class="empty">正在获取检查结果…</div>'
  );
}
function login() {
  if (agent) return;
  $("#app").innerHTML =
    `<div class="auth"><section class="auth-card"><div class="logo">N</div><h1>Net Conductor</h1><p>登录你的私网控制中心</p><form id="login-form"><label class="field">管理密码<input type="password" name="password" autocomplete="current-password" required></label><p class="form-error" id="login-error"></p><button class="primary">登录</button></form></section></div>`;
  $("#login-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("/api/login", "POST", {
        password: new FormData(e.target).get("password"),
      });
      authenticated = true;
      await refresh();
    } catch (e) {
      $("#login-error").textContent = e.message;
    }
  };
}
function field(label, name, value = "", type = "text", extra = "") {
  return `<label class="field">${label}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
}
function select(label, name, options, value) {
  return `<label class="field">${label}<select name="${name}">${options.map(([v, t]) => `<option value="${esc(v)}" ${v === value ? "selected" : ""}>${esc(t)}</option>`).join("")}</select></label>`;
}
function modal(title, body, onSubmit) {
  const dlg = $("#dialog");
  dlg.className = "";
  dlg.innerHTML = `<header><h2>${title}</h2><button data-action="close-dialog" type="button">✕</button></header><form>${body}<p class="form-error"></p><div class="actions"><button type="button" data-action="close-dialog">取消</button><button class="primary" type="submit">保存</button></div></form>`;
  dlg.showModal();
  dlg.querySelector("form").onsubmit = async (e) => {
    e.preventDefault();
    const btn = e.target.querySelector("[type=submit]");
    btn.disabled = true;
    try {
      await onSubmit(Object.fromEntries(new FormData(e.target)));
      dlg.close();
      await refresh();
      toast("已保存");
    } catch (error) {
      dlg.querySelector(".form-error").textContent = error.message;
    } finally {
      btn.disabled = false;
    }
  };
}
function closeDialog() {
  socket?.close();
  socket = null;
  term?.dispose();
  term = null;
  $("#dialog").close();
}
async function action(name, id) {
  if (name === "logout") {
    await api("/api/logout", "POST", {});
    authenticated = false;
    login();
  }
  if (name === "close-dialog") closeDialog();
  if (name === "enroll")
    modal(
      "添加 Windows 设备",
      field("接入现有设备时填写虚拟 IP；新设备留空", "ip") +
        '<p class="help">入网码单次有效，10 分钟过期。请在客户端填写服务器 HTTPS 地址，并导入服务器证书。</p>',
      async (v) => {
        const x = await api("/api/enrollment", "POST", v);
        setTimeout(() => {
          const d = $("#dialog");
          d.innerHTML =
            '<header><h2>入网码已生成</h2><button data-action="close-dialog">✕</button></header><div class="dialog-body"><p class="help">复制到 Windows 客户端，10 分钟内有效。</p><textarea readonly>' +
            esc(x.code) +
            "</textarea></div>";
          d.showModal();
        }, 30);
      },
    );
  if (name === "device") {
    const d = device(id);
    modal(
      "设备设置",
      field("设备名称", "name", d.name, "text", 'required maxlength="80"') +
        `<div class="field-row">${select(
          "共享代理协议",
          "proxyType",
          [
            ["socks5", "SOCKS5"],
            ["http", "HTTP"],
          ],
          d.proxyType || "socks5",
        )}${field("共享代理端口，0 表示关闭", "proxyPort", d.proxyPort, "number", 'min="0" max="65535" required')}</div><div class="field-row">${field("SSH 用户", "sshUser", d.sshUser)}${field("SSH 端口", "sshPort", d.sshPort || 22, "number", 'min="1" max="65535" required')}</div>` +
        field(
          "SSH 主机指纹（在目标机核对）",
          "sshFingerprint",
          d.sshFingerprint,
        ) +
        '<p class="help">SSH 使用服务器上配置的专用密钥。主机指纹不匹配时拒绝连接。</p>',
      (v) =>
        api("/api/devices/" + id, "PUT", {
          ...v,
          proxyPort: +v.proxyPort,
          sshPort: +v.sshPort,
        }),
    );
  }
  if (name === "policy") {
    const p = state.policies.find((x) => x.source === id) || {
      exits: [],
      onFailure: "block",
    };
    const opts = [
      ["", "不设置"],
      ...state.devices.filter((d) => d.proxyPort).map((d) => [d.id, d.name]),
    ];
    modal(
      "配置 " + esc(deviceName(id)) + " 的出口",
      Array.from({ length: Math.max(3, p.exits.length + 1) }, (_, i) =>
        select("优先级 " + (i + 1), "exit" + i, opts, p.exits[i] || ""),
      ).join("") +
        select(
          "所有出口失败时",
          "onFailure",
          [
            ["block", "停止代理"],
            ["direct", "直接连接"],
          ],
          p.onFailure,
        ),
      (v) =>
        api("/api/policies/" + id, "PUT", {
          source: id,
          exits: Object.entries(v)
            .filter(([k, x]) => k.startsWith("exit") && x)
            .map(([, x]) => x),
          onFailure: v.onFailure,
          active: "",
        }),
    );
  }
  if (name === "mapping")
    modal(
      "添加端口映射",
      field("映射名称", "name", "", "text", 'required maxlength="80"') +
        `<div class="field-row">${select(
          "协议",
          "protocol",
          [
            ["tcp", "TCP"],
            ["udp", "UDP"],
            ["http", "HTTP"],
          ],
          "tcp",
        )}${field("公网监听端口", "listenPort", "", "number", 'required min="1024" max="65535"')}</div>` +
        select(
          "目标设备",
          "device",
          state.devices.map((d) => [d.id, d.name]),
          "",
        ) +
        field(
          "目标服务端口",
          "targetPort",
          "",
          "number",
          'required min="1" max="65535"',
        ) +
        field("HTTP 域名（可选）", "domain") +
        '<p class="help">HTTPS 可由现有 Caddy 转到此 HTTP 映射端口；本版本不自动修改已有 Caddy 配置。</p>',
      (v) =>
        api("/api/mappings", "POST", {
          ...v,
          listenPort: +v.listenPort,
          targetPort: +v.targetPort,
          enabled: true,
          id: "",
          error: "",
        }),
    );
  if (name === "delete-map") {
    const m = state.mappings.find((x) => x.id === id);
    modal(
      "删除映射",
      "<p>停止并删除 " + esc(m.name) + " 的公网转发。</p>",
      () => api("/api/mappings/" + id, "DELETE"),
    );
  }
  if (name === "join")
    modal(
      "加入私网",
      field(
        "服务器 HTTPS 地址",
        "server",
        "",
        "url",
        'required placeholder="https://服务器:18443"',
      ) +
        field("设备名称", "name", state.name, "text", "required") +
        field("入网码", "code", "", "password", "required") +
        '<label class="field">服务器证书 PEM（使用受信任证书时可留空）<textarea name="certificate" placeholder="-----BEGIN CERTIFICATE-----"></textarea></label>' +
        field(
          "WireGuard 隧道名称",
          "interface",
          state.interface,
          "text",
          'required pattern="[A-Za-z0-9_-]+"',
        ) +
        select(
          "组网方式",
          "adopt",
          [
            ["false", "创建新隧道"],
            ["true", "接入已有隧道"],
          ],
          "false",
        ),
      (v) =>
        api("/api/local/join", "POST", { ...v, adopt: v.adopt === "true" }),
    );
  if (name === "diagnose") {
    await api("/api/local/diagnose", "POST", {});
    await refresh();
    toast("检查完成");
  }
  if (name === "repair") {
    await api("/api/local/repair", "POST", {});
    await refresh();
    toast("隧道已重新连接");
  }
  if (name === "share")
    modal(
      "共享本机代理",
      field(
        "本机 HTTP / SOCKS5 代理端口",
        "port",
        "",
        "number",
        'required min="1" max="65535"',
      ) +
        field(
          "私网共享端口",
          "listenPort",
          "",
          "number",
          'required min="1024" max="65535"',
        ) +
        '<p class="help">请先在艾可云/Clash 中核对实际监听端口与协议，不要填写 Net Conductor 本机入口。仅允许公网服务器访问。完成后在网页设备设置中填写对应协议和私网共享端口，并将服务器出口策略选择为本机。代理需自行启动，停止时共享会等待，重启后自动恢复。</p>',
      (v) =>
        api("/api/local/share", "POST", {
          port: +v.port,
          listenPort: +v.listenPort,
        }),
    );
  if (name === "system-proxy") {
    if (!window.desktop) {
      toast("请在桌面客户端操作");
      return;
    }
    const x = await window.desktop.systemProxy("status");
    modal(
      "当前用户的系统代理",
      select(
        "操作",
        "action",
        [
          ["enable", "启用 Net Conductor 代理"],
          ["restore", "恢复启用前的代理设置"],
        ],
        x.enabled ? "restore" : "enable",
      ) +
        "<p class=help>仅影响遵循 Windows 系统代理的应用。启用前保存原配置，恢复时还原原值。</p>",
      (v) => window.desktop.systemProxy(v.action),
    );
  }
  if (name === "ssh" && !canRemote(device(id))) openSSH(id);
  else if (["ssh", "files", "desktop"].includes(name))
    window.openRemote(id, name, deviceName(id));
}
function openSSH(id) {
  closeDialog();
  const dlg = $("#dialog");
  dlg.className = "terminal-dialog";
  dlg.innerHTML = `<header><h2>SSH · ${esc(deviceName(id))}</h2><button data-action="close-dialog">关闭终端</button></header><div id="terminal"></div>`;
  dlg.showModal();
  term = new Terminal({
    cursorBlink: true,
    fontFamily: "Consolas, monospace",
    fontSize: 14,
    theme: { background: "#0c192b", foreground: "#d6e5f6" },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("#terminal"));
  fit.fit();
  term.writeln("正在校验主机身份并连接…");
  socket = new WebSocket(
    `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/ssh/${encodeURIComponent(id)}`,
  );
  socket.binaryType = "arraybuffer";
  const ws = socket;
  ws.onopen = () => {
    term.clear();
    ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
    term.focus();
  };
  ws.onmessage = (e) =>
    term?.write(typeof e.data === "string" ? e.data : new Uint8Array(e.data));
  ws.onclose = () => term?.writeln("\r\n[连接已关闭]");
  ws.onerror = () =>
    term?.writeln(
      "\r\n[连接失败：请核对 SSH 用户、授权密钥、主机指纹和目标端口]",
    );
  term.onData((x) => {
    if (ws.readyState === 1) ws.send(new TextEncoder().encode(x));
  });
  const ro = new ResizeObserver(() => {
    if (!term) return;
    fit.fit();
    if (ws.readyState === 1)
      ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
  });
  ro.observe($("#terminal"));
  dlg.addEventListener("close", () => ro.disconnect(), { once: true });
}
async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    state = await api(agent ? "/api/local/state" : "/api/state");
    authenticated = true;
    render();
  } catch (e) {
    if (agent) {
      if (!$("#content")) shell();
      $("#content").innerHTML =
        '<div class="notice error">无法连接后台服务，请确认 NetConductorAgent 服务正在运行。</div>';
    } else if (authenticated) {
      $("#live").textContent = "同步失败，正在重试";
      $("#live").style.color = "#d63948";
    }
  } finally {
    refreshing = false;
  }
}
document.addEventListener("click", async (e) => {
  const t = e.target.closest("[data-tab]");
  if (t) {
    tab = t.dataset.tab;
    render();
    return;
  }
  const b = e.target.closest("[data-action]");
  if (b) {
    try {
      await action(b.dataset.action, b.dataset.id);
    } catch (e) {
      toast(e.message);
    }
  }
});
$("#dialog").addEventListener("cancel", () => {
  socket?.close();
  term?.dispose();
  term = null;
});
refresh();
setInterval(() => {
  if (agent || authenticated) refresh();
}, 3000);

function canRemote(d) {
  const [major, minor] = String(d?.agentVersion || "0.0")
    .split(".")
    .map(Number);
  return d?.id === "cloud" || major > 0 || minor >= 2;
}
