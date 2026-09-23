(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  /* ------------------------------------------------------------------ *
   * helpers
   * ------------------------------------------------------------------ */
  function b64ToBytes(b64) {
    if (!b64) return new Uint8Array(0);
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }
  function bytesToB64(bytes) {
    let bin = "";
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin);
  }
  function bytesToHexLines(bytes) {
    const rows = [""];
    for (let i = 0; i < bytes.length; i++) {
      if (bytes[i] === 0x0a) {
        rows.push("");
      } else {
        const row = rows[rows.length - 1];
        rows[rows.length - 1] = (row === "" ? "" : row + " ") + bytes[i].toString(16).padStart(2, "0");
      }
    }
    return rows.join("\n");
  }
  function formatBytes(n) {
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / 1024 / 1024).toFixed(2) + " MB";
  }
  function formatSize(n) {
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + " MB";
    return (n / 1024 / 1024 / 1024).toFixed(2) + " GB";
  }
  function fmtTime(iso) {
    const d = iso ? new Date(iso) : new Date();
    const p = (v, w = 2) => String(v).padStart(w, "0");
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
  }
  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  function termSize(el) {
    const cs = getComputedStyle(el);
    const lh = parseFloat(cs.lineHeight) || 18;
    const probe = document.createElement("span");
    probe.textContent = "0000000000";
    probe.style.cssText = "position:absolute;visibility:hidden;white-space:pre";
    probe.style.fontFamily = cs.fontFamily;
    probe.style.fontSize = cs.fontSize;
    document.body.appendChild(probe);
    const cw = probe.getBoundingClientRect().width / 10 || 7.5;
    probe.remove();
    return {
      cols: Math.max(20, Math.floor((el.clientWidth - 24) / cw)),
      rows: Math.max(5, Math.floor((el.clientHeight - 20) / lh)),
    };
  }

  /* ------------------------------------------------------------------ *
   * console view (line buffered terminal model)
   * ------------------------------------------------------------------ */
  class ConsoleView {
    constructor(el, opts = {}) {
      this.el = el;
      this.max = opts.max || 5000;
      this.events = [];
      this.hex = false;
      this.ts = true;
      this.auto = true;
      this.raf = 0;
      this.reset();
    }
    reset() {
      if (this.raf) { cancelAnimationFrame(this.raf); this.raf = 0; }
      this.el.textContent = "";
      this.active = null;
      this.before = null;
      this.after = null;
      this.lineCount = 0;
      this.decoder = new TextDecoder("utf-8", { fatal: false });
      this.state = "text";
      this.seq = "";
      this.cursorEl = document.createElement("span");
      this.cursorEl.className = "term-cursor";
      this.el.appendChild(this.cursorEl);
    }
    push(kind, bytes, time) {
      const ev = { kind, bytes, time };
      this.events.push(ev);
      if (this.events.length > this.max) this.events.splice(0, this.events.length - this.max);
      this.apply(ev);
    }
    apply(ev) {
      const isLog = ev.kind === "info" || ev.kind === "error" || ev.kind === "closed";
      if (isLog) {
        this.freeze();
        const text = new TextDecoder("utf-8", { fatal: false }).decode(ev.bytes);
        for (const line of text.split("\n")) if (line !== "") this.writeLine(ev.kind, ev.time, line);
        if (this.active) this.endLine();
        this.schedule();
        return;
      }
      if (this.hex) {
        this.freeze();
        for (const line of bytesToHexLines(ev.bytes).split("\n")) this.writeLine(ev.kind, ev.time, line);
        this.schedule();
        return;
      }
      const chunk = this.decoder.decode(ev.bytes, { stream: true });
      for (const ch of chunk) this.step(ch, ev.kind, ev.time);
      this.schedule();
    }
    makeSpan(kind, time) {
      const span = document.createElement("span");
      span.className = "line " + kind;
      if (this.ts) {
        const ts = document.createElement("span");
        ts.className = "ts";
        ts.textContent = "[" + fmtTime(time) + "]";
        span.appendChild(ts);
      }
      return span;
    }
    placeCursorAtEnd() {
      let last = null;
      for (const child of this.el.children) {
        if (child.classList && child.classList.contains("line")) last = child;
      }
      if (last) last.appendChild(this.cursorEl);
      else this.el.appendChild(this.cursorEl);
    }
    writeLine(kind, time, text) {
      const span = this.makeSpan(kind, time);
      span.appendChild(document.createTextNode(text));
      this.el.appendChild(span);
      this.lineCount++;
      this.trim();
      this.placeCursorAtEnd();
    }
    startActive(kind, time) {
      const span = this.makeSpan(kind, time);
      this.before = document.createTextNode("");
      this.after = document.createTextNode("");
      span.appendChild(this.before);
      span.appendChild(this.cursorEl);
      span.appendChild(this.after);
      this.el.appendChild(span);
      this.lineCount++;
      this.trim();
      this.active = { kind, chars: [], text: "", col: 0 };
    }
    endLine() {
      // Flush the active line into the DOM before dropping it: rendering is
      // deferred to the next frame, so the text must be written here.
      if (!this.active) return;
      this.before.nodeValue = this.active.text;
      this.after.nodeValue = "";
      this.active = null;
      this.placeCursorAtEnd();
    }
    freeze() { this.endLine(); }
    putChar(ch, kind, time) {
      if (!this.active) this.startActive(kind, time);
      const a = this.active;
      if (a.col === a.chars.length) {
        a.chars.push(ch);
        a.text += ch;
      } else {
        a.chars[a.col] = ch;
        a.text = a.text.slice(0, a.col) + ch + a.text.slice(a.col + 1);
      }
      a.col++;
    }
    trim() {
      while (this.lineCount > this.max) {
        const first = this.el.firstChild;
        if (!first || first === this.cursorEl) break;
        this.el.removeChild(first);
        this.lineCount--;
      }
    }
    step(ch, kind, time) {
      const c = ch.charCodeAt(0);
      switch (this.state) {
        case "text":
          if (c === 0x1b) { this.state = "esc"; return; }
          if (c === 0x9b) { this.state = "csi"; this.seq = ""; return; }
          if (c === 0x0a) { if (this.active) this.endLine(); return; }
          if (c === 0x0d) { if (this.active) this.active.col = 0; return; }
          if (c === 0x08 || c === 0x7f) {
            if (this.active && this.active.col > 0) this.active.col--;
            return;
          }
          if (c === 0x09) { this.putChar("\t", kind, time); return; }
          if (c < 0x20) return;
          this.putChar(ch, kind, time);
          return;
        case "esc":
          if (ch === "[") { this.state = "csi"; this.seq = ""; return; }
          if (ch === "]") { this.state = "osc"; return; }
          if (ch === "P" || ch === "X" || ch === "^" || ch === "_") { this.state = "str"; return; }
          if (c >= 0x20 && c <= 0x2f) { this.state = "escInter"; return; }
          this.state = "text";
          return;
        case "escInter":
          if (c >= 0x20 && c <= 0x2f) return;
          this.state = "text";
          return;
        case "csi":
          if (c >= 0x30 && c <= 0x3f) { this.seq += ch; return; }
          if (c >= 0x20 && c <= 0x2f) { this.seq += ch; return; }
          if (c >= 0x40 && c <= 0x7e) { this.applyCSI(ch); this.state = "text"; return; }
          this.state = "text";
          return;
        case "osc":
          if (c === 0x07) { this.state = "text"; return; }
          if (c === 0x1b) { this.state = "oscEsc"; return; }
          return;
        case "oscEsc":
          this.state = ch === "\\" ? "text" : "osc";
          return;
        case "str":
          if (c === 0x07) { this.state = "text"; return; }
          if (c === 0x1b) { this.state = "strEsc"; return; }
          return;
        case "strEsc":
          this.state = ch === "\\" ? "text" : "str";
          return;
        default:
          this.state = "text";
          return;
      }
    }
    applyCSI(final) {
      const parts = this.seq.replace(/[^0-9;]/g, "").split(";");
      const num = (i, def) => {
        const v = parseInt(parts[i], 10);
        return isNaN(v) ? def : v;
      };
      const a = this.active;
      switch (final) {
        case "K": {
          if (!a) return;
          const mode = num(0, 0);
          if (mode === 1) {
            const n = Math.min(a.col, a.chars.length);
            for (let i = 0; i < n; i++) a.chars[i] = " ";
            a.text = " ".repeat(n) + a.text.slice(n);
          } else if (mode === 2) {
            a.chars = [];
            a.text = "";
            a.col = 0;
          } else {
            a.chars.length = Math.min(a.chars.length, a.col);
            a.text = a.text.slice(0, a.col);
          }
          return;
        }
        case "D": if (a) a.col = Math.max(0, a.col - num(0, 1)); return;
        case "C": if (a) a.col = Math.min(a.chars.length, a.col + num(0, 1)); return;
        case "G": if (a) a.col = Math.max(0, num(0, 1) - 1); return;
        default: return;
      }
    }
    schedule() {
      if (this.raf) return;
      this.raf = requestAnimationFrame(() => {
        this.raf = 0;
        this.render();
      });
    }
    render() {
      const a = this.active;
      if (a && this.before) {
        this.before.nodeValue = a.text.slice(0, a.col);
        this.after.nodeValue = a.text.slice(a.col);
      }
      if (this.auto) this.el.scrollTop = this.el.scrollHeight;
    }
    renderNow() {
      if (this.raf) { cancelAnimationFrame(this.raf); this.raf = 0; }
      this.render();
    }
    rerender() {
      const events = this.events;
      this.reset();
      for (const ev of events) this.apply(ev);
      this.renderNow();
    }
    clear() {
      this.events = [];
      this.reset();
    }
    scrollBottom() {
      this.el.scrollTop = this.el.scrollHeight;
    }
  }

  /* ------------------------------------------------------------------ *
   * state
   * ------------------------------------------------------------------ */
  const state = {
    activeTab: null,      // "agent" | device id
    focusDevice: null,    // device id the agent / workspace target
    devices: new Map(),
    agentOpen: false,
    agent: { baseUrl: "", apiKey: "", model: "", autoRun: false, target: "remote", running: false, mode: "normal" },
    ws: {
      sides: ["local"],
      side: "local",
      paths: { local: "", remote: "/", serial: "/" },
      entries: [],
      selected: null,
      visible: true,
    },
    kits: { kits: [], tools: [] },
    localEcho: false,
    sidebar: true,
    fontScale: 1,
    rx: 0,
    tx: 0,
  };

  const SIDE_LABEL = { local: "本地", remote: "远端", serial: "串口" };
  const FOLDER_SVG = '<svg viewBox="0 0 16 16"><path d="M1.5 4.5A1.5 1.5 0 0 1 3 3h3l1.5 1.8H13a1.5 1.5 0 0 1 1.5 1.5v5.2A1.5 1.5 0 0 1 13 13H3a1.5 1.5 0 0 1-1.5-1.5z"/></svg>';
  const FILE_SVG = '<svg viewBox="0 0 16 16"><path d="M9 1.8H4.5a1 1 0 0 0-1 1v10.4a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1V5.3z"/><path d="M9 1.8v3.5h3.5"/></svg>';
  const ICON = {
    serial: '<svg viewBox="0 0 16 16"><path d="M6 1.5v3M10 1.5v3M4.5 4.5h7v3.5a3.5 3.5 0 0 1-7 0zM8 11.5V14"/></svg>',
    ssh: '<svg viewBox="0 0 16 16"><rect x="1.5" y="2.5" width="13" height="11" rx="1.5"/><path d="M4 6l2 2-2 2M8 10h4"/></svg>',
  };

  /* ------------------------------------------------------------------ *
   * device sessions
   * ------------------------------------------------------------------ */
  function createDeviceSession(info, autoShell) {
    const ds = Object.assign(
      { connected: false, shell: false, sftp: false, config: null, tabOpen: true },
      info,
      { console: null, view: null, tab: null, item: null, autoShell: !!autoShell && info.kind === "ssh" }
    );

    const item = document.createElement("li");
    item.className = "session-item";
    item.innerHTML = `<span class="s-ico ${esc(ds.kind)}">${ICON[ds.kind] || ""}</span>
      <span class="s-info"><b></b><small></small></span><span class="s-dot"></span>`;
    item.addEventListener("click", () => activateSession(ds.id));
    $("session-list").appendChild(item);
    ds.item = item;

    const tab = document.createElement("div");
    tab.className = "tab";
    tab.innerHTML = `<span class="tab-accent ${esc(ds.kind)}"></span><span class="tab-label"></span><span class="tab-x" title="关闭会话">×</span>`;
    tab.addEventListener("click", () => activateSession(ds.id));
    tab.querySelector(".tab-x").addEventListener("click", (e) => {
      e.stopPropagation();
      closeDeviceTab(ds.id);
    });
    const spacer = $("tabstrip").querySelector(".spacer");
    $("tabstrip").insertBefore(tab, spacer);
    ds.tab = tab;

    const view = document.createElement("section");
    view.className = "view";
    view.innerHTML = `<div class="term-head">
        <span class="term-dot"></span>
        <span class="term-title"></span>
        <span class="term-hint">点击终端后可直接输入</span>
        <span class="spacer"></span>
        <button class="btn small act-shell hidden">打开 Shell</button>
      </div>
      <pre class="output" tabindex="0" spellcheck="false"></pre>`;
    $("tabbody").insertBefore(view, $("empty-state"));
    ds.view = view;
    ds.titleEl = view.querySelector(".term-title");
    ds.dotEl = view.querySelector(".term-dot");
    ds.shellBtn = view.querySelector(".act-shell");
    ds.shellBtn.addEventListener("click", () => toggleShell(ds));

    const output = view.querySelector(".output");
    output.addEventListener("keydown", onTermKey);
    output.addEventListener("paste", onTermPaste);
    ds.console = new ConsoleView(output);
    ds.termEl = output;

    state.devices.set(ds.id, ds);
    applyDeviceStatus(ds);
    // Replay the device's record so a reloaded UI keeps its scrollback.
    requestTimeline(ds);
    return ds;
  }

  // The host keeps an append-only timeline per device; ask for the tail of it.
  function requestTimeline(ds) {
    send("timeline", { limit: 3000 }, ds.id);
  }

  function onTimelineRecords(p) {
    const ds = state.devices.get(p.sessionId);
    if (!ds || !p.records || !p.records.length) return;
    if (ds.console.events.length > 0) return; // live content is already there
    for (const r of p.records) {
      if (r.channel === "agent") continue;
      ds.console.push(r.kind || "rx", b64ToBytes(r.data), r.time);
    }
  }

  function removeDeviceSession(id) {
    const ds = state.devices.get(id);
    if (!ds) return;
    ds.tab.remove();
    ds.item.remove();
    ds.view.remove();
    ds.console.clear();
    state.devices.delete(id);
  }

  function applyDeviceStatus(ds) {
    if (!ds) return;
    const connected = !!ds.connected;
    const label = ds.kind === "serial" ? shortPort(ds.label) : ds.label;
    ds.tab.querySelector(".tab-label").textContent = (ds.kind === "serial" ? "串口 · " : "SSH · ") + label;
    ds.item.querySelector("b").textContent = ds.kind === "serial" ? "串口会话" : "SSH 会话";
    ds.item.querySelector("small").textContent = label;
    ds.item.querySelector(".s-dot").classList.toggle("on", connected);
    ds.titleEl.textContent = (ds.kind === "serial" ? "串口 " : "SSH ") + ds.label + (connected ? "" : "（未连接）");
    ds.dotEl.classList.toggle("on", connected);
    ds.shellBtn.classList.toggle("hidden", ds.kind !== "ssh");
    ds.shellBtn.textContent = ds.shell ? "关闭 Shell" : "打开 Shell";
    if (ds.id === state.focusDevice) {
      updateDeviceInfo(ds);
      updateWorkspaceContext(false);
    }
    updatePill();
  }

  // closeDeviceSession tears the connection down (server-side) and removes the
  // session entirely. It is triggered from the 会话信息 panel.
  function closeDeviceSession(id) {
    const ds = state.devices.get(id);
    if (!ds) return;
    if (ds.kind === "serial") send("serial.close", null, id);
    else send("ssh.disconnect", null, id);
  }

  // Closing a tab only hides the view; the connection stays up.
  function openDeviceTab(id) {
    const ds = state.devices.get(id);
    if (!ds) return;
    ds.tabOpen = true;
    ds.tab.classList.remove("hidden");
  }

  function closeDeviceTab(id) {
    const ds = state.devices.get(id);
    if (!ds || !ds.tabOpen) return;
    ds.tabOpen = false;
    ds.tab.classList.add("hidden");
    if (state.activeTab !== id) return;
    const next = nextVisibleTab(id);
    if (next) activateSession(next);
    else showEmpty();
  }

  function nextVisibleTab(exclude) {
    for (const ds of state.devices.values()) {
      if (ds.id !== exclude && ds.tabOpen) return ds.id;
    }
    return state.agentOpen ? "agent" : null;
  }

  function startShell(ds) {
    if (ds.kind !== "ssh" || ds.shell) return;
    const { cols, rows } = termSize(ds.termEl);
    send("ssh.shell.start", { cols, rows }, ds.id);
  }

  function toggleShell(ds) {
    if (ds.kind !== "ssh") return;
    if (ds.shell) send("ssh.shell.close", null, ds.id);
    else startShell(ds);
  }

  function shortPort(name) {
    return (name || "").split("/").pop();
  }

  function focusedDevice() {
    return state.focusDevice ? state.devices.get(state.focusDevice) : null;
  }

  /* ------------------------------------------------------------------ *
   * tab / session activation
   * ------------------------------------------------------------------ */
  function activateSession(id) {
    state.activeTab = id;
    const isAgent = id === "agent";
    $("tab-agent").classList.toggle("active", isAgent);
    $("sess-agent").classList.toggle("active", isAgent);
    $("view-agent").classList.toggle("active", isAgent);
    for (const ds of state.devices.values()) {
      const on = ds.id === id;
      ds.tab.classList.toggle("active", on);
      ds.item.classList.toggle("active", on);
      ds.view.classList.toggle("active", on);
    }
    $("empty-state").classList.remove("show");
    $("settings-agent").classList.toggle("hidden", !isAgent);
    $("settings-device").classList.toggle("hidden", isAgent);

    if (isAgent) {
      $("agent-input").focus();
    } else {
      const ds = state.devices.get(id);
      if (ds) {
        openDeviceTab(id);
        state.focusDevice = id;
        send("session.focus", null, id);
        updateAgentFocus();
        updateDeviceInfo(ds);
        if (ds.termEl.tabIndex >= 0) ds.termEl.focus();
        if (ds.kind === "ssh" && ds.shell) {
          const { cols, rows } = termSize(ds.termEl);
          send("ssh.shell.resize", { cols, rows }, ds.id);
        }
      }
    }
    syncToolbar();
    updateWorkspaceContext(true);
  }

  function showEmpty() {
    state.activeTab = null;
    $("tab-agent").classList.remove("active");
    $("sess-agent").classList.remove("active");
    $("view-agent").classList.remove("active");
    for (const ds of state.devices.values()) {
      ds.tab.classList.remove("active");
      ds.item.classList.remove("active");
      ds.view.classList.remove("active");
    }
    $("empty-state").classList.add("show");
    $("settings-agent").classList.add("hidden");
    $("settings-device").classList.add("hidden");
    updatePill();
    updateMenuState();
    updateWorkspaceContext(true);
  }

  function openAgentTab() {
    state.agentOpen = true;
    $("tab-agent").classList.remove("hidden");
    activateSession("agent");
  }

  function closeAgentTab() {
    state.agentOpen = false;
    $("tab-agent").classList.add("hidden");
    if (state.activeTab === "agent") {
      const next = state.focusDevice || (state.devices.size ? state.devices.keys().next().value : null);
      if (next) activateSession(next);
      else showEmpty();
    }
  }

  /* ------------------------------------------------------------------ *
   * websocket
   * ------------------------------------------------------------------ */
  let ws = null;
  let retry = null;

  function connect() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const url = window.__EDGEKIT_WS__ || `${proto}://${location.host}/ws`;
    ws = new WebSocket(url);
    ws.onopen = () => {
      setWS("online", "已连接");
      send("serial.list");
    };
    ws.onclose = () => {
      setWS("offline", "连接断开");
      if (retry) clearTimeout(retry);
      retry = setTimeout(connect, 1500);
    };
    ws.onerror = () => { try { ws.close(); } catch (_) { /* ignore */ } };
    ws.onmessage = (e) => {
      let msg;
      try { msg = JSON.parse(e.data); } catch (_) { return; }
      handle(msg);
    };
  }

  function send(type, payload, sessionId) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    const msg = { type };
    if (sessionId) msg.sessionId = sessionId;
    if (payload !== undefined && payload !== null) msg.payload = payload;
    ws.send(JSON.stringify(msg));
  }

  function setWS(cls, text) {
    const el = $("ws-state");
    el.className = "ws-state " + cls;
    el.querySelector("span").textContent = text;
  }

  /* ------------------------------------------------------------------ *
   * dispatch
   * ------------------------------------------------------------------ */
  function handle(msg) {
    switch (msg.type) {
      case "sessions": onSessions(msg.payload || {}); break;
      case "session.opened": onSessionOpened(msg.payload || {}); break;
      case "session.closed": onSessionClosed(msg.payload || {}); break;
      case "session.focus": onSessionFocus(msg.payload || {}); break;
      case "serial.ports": onPorts(msg.payload.ports || []); break;
      case "serial.status": onSerialStatus(msg.payload || {}); break;
      case "serial.event": onSerialEvent(msg.payload || {}); break;
      case "ssh.status": onSSHStatus(msg.payload || {}); break;
      case "ssh.event": onSSHEvent(msg.payload || {}); break;
      case "agent.event": onAgentEvent(msg.payload || {}); break;
      case "agent.config": onAgentConfig(msg.payload || {}); break;
      case "settings": applySettings(msg.payload); break;
      case "kits": state.kits = msg.payload || { kits: [], tools: [] }; break;
      case "fs.files": onFSFiles(msg.payload || {}); break;
      case "fs.done": onFSDone(msg.payload || {}); break;
      case "fs.content": onFSContent(msg.payload || {}); break;
      case "timeline.records": onTimelineRecords(msg.payload || {}); break;
      case "error": onError(msg.payload && msg.payload.message); break;
      default: break;
    }
  }

  function onError(message) {
    if (!message) return;
    toast(message);
    const ds = state.activeTab && state.activeTab !== "agent" ? state.devices.get(state.activeTab) : null;
    if (ds) ds.console.push("error", new TextEncoder().encode(message), new Date().toISOString());
    else if (state.activeTab === "agent") appendAgentMessage("error", message);
  }

  function onSessions(payload) {
    const seen = new Set();
    for (const info of payload.sessions || []) {
      seen.add(info.id);
      let ds = state.devices.get(info.id);
      if (!ds) ds = createDeviceSession(info, false);
      Object.assign(ds, {
        kind: info.kind, label: info.label,
        connected: !!info.connected, shell: !!info.shell, sftp: !!info.sftp, config: info.config,
      });
      applyDeviceStatus(ds);
    }
    for (const id of Array.from(state.devices.keys())) {
      if (!seen.has(id)) removeDeviceSession(id);
    }
    state.focusDevice = payload.focus || null;
    updateAgentFocus();
    if (state.activeTab && state.activeTab !== "agent" && !state.devices.has(state.activeTab)) {
      state.activeTab = null;
    }
    if (!state.activeTab) {
      if (payload.focus && state.devices.has(payload.focus)) activateSession(payload.focus);
      else if (state.agentOpen) activateSession("agent");
      else showEmpty();
    }
  }

  function onSessionOpened(info) {
    if (state.devices.has(info.id)) { activateSession(info.id); return; }
    createDeviceSession(info, true); // a freshly opened SSH session gets a shell
    if (info.id) {
      Object.assign(state.devices.get(info.id), {
        connected: !!info.connected, shell: !!info.shell, sftp: !!info.sftp, config: info.config,
      });
      activateSession(info.id);
    }
  }

  function onSessionClosed(payload) {
    removeDeviceSession(payload.id);
    state.focusDevice = payload.focus || null;
    updateAgentFocus();
    if (state.activeTab === payload.id || !state.activeTab) {
      if (state.focusDevice && state.devices.has(state.focusDevice)) activateSession(state.focusDevice);
      else if (state.agentOpen) activateSession("agent");
      else showEmpty();
    }
  }

  function onSessionFocus(payload) {
    if (!payload.id) return;
    state.focusDevice = payload.id;
    updateAgentFocus();
    updateWorkspaceContext(false);
  }

  function updateAgentFocus() {
    const ds = focusedDevice();
    $("m-agent-focus").textContent = ds
      ? (ds.kind === "serial" ? shortPort(ds.label) : ds.label)
      : "未选择";
  }

  /* ------------------------------------------------------------------ *
   * serial / ssh status + events
   * ------------------------------------------------------------------ */
  function onPorts(ports) {
    const sel = $("sel-port");
    const prev = sel.value;
    sel.textContent = "";
    if (!ports.length) {
      const o = document.createElement("option");
      o.value = "";
      o.textContent = "未发现串口";
      sel.appendChild(o);
      $("port-hint").textContent = "请检查设备连接或权限 (dialout 组)。";
      return;
    }
    for (const p of ports) {
      const o = document.createElement("option");
      o.value = p.name;
      const extra = [p.description, p.manufacturer].filter(Boolean).join(" · ");
      o.textContent = p.name + (extra ? ` — ${extra}` : "");
      sel.appendChild(o);
    }
    if (prev && ports.some((p) => p.name === prev)) sel.value = prev;
    $("port-hint").textContent = `共 ${ports.length} 个端口`;
  }

  function onSerialStatus(p) {
    const ds = state.devices.get(p.sessionId);
    if (!ds) return;
    ds.connected = !!p.open;
    ds.config = p.config;
    applyDeviceStatus(ds);
  }

  function onSerialEvent(p) {
    const ds = state.devices.get(p.sessionId);
    if (!ds) return;
    const bytes = b64ToBytes(p.data);
    if (p.direction === "tx") {
      state.tx += bytes.length;
      if (state.localEcho) ds.console.push("tx", bytes, p.time);
    } else {
      if (p.direction === "rx") state.rx += bytes.length;
      ds.console.push(p.direction || "rx", bytes, p.time);
    }
    updateTraffic();
  }

  function onSSHStatus(p) {
    const ds = state.devices.get(p.sessionId);
    if (!ds) return;
    ds.connected = !!p.connected;
    ds.shell = !!p.shell;
    ds.sftp = !!p.sftp;
    ds.config = p.config;
    applyDeviceStatus(ds);
    // Newly connected SSH sessions open an interactive shell by default.
    if (ds.autoShell && ds.connected && !ds.shell) {
      ds.autoShell = false;
      startShell(ds);
    }
  }

  function onSSHEvent(p) {
    const ds = state.devices.get(p.sessionId);
    if (!ds) return;
    ds.console.push(p.kind || "stdout", b64ToBytes(p.data), p.time);
  }

  function updateTraffic() {
    $("status-traffic").textContent = `RX ${formatBytes(state.rx)} / TX ${formatBytes(state.tx)}`;
  }

  /* ------------------------------------------------------------------ *
   * device info panel
   * ------------------------------------------------------------------ */
  function updateDeviceInfo(ds) {
    if (!ds) return;
    const rows = [
      ["类型", ds.kind === "serial" ? "串口" : "SSH"],
      ["目标", ds.label || "—"],
      ["状态", ds.connected ? "已连接" : "未连接"],
    ];
    if (ds.kind === "serial" && ds.config) rows.push(["波特率", ds.config.baud]);
    if (ds.kind === "ssh") rows.push(["Shell", ds.shell ? "已打开" : "未打开"], ["SFTP", ds.sftp ? "就绪" : "未就绪"]);
    $("device-info").innerHTML = rows
      .map(([k, v]) => `<div class="dev-row"><span>${esc(k)}</span><b>${esc(v)}</b></div>`)
      .join("");
    $("m-device").textContent = ds.connected ? "已连接" : "未连接";
    $("btn-device-toggle").textContent = ds.kind === "serial" ? "关闭串口" : "断开 SSH";
    $("btn-device-shell").classList.toggle("hidden", ds.kind !== "ssh");
    $("btn-device-shell").textContent = ds.shell ? "关闭 Shell" : "打开 Shell";
  }

  /* ------------------------------------------------------------------ *
   * workspace (local / remote / serial)
   * ------------------------------------------------------------------ */
  function computeSides() {
    const ds = focusedDevice();
    const sshReady = !!(ds && ds.kind === "ssh" && ds.sftp);
    const serReady = !!(ds && ds.kind === "serial" && ds.connected);
    const sides = [];
    if (!(ds && ds.kind === "serial" && !sshReady)) sides.push("local");
    if (sshReady) sides.push("remote");
    if (serReady) sides.push("serial");
    return sides.length ? sides : ["local"];
  }

  function preferredSide(sides) {
    let pref = "local";
    if (state.activeTab === "agent" && state.agent.target === "remote") pref = "remote";
    else {
      const ds = focusedDevice();
      if (ds && ds.kind === "serial") pref = "serial";
    }
    if (sides.includes(pref)) return pref;
    return sides[0];
  }

  function updateWorkspaceContext(force) {
    const sides = computeSides();
    const prev = state.ws.sides;
    const same = sides.length === prev.length && sides.every((s, i) => s === prev[i]);
    state.ws.sides = sides;

    let want = state.ws.side;
    if (force || !sides.includes(want)) want = preferredSide(sides);
    if (want === state.ws.side && same && !force) return;

    state.ws.side = want;
    state.ws.selected = null;
    renderSideToggle();
    wsRefresh();
  }

  function renderSideToggle() {
    const seg = $("ws-sides");
    seg.textContent = "";
    for (const side of state.ws.sides) {
      const b = document.createElement("button");
      b.textContent = SIDE_LABEL[side] || side;
      b.className = side === state.ws.side ? "active" : "";
      b.addEventListener("click", () => wsSetSide(side));
      seg.appendChild(b);
    }
  }

  function wsSetSide(side) {
    if (!state.ws.sides.includes(side)) return;
    state.ws.side = side;
    state.ws.selected = null;
    renderSideToggle();
    wsRefresh();
  }

  function wsCurrentPath() {
    return state.ws.paths[state.ws.side] || (state.ws.side === "local" ? "" : "/");
  }
  function joinSidePath(base, name) {
    const b = base || "/";
    return b === "/" ? "/" + name : b.replace(/\/$/, "") + "/" + name;
  }
  function wsRefresh() {
    const side = state.ws.side;
    send("fs.list", { side, path: wsCurrentPath() }, side === "local" ? null : state.focusDevice);
  }

  function onFSFiles(p) {
    if (p.side !== state.ws.side) return;
    const prevName = state.ws.selected ? state.ws.selected.name : null;
    state.ws.paths[p.side] = p.path || "";
    state.ws.entries = p.entries || [];
    state.ws.selected = null;
    $("ws-path").textContent = p.display || wsCurrentPath() || "/";
    renderWorkspaceList();
    // Keep the selection across refreshes when the entry still exists.
    if (prevName) {
      const entry = state.ws.entries.find((e) => e.name === prevName);
      const row = Array.from($("ws-list").querySelectorAll(".ws-row")).find((r) => {
        const nm = r.querySelector(".ws-name");
        return nm && nm.textContent === prevName;
      });
      if (entry && row) wsSelect(row, entry);
    }
    updateWorkspaceStatus();
  }

  function renderWorkspaceList() {
    const list = $("ws-list");
    list.textContent = "";
    if (!state.ws.entries.length) {
      const d = document.createElement("div");
      d.className = "ws-empty";
      d.textContent = "（空目录）";
      list.appendChild(d);
      return;
    }
    for (const e of state.ws.entries) {
      const row = document.createElement("div");
      row.className = "ws-row " + (e.isDir ? "dir" : "file");
      const ico = document.createElement("span");
      ico.className = "ws-ico";
      ico.innerHTML = e.isDir ? FOLDER_SVG : FILE_SVG;
      const name = document.createElement("span");
      name.className = "ws-name";
      name.textContent = e.name;
      name.title = e.name + (e.time ? "  " + e.time : "");
      const size = document.createElement("span");
      size.className = "ws-size";
      size.textContent = e.isDir ? "" : formatSize(e.size);
      if (e.isDir) name.addEventListener("click", (ev) => { ev.stopPropagation(); wsEnter(e.name); });
      row.addEventListener("click", () => wsSelect(row, e));
      row.append(ico, name, size);
      list.appendChild(row);
    }
  }

  function wsSelect(row, entry) {
    $("ws-list").querySelectorAll(".ws-row.sel").forEach((el) => el.classList.remove("sel"));
    row.classList.add("sel");
    state.ws.selected = entry;
    updateWorkspaceStatus();
  }
  function wsEnter(name) {
    state.ws.paths[state.ws.side] = joinSidePath(wsCurrentPath(), name);
    wsRefresh();
  }
  function wsUp() {
    const p = wsCurrentPath();
    if (!p || p === "/") return;
    const trimmed = p.replace(/\/+$/, "");
    const idx = trimmed.lastIndexOf("/");
    state.ws.paths[state.ws.side] = idx <= 0 ? (state.ws.side === "local" ? "" : "/") : trimmed.slice(0, idx);
    wsRefresh();
  }
  function wsMkdir() {
    askText("新建文件夹", "文件夹名称").then((name) => {
      if (!name) return;
      send("fs.mkdir", { side: state.ws.side, path: joinSidePath(wsCurrentPath(), name) }, state.ws.side === "local" ? null : state.focusDevice);
    });
  }
  function wsNewFile() {
    askText("新建文件", "文件名称").then((name) => {
      if (!name) return;
      send("fs.newfile", { side: state.ws.side, path: joinSidePath(wsCurrentPath(), name) }, state.ws.side === "local" ? null : state.focusDevice);
    });
  }
  function wsUpload() {
    const ds = focusedDevice();
    if (!ds || ds.kind !== "ssh" || !ds.sftp) { toast("请先连接 SSH 会话后再上传"); return; }
    $("ws-file").click();
  }
  function wsUploadFile(file) {
    const reader = new FileReader();
    reader.onload = () => {
      send("fs.upload", {
        dir: state.ws.paths.remote || "/",
        name: file.name,
        data: bytesToB64(new Uint8Array(reader.result)),
      }, state.focusDevice);
    };
    reader.readAsArrayBuffer(file);
  }
  function wsDownload() {
    const sel = state.ws.selected;
    if (state.ws.side !== "remote" || !sel || sel.isDir) { toast("请在「远端」中选择一个文件"); return; }
    send("fs.download", { path: joinSidePath(wsCurrentPath(), sel.name) }, state.focusDevice);
  }
  function wsDelete() {
    const sel = state.ws.selected;
    if (!sel) { toast("请先选择一个文件或文件夹"); return; }
    const side = state.ws.side;
    const target = joinSidePath(wsCurrentPath(), sel.name);
    confirmDialog("删除确认", `确定删除 ${target} 吗？${sel.isDir ? "（含其中全部内容）" : ""}`).then((ok) => {
      if (!ok) return;
      send("fs.delete", { side, path: target }, side === "local" ? null : state.focusDevice);
    });
  }

  function wsEdit() {
    const sel = state.ws.selected;
    if (!sel || sel.isDir) { toast("请选择一个文件"); return; }
    const side = state.ws.side;
    if (side === "serial") { toast("串口模式暂不支持在线编辑"); return; }
    send("fs.read", { side, path: joinSidePath(wsCurrentPath(), sel.name) },
      side === "local" ? null : state.focusDevice);
  }

  function onFSContent(p) {
    const text = new TextDecoder("utf-8", { fatal: false }).decode(b64ToBytes(p.data));
    editModal("编辑 " + p.path, text).then((value) => {
      if (value === null) return;
      send("fs.write", {
        side: p.side, path: p.path,
        data: bytesToB64(new TextEncoder().encode(value)),
      }, p.side === "local" ? null : state.focusDevice);
    });
  }

  function onFSDone(p) {
    const size = p.size ? ` (${formatSize(p.size)})` : "";
    if (p.op === "upload") toast(`已上传 → ${p.path}${size}`);
    else if (p.op === "download") toast(`已下载 → ${p.path}${size}`);
    else if (p.op === "delete") toast(`已删除 ${p.path}`);
    else if (p.op === "write") toast(`已保存 ${p.path}${size}`);
    wsRefresh();
  }
  function updateWorkspaceStatus() {
    const sel = state.ws.selected;
    $("ws-status").textContent = sel ? sel.name : state.ws.entries.length + " 项";
    const ds = focusedDevice();
    if (!sel && state.activeTab === "agent" && state.agent.target === "remote" && !(ds && ds.kind === "ssh" && ds.sftp)) {
      $("ws-status").textContent = "Remote 未连接";
    }
    $("status-workspace").textContent = "工作区：" + (SIDE_LABEL[state.ws.side] || state.ws.side);
    $("ws-upload").disabled = !(ds && ds.kind === "ssh" && ds.sftp);
    $("ws-download").disabled = !(state.ws.side === "remote" && sel && !sel.isDir);
    $("ws-delete").disabled = !sel;
    $("ws-edit").disabled = !(sel && !sel.isDir && state.ws.side !== "serial");
    $("ws-up").disabled = state.ws.side === "local" ? !wsCurrentPath() : wsCurrentPath() === "/";
  }

  /* ------------------------------------------------------------------ *
   * terminal keyboard input
   * ------------------------------------------------------------------ */
  const CTRL_MAP = { " ": "\x00", "[": "\x1b", "\\": "\x1c", "]": "\x1d", "^": "\x1e", "_": "\x1f", "?": "\x7f" };
  function keyToBytes(e) {
    if (e.altKey && !e.ctrlKey && !e.metaKey && e.key.length === 1) return "\x1b" + e.key;
    if (e.ctrlKey && !e.altKey && !e.metaKey) {
      const k = e.key.length === 1 ? e.key.toLowerCase() : "";
      if (k >= "a" && k <= "z") return String.fromCharCode(k.charCodeAt(0) - 96);
      if (CTRL_MAP[k] !== undefined) return CTRL_MAP[k];
      return null;
    }
    switch (e.key) {
      case "Enter": return "\r";
      case "Backspace": return "\x7f";
      case "Tab": return "\t";
      case "Escape": return "\x1b";
      case "ArrowUp": return "\x1b[A";
      case "ArrowDown": return "\x1b[B";
      case "ArrowRight": return "\x1b[C";
      case "ArrowLeft": return "\x1b[D";
      case "Home": return "\x1b[H";
      case "End": return "\x1b[F";
      case "Delete": return "\x1b[3~";
      case "PageUp": return "\x1b[5~";
      case "PageDown": return "\x1b[6~";
      case "Insert": return "\x1b[2~";
      default: break;
    }
    if (e.key.length === 1) return e.key;
    return null;
  }
  function onTermKey(e) {
    if (e.ctrlKey && e.shiftKey) {
      const k = e.key.toLowerCase();
      if (k === "c") { e.preventDefault(); copySelection(); return; }
      if (k === "v") { e.preventDefault(); pasteClipboard(); return; }
    }
    const data = keyToBytes(e);
    if (data === null) return;
    e.preventDefault();
    sendInput(data);
  }
  function onTermPaste(e) {
    const text = (e.clipboardData || window.clipboardData).getData("text");
    if (!text) return;
    e.preventDefault();
    sendInput(text);
  }
  function sendInput(text) {
    if (!text) return;
    const id = state.activeTab;
    if (!id || id === "agent") return;
    const ds = state.devices.get(id);
    if (!ds) return;
    if (ds.kind === "serial") {
      if (!ds.connected) { toast("串口未打开"); return; }
      send("serial.write", { data: text, hex: false }, id);
    } else {
      if (!ds.connected) { toast("SSH 未连接"); return; }
      if (!ds.shell) { toast("Shell 未打开"); return; }
      send("ssh.shell.write", { data: text }, id);
      if (state.localEcho) ds.console.push("tx", new TextEncoder().encode(text), new Date().toISOString());
    }
  }
  function copySelection() {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed) return;
    const text = sel.toString();
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).catch(() => {});
    } else {
      try { document.execCommand("copy"); } catch (_) { /* ignore */ }
    }
  }
  function pasteClipboard() {
    if (navigator.clipboard && navigator.clipboard.readText) {
      navigator.clipboard.readText().then((t) => sendInput(t)).catch(() => toast("无法读取剪贴板"));
    } else {
      toast("当前环境不支持读取剪贴板");
    }
  }

  /* ------------------------------------------------------------------ *
   * agent
   * ------------------------------------------------------------------ */
  function onAgentConfig(cfg) {
    state.agent.baseUrl = cfg.baseUrl || "";
    state.agent.apiKey = cfg.apiKey || "";
    state.agent.model = cfg.model || "";
    state.agent.autoRun = !!cfg.autoRun;
    state.agent.target = cfg.target === "local" ? "local" : "remote";
    $("in-agent-base").value = state.agent.baseUrl;
    $("in-agent-key").value = state.agent.apiKey;
    $("in-agent-model").value = state.agent.model;
    $("chk-agent-auto").checked = state.agent.autoRun;
    setAgentMode();
  }
  function agentConfigPayload() {
    return {
      baseUrl: state.agent.baseUrl,
      apiKey: state.agent.apiKey,
      model: state.agent.model,
      autoRun: state.agent.autoRun,
      target: state.agent.target,
    };
  }
  function pushAgentConfig() { send("agent.config", agentConfigPayload()); }

  function updateTargetSeg() {
    document.querySelectorAll("#agent-target button").forEach((b) => {
      b.classList.toggle("active", b.dataset.target === state.agent.target);
    });
  }
  function setAgentTarget(target) {
    state.agent.target = target === "local" ? "local" : "remote";
    updateTargetSeg();
    pushAgentConfig();
    persistSettings();
    updatePill();
    updateWorkspaceContext(false);
    if (state.activeTab === "agent") {
      const ds = focusedDevice();
      if (ds && state.agent.target === "remote" && ds.kind === "ssh" && ds.sftp) wsSetSide("remote");
      else if (state.agent.target === "local") wsSetSide("local");
      else updateWorkspaceStatus();
    }
  }
  function setAgentMode() {
    updateTargetSeg();
    const ai = !!(state.agent.apiKey && state.agent.model);
    state.agent.mode = ai ? "ai" : "normal";
    $("m-agent").textContent = ai ? "AI 模式" : "Normal 模式";
    $("sess-agent-sub").textContent = ai ? (state.agent.model || "AI 模式") : "Normal 模式";
    $("agent-hint").textContent = ai
      ? "已配置模型，可用自然语言驱动串口 / SSH / 工作区。"
      : "未配置 API Key，使用 Normal 模式（内置流程）。";
    updatePill();
    updateMenuState();
  }
  function saveAgentConfig() {
    state.agent.baseUrl = $("in-agent-base").value.trim();
    state.agent.apiKey = $("in-agent-key").value.trim();
    state.agent.model = $("in-agent-model").value.trim();
    state.agent.autoRun = $("chk-agent-auto").checked;
    pushAgentConfig();
    setAgentMode();
    persistSettings();
    toast("Agent 配置已保存");
  }
  function agentSend() {
    const input = $("agent-input");
    const text = input.value.trim();
    if (!text) return;
    if (state.agent.running) { toast("上一条指令仍在执行"); return; }
    openAgentTab();
    send("agent.send", { text });
    input.value = "";
    input.style.height = "auto";
  }
  function agentAsk(text) {
    $("agent-input").value = text;
    agentSend();
  }
  function onAgentEvent(ev) {
    switch (ev.kind) {
      case "user":
        appendAgentMessage("user", ev.text);
        state.agent.running = true;
        $("btn-agent-stop").disabled = false;
        break;
      case "assistant": appendAgentMessage("assistant", ev.text); break;
      case "status": appendAgentMessage("status", ev.text); break;
      case "error": appendAgentMessage("error", ev.text); break;
      case "tool": upsertToolCard(ev); break;
      case "approval": appendApproval(ev); break;
      case "done":
        state.agent.running = false;
        $("btn-agent-stop").disabled = true;
        break;
      default: break;
    }
  }
  const agentCards = new Map();
  function appendAgentMessage(role, text) {
    const div = document.createElement("div");
    div.className = "msg " + role;
    div.textContent = text;
    const chat = $("agent-chat");
    chat.appendChild(div);
    chat.scrollTop = chat.scrollHeight;
  }
  function upsertToolCard(ev) {
    const key = ev.tool + "\u0000" + (ev.args || "");
    let card = agentCards.get(key);
    if (ev.state === "running") {
      card = document.createElement("div");
      card.className = "msg tool";
      const head = document.createElement("div");
      head.className = "tool-head";
      const name = document.createElement("span");
      name.className = "tool-name";
      name.textContent = ev.tool;
      const st = document.createElement("span");
      st.className = "tool-state running";
      st.textContent = "执行中…";
      head.append(name, st);
      card.append(head);
      if (ev.args) {
        const args = document.createElement("div");
        args.className = "tool-args";
        args.textContent = ev.args;
        card.append(args);
      }
      agentCards.set(key, card);
      $("agent-chat").appendChild(card);
    } else if (card) {
      agentCards.delete(key);
      card.querySelector(".tool-state").className = "tool-state " + ev.state;
      card.querySelector(".tool-state").textContent =
        ev.state === "ok" ? "完成" : ev.state === "denied" ? "已拒绝" : "失败";
      if (ev.result) {
        const res = document.createElement("div");
        res.className = "tool-result";
        res.textContent = ev.result;
        card.append(res);
      }
    }
    const chat = $("agent-chat");
    chat.scrollTop = chat.scrollHeight;
  }
  function appendApproval(ev) {
    const card = document.createElement("div");
    card.className = "msg approval";
    const text = document.createElement("div");
    text.className = "approval-text";
    text.textContent = `Agent 请求执行修改性操作：${ev.tool} ${ev.args || ""}`;
    const actions = document.createElement("div");
    actions.className = "approval-actions";
    const allow = document.createElement("button");
    allow.className = "btn primary";
    allow.textContent = "允许执行";
    const deny = document.createElement("button");
    deny.className = "btn";
    deny.textContent = "拒绝";
    const finish = () => {
      actions.remove();
      const done = document.createElement("div");
      done.className = "tool-state";
      done.textContent = "已处理";
      card.append(done);
    };
    allow.addEventListener("click", () => { send("agent.approve", { id: ev.id, allow: true }); finish(); });
    deny.addEventListener("click", () => { send("agent.approve", { id: ev.id, allow: false }); finish(); });
    actions.append(allow, deny);
    card.append(text, actions);
    const chat = $("agent-chat");
    chat.appendChild(card);
    chat.scrollTop = chat.scrollHeight;
  }

  /* ------------------------------------------------------------------ *
   * menus / toolbar
   * ------------------------------------------------------------------ */
  function syncToolbar() {
    // Terminal view toggles (自动滚动 / 时间戳 / HEX / 本地回显) live in the
    // 终端 menu; their check state is refreshed by updateMenuState().
    updatePill();
    updateMenuState();
  }
  function updatePill() {
    const pill = $("pill-active");
    let text = "无活动会话";
    let ok = false;
    const ds = state.activeTab && state.activeTab !== "agent" ? state.devices.get(state.activeTab) : null;
    if (state.activeTab === "agent") {
      const mode = state.agent.mode === "ai" ? "AI" : "Normal";
      const target = state.agent.target === "local" ? "Local" : "Remote";
      text = `Agent · ${mode} · ${target}`;
      ok = state.agent.mode === "ai";
    } else if (ds) {
      text = (ds.kind === "serial" ? "串口" : "SSH") + " · " + shortPort(ds.label) + (ds.connected ? "" : " · 未连接");
      ok = ds.connected;
    }
    pill.textContent = text;
    pill.classList.toggle("ok", ok);
    $("status-sessions").textContent = "会话：" + state.devices.size;
  }
  function setupMenus() {
    document.querySelectorAll(".menu").forEach((root) => {
      root.querySelector(".menu-btn").addEventListener("click", (e) => {
        e.stopPropagation();
        if (root.classList.contains("open")) closeMenus();
        else openMenu(root);
      });
      root.addEventListener("mouseenter", () => {
        if (document.querySelector(".menu.open")) openMenu(root);
      });
    });
    document.querySelectorAll(".mi[data-action]").forEach((mi) => {
      mi.addEventListener("click", (e) => {
        e.stopPropagation();
        closeMenus();
        runAction(mi.dataset.action);
      });
    });
    document.addEventListener("click", closeMenus);
  }
  function openMenu(root) {
    document.querySelectorAll(".menu").forEach((m) => m.classList.toggle("open", m === root));
  }
  function closeMenus() {
    document.querySelectorAll(".menu").forEach((m) => m.classList.remove("open"));
  }
  function setChecked(action, on) {
    const mi = document.querySelector(`.mi[data-action="${action}"]`);
    if (mi) mi.classList.toggle("checked", !!on);
  }
  function updateMenuState() {
    const ds = state.activeTab && state.activeTab !== "agent" ? state.devices.get(state.activeTab) : null;
    const cv = ds ? ds.console : null;
    setChecked("toggle-auto", cv ? cv.auto : false);
    setChecked("toggle-ts", cv ? cv.ts : false);
    setChecked("toggle-hex", cv ? cv.hex : false);
    setChecked("toggle-echo", state.localEcho);
    setChecked("toggle-sidebar", state.sidebar);
    setChecked("toggle-workspace", state.ws.visible);
  }
  function runAction(action) {
    const ds = state.activeTab && state.activeTab !== "agent" ? state.devices.get(state.activeTab) : null;
    const cv = ds ? ds.console : null;
    switch (action) {
      case "new-session": showNewSession("serial"); break;
      case "open-agent": openAgentTab(); break;
      case "close-current":
        if (state.activeTab === "agent") closeAgentTab();
        else if (ds) closeDeviceTab(ds.id);
        break;
      case "close-all":
        for (const d of Array.from(state.devices.values())) {
          if (d.tabOpen) closeDeviceTab(d.id);
        }
        closeAgentTab();
        break;
      case "toggle-auto": if (cv) { cv.auto = !cv.auto; if (cv.auto) cv.scrollBottom(); } break;
      case "toggle-ts": if (cv) { cv.ts = !cv.ts; cv.rerender(); } break;
      case "toggle-hex": if (cv) { cv.hex = !cv.hex; cv.rerender(); } break;
      case "toggle-echo": state.localEcho = !state.localEcho; break;
      case "clear":
        if (state.activeTab === "agent") { $("agent-chat").textContent = ""; agentCards.clear(); }
        else if (cv) cv.clear();
        break;
      case "copy": copySelection(); break;
      case "paste": pasteClipboard(); break;
      case "refresh-ports": send("serial.list"); showNewSession("serial"); break;
      case "agent-inspect": agentAsk("巡检设备状态"); break;
      case "agent-logs": agentAsk("查看系统日志"); break;
      case "toggle-sidebar": state.sidebar = !state.sidebar; $("sessions-pane").classList.toggle("hidden", !state.sidebar); break;
      case "toggle-workspace": state.ws.visible = !state.ws.visible; $("workspace").classList.toggle("hidden", !state.ws.visible); $("ws-sides").classList.toggle("hidden", !state.ws.visible); break;
      case "font-inc": setFont(state.fontScale + 0.1); break;
      case "font-dec": setFont(state.fontScale - 0.1); break;
      case "font-reset": setFont(1); break;
      case "about": showAbout(); break;
      default: break;
    }
    updateMenuState();
  }
  function setFont(scale) {
    state.fontScale = Math.min(1.6, Math.max(0.8, Math.round(scale * 10) / 10));
    document.documentElement.style.setProperty("--term-font-size", (12.5 * state.fontScale).toFixed(1) + "px");
    const ds = focusedDevice();
    if (ds && ds.kind === "ssh" && ds.shell) {
      const { cols, rows } = termSize(ds.termEl);
      send("ssh.shell.resize", { cols, rows }, ds.id);
    }
  }

  /* ------------------------------------------------------------------ *
   * new-session dialog
   * ------------------------------------------------------------------ */
  let newKind = "serial";
  function showNewSession(kind) {
    setNewKind(kind || "serial");
    $("session-modal").classList.add("show");
    if (newKind === "serial") send("serial.list");
    else $("in-host").focus();
  }
  function hideNewSession() {
    $("session-modal").classList.remove("show");
  }
  function setNewKind(kind) {
    newKind = kind === "ssh" ? "ssh" : "serial";
    document.querySelectorAll("#new-kind button").forEach((b) => {
      b.classList.toggle("active", b.dataset.kind === newKind);
    });
    $("new-serial-form").classList.toggle("hidden", newKind !== "serial");
    $("new-ssh-form").classList.toggle("hidden", newKind !== "ssh");
  }
  function createFromDialog() {
    if (newKind === "serial") {
      const port = $("sel-port").value;
      if (!port) { toast("请先选择串口设备"); return; }
      send("serial.open", {
        port,
        baud: parseInt($("in-baud").value, 10) || 115200,
        dataBits: parseInt($("sel-databits").value, 10) || 8,
        parity: $("sel-parity").value,
        stopBits: parseFloat($("sel-stopbits").value) || 1,
      });
    } else {
      const auth = $("sel-auth").value;
      send("ssh.connect", {
        host: $("in-host").value.trim(),
        port: parseInt($("in-port").value, 10) || 22,
        user: $("in-user").value.trim(),
        password: auth === "password" ? $("in-password").value : "",
        privateKey: auth === "key" ? $("in-key").value : "",
        passphrase: auth === "key" ? $("in-passphrase").value : "",
      });
    }
    hideNewSession();
  }
  function setupNewSession() {
    $("btn-new-session").addEventListener("click", () => showNewSession("serial"));
    $("empty-new").addEventListener("click", () => showNewSession("serial"));
    $("session-modal-close").addEventListener("click", hideNewSession);
    $("btn-new-cancel").addEventListener("click", hideNewSession);
    $("session-modal").addEventListener("click", (e) => { if (e.target === $("session-modal")) hideNewSession(); });
    document.querySelectorAll("#new-kind button").forEach((b) => {
      b.addEventListener("click", () => setNewKind(b.dataset.kind));
    });
    $("btn-refresh").addEventListener("click", () => send("serial.list"));
    $("btn-new-connect").addEventListener("click", createFromDialog);
    $("sel-auth").addEventListener("change", (e) => {
      const isKey = e.target.value === "key";
      $("field-password").classList.toggle("hidden", isKey);
      $("field-key").classList.toggle("hidden", !isKey);
    });
    $("btn-device-toggle").addEventListener("click", () => {
      const ds = focusedDevice();
      if (ds) closeDeviceSession(ds.id);
    });
    $("btn-device-shell").addEventListener("click", () => {
      const ds = focusedDevice();
      if (ds) toggleShell(ds);
    });
    $("btn-device-clear").addEventListener("click", () => {
      const ds = focusedDevice();
      if (ds) ds.console.clear();
    });
  }

  /* ------------------------------------------------------------------ *
   * modal / toast
   * ------------------------------------------------------------------ */
  function showModal(title, html) {
    $("modal-title").textContent = title;
    $("modal-body").innerHTML = html;
    $("modal").classList.add("show");
  }
  function closeModal() { $("modal").classList.remove("show"); }
  function askText(title, placeholder) {
    return new Promise((resolve) => {
      showModal(title, `<div class="modal-form">
        <input id="modal-input" placeholder="${esc(placeholder)}" autocomplete="off">
        <div class="modal-actions">
          <button class="btn" id="modal-cancel">取消</button>
          <button class="btn primary" id="modal-ok">确定</button>
        </div>
      </div>`);
      const input = $("modal-input");
      input.focus();
      const done = (v) => { closeModal(); resolve(v); };
      $("modal-ok").addEventListener("click", () => done(input.value.trim() || null));
      $("modal-cancel").addEventListener("click", () => done(null));
      input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); done(input.value.trim() || null); }
      });
    });
  }
  function confirmDialog(title, message) {
    return new Promise((resolve) => {
      showModal(title, `<p>${esc(message)}</p>
        <div class="modal-actions">
          <button class="btn" id="modal-cancel">取消</button>
          <button class="btn primary" id="modal-ok">确定</button>
        </div>`);
      $("modal-ok").addEventListener("click", () => { closeModal(); resolve(true); });
      $("modal-cancel").addEventListener("click", () => { closeModal(); resolve(false); });
    });
  }

  function editModal(title, text) {
    return new Promise((resolve) => {
      showModal(title, `<div class="modal-form">
        <textarea id="modal-editor" rows="16" spellcheck="false">${esc(text)}</textarea>
        <div class="modal-actions">
          <button class="btn" id="modal-cancel">取消</button>
          <button class="btn primary" id="modal-ok">保存</button>
        </div>
      </div>`);
      const ta = $("modal-editor");
      ta.focus();
      const done = (v) => { closeModal(); resolve(v); };
      $("modal-ok").addEventListener("click", () => done(ta.value));
      $("modal-cancel").addEventListener("click", () => done(null));
    });
  }

  // Installed kits, contributed by the host (phase-1 Kit manifest plumbing).
  function kitsSection() {
    const kits = state.kits.kits || [];
    const tools = state.kits.tools || [];
    if (!kits.length) return "";
    const mutating = tools.filter((t) => t.risk !== "read").length;
    const items = kits
      .map((k) => `<li><b>${esc(k.name)}</b> <span class="muted">v${esc(k.version)}${k.license ? " · " + esc(k.license) : ""} — ${esc(k.description || "")}</span></li>`)
      .join("");
    return `<p class="muted">已加载 ${kits.length} 个 Kit，共 ${tools.length} 个工具（${tools.length - mutating} 只读 / ${mutating} 修改）：</p><ul>${items}</ul>`;
  }

  function showAbout() {
    showModal("关于 EdgeKit", `
      <h4>EdgeKit</h4>
      <p>AI 加速端侧设备升级。</p>
      <p class="muted">以 Agent 会话为中枢，把端侧设备调试与升级所需手段整合到一个原生桌面应用：</p>
      <ul>
        <li>多设备会话：同时连接多块板的串口 / SSH（最多几块），标签切换</li>
        <li>Agent 会话：自然语言驱动串口 / SSH / 工作区，含安全审批</li>
        <li>工作区：本地 / 远端（SFTP）文件浏览与上传下载，串口模式尝试列出设备目录</li>
      </ul>
      ${kitsSection()}
      <p class="muted">许可证：Apache License 2.0（详见仓库 LICENSE 文件）。</p>
      <p class="muted">后续将持续演进：AI 辅助诊断、设备自动发现、批量运维与智能编排。</p>
    `);
  }
  let toastTimer = null;
  function toast(message) {
    const el = $("toast");
    el.textContent = message;
    el.classList.add("show");
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove("show"), 4000);
  }

  /* ------------------------------------------------------------------ *
   * persisted settings (backend JSON file)
   * ------------------------------------------------------------------ */
  const PERSIST_FIELDS = [
    "in-host", "in-port", "in-user", "sel-auth", "in-key", "in-passphrase",
    "in-baud", "sel-databits", "sel-stopbits", "sel-parity", "sel-port",
    "in-agent-base", "in-agent-model",
  ];
  function collectSettings() {
    const data = {};
    for (const id of PERSIST_FIELDS) {
      const el = $(id);
      if (el) data[id] = el.value;
    }
    data["in-agent-key"] = $("in-agent-key").value;
    data["chk-agent-auto"] = $("chk-agent-auto").checked;
    data["agent-target"] = state.agent.target;
    data["new-kind"] = newKind;
    return data;
  }
  function persistSettings() { send("settings.set", collectSettings()); }
  function applySettings(data) {
    if (!data) return;
    for (const id of PERSIST_FIELDS) {
      const el = $(id);
      if (el && data[id] !== undefined) el.value = data[id];
    }
    if (data["chk-agent-auto"] !== undefined) $("chk-agent-auto").checked = !!data["chk-agent-auto"];
    if (data["in-agent-key"]) $("in-agent-key").value = data["in-agent-key"];
    if (data["sel-auth"] === "key") $("sel-auth").dispatchEvent(new Event("change"));
    if (data["new-kind"]) setNewKind(data["new-kind"]);

    state.agent.baseUrl = $("in-agent-base").value.trim();
    state.agent.apiKey = $("in-agent-key").value.trim();
    state.agent.model = $("in-agent-model").value.trim();
    state.agent.autoRun = $("chk-agent-auto").checked;
    state.agent.target = data["agent-target"] === "local" ? "local" : "remote";
    setAgentMode();
    pushAgentConfig();
  }
  function wirePersist() {
    for (const id of PERSIST_FIELDS) {
      const el = $(id);
      if (el) el.addEventListener("change", persistSettings);
    }
    $("in-agent-key").addEventListener("change", persistSettings);
    $("chk-agent-auto").addEventListener("change", persistSettings);
  }

  /* ------------------------------------------------------------------ *
   * workspace toolbar
   * ------------------------------------------------------------------ */
  function setupWorkspace() {
    $("ws-up").addEventListener("click", wsUp);
    $("ws-refresh").addEventListener("click", wsRefresh);
    $("ws-mkdir").addEventListener("click", wsMkdir);
    $("ws-newfile").addEventListener("click", wsNewFile);
    $("ws-upload").addEventListener("click", wsUpload);
    $("ws-download").addEventListener("click", wsDownload);
    $("ws-delete").addEventListener("click", wsDelete);
    $("ws-edit").addEventListener("click", wsEdit);
    $("ws-file").addEventListener("change", (e) => {
      const file = e.target.files && e.target.files[0];
      if (file) wsUploadFile(file);
      e.target.value = "";
    });
  }

  /* ------------------------------------------------------------------ *
   * misc wiring + init
   * ------------------------------------------------------------------ */
  function setupAgent() {
    $("btn-agent-send").addEventListener("click", agentSend);
    $("agent-input").addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); agentSend(); }
    });
    $("agent-input").addEventListener("input", (e) => {
      e.target.style.height = "auto";
      e.target.style.height = Math.min(120, e.target.scrollHeight) + "px";
    });
    $("btn-agent-stop").addEventListener("click", () => send("agent.cancel"));
    $("btn-agent-reset").addEventListener("click", () => {
      $("agent-chat").textContent = "";
      agentCards.clear();
      send("agent.reset");
    });
    $("btn-agent-save").addEventListener("click", saveAgentConfig);
    document.querySelectorAll("[data-agent]").forEach((b) => {
      b.addEventListener("click", () => agentAsk(b.dataset.agent));
    });
    document.querySelectorAll("#agent-target button").forEach((b) => {
      b.addEventListener("click", () => setAgentTarget(b.dataset.target));
    });
    $("sess-agent").addEventListener("click", openAgentTab);
    $("tab-agent").addEventListener("click", openAgentTab);
    $("tab-agent").querySelector(".tab-x").addEventListener("click", (e) => {
      e.stopPropagation();
      closeAgentTab();
    });
    window.addEventListener("resize", () => {
      const ds = focusedDevice();
      if (ds && ds.kind === "ssh" && ds.shell) {
        const { cols, rows } = termSize(ds.termEl);
        send("ssh.shell.resize", { cols, rows }, ds.id);
      }
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") closeModal();
    });
    $("modal-close").addEventListener("click", closeModal);
    $("modal").addEventListener("click", (e) => { if (e.target === $("modal")) closeModal(); });
  }

  setupMenus();
  setupNewSession();
  setupWorkspace();
  setupAgent();
  wirePersist();
  setFont(1);
  setNewKind("serial");
  updateTargetSeg();
  showEmpty();
  connect();
})();
