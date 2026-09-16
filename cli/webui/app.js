"use strict";

const token = document.querySelector('meta[name="httpcapture-token"]').content;
const state = {
  sessions: [], selected: null, page: 1, pageSize: 100, total: 0, requestId: null,
  selectedSessionIds: new Set(), selectedRequestIds: new Map(),
  appliedSessionFilter: {}, appliedRequestFilter: {},
  appliedSessionParams: new URLSearchParams(), appliedRequestParams: new URLSearchParams(),
  livePaused: false, liveConnected: false, liveRefreshPending: false, liveRefreshBusy: false,
  newMatchingRequests: 0, currentDetail: null
};
const byId = id => document.getElementById(id);

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.method && options.method !== "GET") headers.set("X-HttpCapture-Token", token);
  const response = await fetch(path, { ...options, headers });
  if (!response.ok) {
    let message = "HTTP " + response.status;
    try { message = (await response.json()).error || message; } catch (_) {}
    throw new Error(message);
  }
  return response.json();
}

function paramsFromForm(form) {
  const params = new URLSearchParams();
  for (const [key, value] of new FormData(form)) {
    const normalized = String(value).trim();
    if (normalized) params.set(key, normalized);
  }
  return params;
}

function datetimeMillis(value) { return value ? String(new Date(value).getTime()) : ""; }
function setStatus(message, error = false) {
  byId("status").textContent = message;
  byId("status").classList.toggle("error", error);
}

async function loadSessions(keepSelection = true, useApplied = false, quiet = false) {
  if (!quiet) setStatus("正在读取本地会话…");
  const params = useApplied ? new URLSearchParams(state.appliedSessionParams) : paramsFromForm(byId("session-filters"));
  const appliedFilter = useApplied ? state.appliedSessionFilter : sessionExportFilter();
  const from = params.get("from"), to = params.get("to");
  params.delete("from"); params.delete("to");
  if (from) params.set("fromMs", datetimeMillis(from));
  if (to) params.set("toMs", datetimeMillis(to));
  try {
    const result = await api("/api/sessions?" + params);
    state.sessions = result.items || [];
    state.appliedSessionFilter = appliedFilter;
    if (!useApplied) state.appliedSessionParams = new URLSearchParams(params);
    renderSessions();
    setStatus("已载入 " + state.sessions.length + " 个会话");
    if (keepSelection && state.selected) {
      const refreshed = state.sessions.find(item => item.id === state.selected.id);
      if (refreshed) { state.selected = refreshed; renderSelectedSession(); } else { clearSelection(); }
    }
  } catch (error) { setStatus(error.message, true); }
}

function renderSessions() {
  const container = byId("sessions");
  container.replaceChildren();
  byId("session-count").textContent = state.sessions.length;
  byId("selected-session-count").textContent = state.selectedSessionIds.size;
  for (const session of state.sessions) {
    const wrapper = document.createElement("div");
    wrapper.className = "session-row";
    if (session.engine === "proxify") {
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.className = "session-check";
      checkbox.title = "选择会话用于结构化导出";
      checkbox.checked = state.selectedSessionIds.has(session.id);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) state.selectedSessionIds.add(session.id);
        else state.selectedSessionIds.delete(session.id);
        byId("selected-session-count").textContent = state.selectedSessionIds.size;
      });
      wrapper.append(checkbox);
    }
    const button = document.createElement("button");
    button.type = "button";
    button.className = "session-card";
    if (state.selected && state.selected.id === session.id) button.classList.add("active");
    const title = document.createElement("strong");
    title.textContent = session.packages.length ? session.packages.join(", ") : session.captureId;
    const info = document.createElement("small");
    info.textContent = formatTime(session.startTimeMillis) + " · " + session.indexedRequestCount + " 条";
    const source = document.createElement("small");
    source.textContent = session.engine + (session.deviceName ? " · " + session.deviceName : "");
    button.append(title, info, source);
    button.addEventListener("click", () => selectSession(session));
    wrapper.append(button);
    container.append(wrapper);
  }
  if (!state.sessions.length) {
    const empty = document.createElement("p");
    empty.className = "body-note";
    empty.textContent = "没有找到符合条件的本地会话。";
    container.append(empty);
  }
}

function selectSession(session) {
  state.selected = session;
  state.page = 1;
  state.total = 0;
  state.requestId = null;
  state.newMatchingRequests = 0;
  renderSessions();
  renderSelectedSession();
  closeDetail();
  loadRequests();
}

function renderSelectedSession() {
  if (!state.selected) return;
  byId("empty-state").hidden = true;
  byId("session-view").hidden = false;
  byId("selected-title").textContent = state.selected.packages.join(", ") || state.selected.captureId;
  byId("selected-meta").textContent = state.selected.engine + " · " + formatTime(state.selected.startTimeMillis) + " · " + (state.selected.status || "unknown");
  const har = byId("export-har");
  har.hidden = !state.selected.hasHar;
  har.href = "/api/sessions/" + encodeURIComponent(state.selected.id) + "/har";
  byId("export-session").disabled = state.selected.engine !== "proxify";
  byId("export-filtered-requests").disabled = state.selected.engine !== "proxify";
}

function clearSelection() {
  state.selected = null;
  state.requestId = null;
  state.total = 0;
  state.newMatchingRequests = 0;
  byId("empty-state").hidden = false;
  byId("session-view").hidden = true;
  closeDetail();
  renderSessions();
}

async function loadRequests(useApplied = false, quiet = false) {
  if (!state.selected) return;
  const params = useApplied ? new URLSearchParams(state.appliedRequestParams) : paramsFromForm(byId("request-filters"));
  const appliedFilter = useApplied ? state.appliedRequestFilter : requestExportFilter();
  params.set("page", state.page);
  params.set("pageSize", state.pageSize);
  if (!quiet) setStatus("正在查询请求…");
  try {
    const path = "/api/sessions/" + encodeURIComponent(state.selected.id) + "/requests?" + params;
    const result = await api(path);
    const previousTotal = state.total;
    if (!useApplied || result.total < previousTotal) state.newMatchingRequests = 0;
    else if (result.total > previousTotal &&
        (state.page > 1 || state.appliedRequestFilter.SortAscending || !byId("detail-panel").hidden)) {
      state.newMatchingRequests += result.total - previousTotal;
    }
    state.total = result.total;
    state.page = result.page;
    state.appliedRequestFilter = appliedFilter;
    if (!useApplied) { params.delete("page"); params.delete("pageSize"); state.appliedRequestParams = new URLSearchParams(params); }
    renderRequests(result.items || []);
    renderPager();
    setStatus("当前会话 " + result.total + " 条匹配请求");
  } catch (error) { setStatus(error.message, true); }
}

function renderRequests(items) {
  const body = byId("requests");
  body.replaceChildren();
  const selected = state.selectedRequestIds.get(state.selected.id) || new Set();
  for (const item of items) {
    const row = document.createElement("tr");
    row.className = "request-row";
    if (item.id === state.requestId) row.classList.add("active");
    const selectCell = document.createElement("td");
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.title = "选择请求用于结构化导出";
    checkbox.checked = selected.has(item.id);
    checkbox.addEventListener("click", event => event.stopPropagation());
    checkbox.addEventListener("change", () => {
      let set = state.selectedRequestIds.get(state.selected.id);
      if (!set) { set = new Set(); state.selectedRequestIds.set(state.selected.id, set); }
      if (checkbox.checked) set.add(item.id); else set.delete(item.id);
      if (!set.size) state.selectedRequestIds.delete(state.selected.id);
      renderSelectedRequestCount();
    });
    selectCell.append(checkbox);
    row.append(selectCell);
    appendCell(row, new Date(item.timestampMillis).toLocaleTimeString());
    appendCell(row, item.method, "method");
    appendCell(row, item.status, statusClass(item.status));
    appendCell(row, item.host + item.path);
    appendCell(row, item.contentType || "—");
    appendCell(row, item.durationMillis + " ms");
    appendCell(row, formatBytes(item.totalSize));
    row.addEventListener("click", () => openDetail(item.id));
    body.append(row);
  }
  if (!items.length) {
    const row = document.createElement("tr"), cell = document.createElement("td");
    cell.colSpan = 8; cell.className = "body-note"; cell.textContent = "没有匹配请求";
    row.append(cell); body.append(row);
  }
  renderSelectedRequestCount();
}

function renderSelectedRequestCount() {
  let count = 0;
  for (const ids of state.selectedRequestIds.values()) count += ids.size;
  byId("selected-request-count").textContent = count;
}

function appendCell(row, value, className = "") {
  const cell = document.createElement("td");
  cell.textContent = value;
  if (className) cell.className = className;
  cell.title = String(value);
  row.append(cell);
}

function renderPager() {
  const pages = Math.max(1, Math.ceil(state.total / state.pageSize));
  byId("request-count").textContent = state.total + " 条请求";
  byId("page-label").textContent = state.page + " / " + pages;
  byId("previous-page").disabled = state.page <= 1;
  byId("next-page").disabled = state.page >= pages;
  const hint = byId("new-request-hint");
  hint.hidden = state.newMatchingRequests <= 0;
  hint.textContent = state.newMatchingRequests > 0 ? " · 有 " + state.newMatchingRequests + " 条新匹配请求" : "";
}

async function openDetail(requestId) {
  state.requestId = requestId;
  try {
    const path = "/api/sessions/" + encodeURIComponent(state.selected.id) + "/requests/" + requestId;
    const detail = await api(path);
    state.currentDetail = detail;
    document.querySelector(".workspace").classList.add("detail-open");
    byId("detail-panel").hidden = false;
    byId("detail-method").textContent = detail.method;
    byId("detail-status").textContent = detail.statusCode + " " + (detail.status || "");
    byId("detail-status").className = statusClass(detail.statusCode);
    byId("detail-url").textContent = detail.url;
    byId("detail-timing").replaceChildren(fact(formatTime(detail.timestampMillis)), fact(detail.durationMillis + " ms"), fact(detail.httpVersion || "HTTP"));
    byId("detail-query").textContent = pretty(detail.query);
    byId("request-headers").textContent = pretty(detail.requestHeaders);
    byId("response-headers").textContent = pretty(detail.responseHeaders);
    renderBody(byId("request-body"), detail.requestBody);
    renderBody(byId("response-body"), detail.responseBody);
  } catch (error) { setStatus(error.message, true); }
}

function fact(text) { const span = document.createElement("span"); span.textContent = text; return span; }

function renderBody(container, payload) {
  container.replaceChildren();
  const note = document.createElement("p"), flags = [];
  note.className = "body-note";
  flags.push(formatBytes(payload.capturedSize) + " 已保存");
  if (payload.declaredSize > payload.capturedSize) flags.push("声明 " + formatBytes(payload.declaredSize));
  if (payload.truncated) flags.push("采集时已截断");
  if (payload.displayTruncated) flags.push("页面预览已截断");
  if (payload.readError) flags.push(payload.readError);
  note.textContent = flags.join(" · ");
  container.append(note);
  if (payload.data) {
    const pre = document.createElement("pre"); pre.textContent = payload.data; container.append(pre);
  } else if (payload.encoding === "base64") {
    const binary = document.createElement("p"); binary.textContent = "二进制 Body 不在页面内展开。"; container.append(binary);
  }
  if (payload.downloadUrl) {
    const link = document.createElement("a");
    link.className = "button"; link.href = payload.downloadUrl + "?download=1"; link.textContent = "下载已保存 Body";
    container.append(link);
  }
}

async function copyCurrentRequestAsCurl() {
  if (!state.currentDetail) return;
  const result = curlFromRequestDetail(state.currentDetail);
  try {
    await copyText(result.command);
    const suffix = result.warnings.length ? "；" + result.warnings.join("；") : "";
    setStatus("已复制 cURL" + suffix);
  } catch (error) {
    setStatus("复制失败: " + error.message, true);
  }
}

function curlFromRequestDetail(detail) {
  const warnings = [];
  const method = String(detail.method || "GET").toUpperCase();
  const parts = ["curl", shellQuote(detail.url || "")];
  if (method !== "GET" || requestBodyCanBeCopied(detail.requestBody)) {
    parts.push("-X", shellQuote(method));
  }
  const headers = detail.requestHeaders || {};
  for (const name of Object.keys(headers).sort((left, right) => left.localeCompare(right))) {
    if (!name || name.toLowerCase() === "content-length") continue;
    const values = Array.isArray(headers[name]) ? headers[name] : [headers[name]];
    for (const value of values) {
      if (value === undefined || value === null) continue;
      parts.push("-H", shellQuote(name + ": " + String(value)));
    }
  }
  if (requestBodyCanBeCopied(detail.requestBody)) {
    parts.push("--data-raw", shellQuote(detail.requestBody.data || ""));
  } else if (requestBodyWasCaptured(detail.requestBody)) {
    warnings.push("请求体不是完整文本，未写入 cURL");
  }
  return { command: wrapCurlParts(parts), warnings };
}

function requestBodyCanBeCopied(payload) {
  return payload && payload.encoding === "utf8" && !payload.truncated && !payload.displayTruncated &&
    !payload.readError && Number(payload.capturedSize || 0) > 0 && typeof payload.data === "string";
}

function requestBodyWasCaptured(payload) {
  return payload && Number(payload.capturedSize || 0) > 0;
}

function shellQuote(value) {
  return "'" + String(value).replace(/'/g, "'\\''") + "'";
}

function wrapCurlParts(parts) {
  if (parts.length <= 2) return parts.join(" ");
  const lines = [parts[0] + " " + parts[1]];
  for (let index = 2; index < parts.length; index += 2) {
    lines.push("  " + parts[index] + (parts[index + 1] ? " " + parts[index + 1] : ""));
  }
  return lines.join(" \\\n");
}

async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.left = "-9999px";
  document.body.append(area);
  area.select();
  try {
    if (!document.execCommand("copy")) throw new Error("浏览器拒绝复制");
  } finally {
    area.remove();
  }
}

function closeDetail() {
  state.currentDetail = null;
  byId("detail-panel").hidden = true;
  document.querySelector(".workspace").classList.remove("detail-open");
}
function pretty(value) { return JSON.stringify(value || {}, null, 2); }
function formatTime(milliseconds) { return milliseconds ? new Date(milliseconds).toLocaleString() : "—"; }
function formatBytes(value) {
  const number = Number(value || 0);
  if (number < 1024) return number + " B";
  if (number < 1048576) return (number / 1024).toFixed(1) + " KB";
  return (number / 1048576).toFixed(1) + " MB";
}
function statusClass(status) {
  if (status >= 500) return "status-error";
  if (status >= 400) return "status-warn";
  return "status-ok";
}

byId("session-filters").addEventListener("submit", event => { event.preventDefault(); loadSessions(false); });
byId("request-filters").addEventListener("submit", event => { event.preventDefault(); state.page = 1; loadRequests(); });
byId("previous-page").addEventListener("click", () => { if (state.page > 1) { state.page--; loadRequests(); } });
byId("next-page").addEventListener("click", () => { if (state.page * state.pageSize < state.total) { state.page++; loadRequests(); } });
byId("close-detail").addEventListener("click", closeDetail);
byId("copy-curl").addEventListener("click", copyCurrentRequestAsCurl);
byId("rescan").addEventListener("click", async () => {
  try { setStatus("正在重新扫描…"); await api("/api/rescan", { method: "POST" }); await loadSessions(true); if (state.selected) await loadRequests(); }
  catch (error) { setStatus(error.message, true); }
});
byId("open-folder").addEventListener("click", async () => {
  if (!state.selected) return;
  try { await api("/api/sessions/" + encodeURIComponent(state.selected.id) + "/open", { method: "POST" }); }
  catch (error) { setStatus(error.message, true); }
});
byId("trash-session").addEventListener("click", async () => {
  if (!state.selected || !confirm("将会话移到 .trash？原始文件不会永久删除。")) return;
  try {
    await api("/api/sessions/" + encodeURIComponent(state.selected.id), { method: "DELETE" });
    state.selectedSessionIds.delete(state.selected.id);
    state.selectedRequestIds.delete(state.selected.id);
    clearSelection(); await loadSessions(false);
  } catch (error) { setStatus(error.message, true); }
});

function sessionExportFilter() {
  const params = paramsFromForm(byId("session-filters"));
  const from = params.get("from"), to = params.get("to");
  return {
    engine: params.get("engine") || "",
    package: params.get("package") || "",
    device: params.get("device") || "",
    fromMs: from ? Number(datetimeMillis(from)) : 0,
    toMs: to ? Number(datetimeMillis(to)) : 0
  };
}

function requestExportFilter() {
  const params = paramsFromForm(byId("request-filters"));
  return {
    Search: params.get("q") || "", Method: params.get("method") || "",
    Status: params.get("status") || "", ContentType: params.get("contentType") || "",
    MinDurationMS: Number(params.get("minDurationMs") || 0),
    MaxDurationMS: Number(params.get("maxDurationMs") || 0),
    MinSize: Number(params.get("minSize") || 0),
    MaxSize: Number(params.get("maxSize") || 0),
    SortAscending: params.get("sort") === "asc"
  };
}

let pendingExportSpec = null;
function submitExport(spec) {
  if (spec.scope === "selected" && !(spec.sessionIds || []).length && !(spec.requestIds || []).length) {
    setStatus("请先勾选一个或多个会话/请求。", true);
    return;
  }
  pendingExportSpec = spec;
  byId("export-confirm").showModal();
}

function startExport(spec) {
  let frame = byId("export-download-frame");
  if (!frame) {
    frame = document.createElement("iframe");
    frame.id = "export-download-frame";
    frame.name = "export-download-frame";
    frame.hidden = true;
    document.body.append(frame);
  }
  const form = document.createElement("form");
  form.method = "POST";
  form.action = "/api/export";
  form.target = frame.name;
  for (const [name, value] of [["token", token], ["spec", JSON.stringify(spec)]]) {
    const input = document.createElement("input");
    input.type = "hidden"; input.name = name; input.value = value; form.append(input);
  }
  document.body.append(form);
  form.submit();
  form.remove();
  setStatus("已提交结构化 ZIP 导出，请查看浏览器下载状态。仅支持 Proxify 逐请求数据。");
}

byId("export-confirm-cancel").addEventListener("click", () => {
  pendingExportSpec = null;
  byId("export-confirm").close();
});
byId("export-confirm-ok").addEventListener("click", () => {
  const spec = pendingExportSpec;
  pendingExportSpec = null;
  byId("export-confirm").close();
  if (spec) startExport(spec);
});

byId("export-all").addEventListener("click", () => submitExport({
  scope: "all", sessionFilter: {}, requestFilter: {}
}));
byId("export-filtered-sessions").addEventListener("click", () => submitExport({
  scope: "filtered", sessionFilter: state.appliedSessionFilter, requestFilter: {}
}));
byId("export-session").addEventListener("click", () => {
  if (state.selected) submitExport({ scope: "all", sessionId: state.selected.id, sessionFilter: {}, requestFilter: {} });
});
byId("export-filtered-requests").addEventListener("click", () => {
  if (state.selected) submitExport({
    scope: "filtered", sessionId: state.selected.id,
    sessionFilter: {}, requestFilter: state.appliedRequestFilter
  });
});
byId("export-selected-sessions").addEventListener("click", () => {
  const selectedRequests = [];
  for (const [sessionId, ids] of state.selectedRequestIds) {
    for (const id of ids) selectedRequests.push({ sessionId, id });
  }
  submitExport({
    scope: "selected", sessionFilter: {}, requestFilter: {},
    sessionIds: [...state.selectedSessionIds], requestIds: selectedRequests
  });
});

function setLiveStatus(message, kind = "") {
  const node = byId("live-status");
  node.textContent = message;
  node.classList.toggle("interrupted", kind === "interrupted");
  node.classList.toggle("paused", kind === "paused");
}

async function refreshLiveView() {
  if (state.livePaused) return;
  if (state.liveRefreshBusy) { state.liveRefreshPending = true; return; }
  state.liveRefreshBusy = true;
  try {
    await loadSessions(true, true, true);
    if (state.selected) await loadRequests(true, true);
  } finally {
    state.liveRefreshBusy = false;
    if (state.liveRefreshPending && !state.livePaused) {
      state.liveRefreshPending = false;
      scheduleLiveRefresh();
    }
  }
}

function scheduleLiveRefresh() {
  if (state.livePaused || state.liveRefreshPending) return;
  state.liveRefreshPending = true;
  setTimeout(() => {
    state.liveRefreshPending = false;
    refreshLiveView();
  }, 120);
}

byId("live-toggle").addEventListener("click", () => {
  state.livePaused = !state.livePaused;
  byId("live-toggle").textContent = state.livePaused ? "继续页面更新" : "暂停页面更新";
  if (state.livePaused) setLiveStatus("页面更新已暂停", "paused");
  else { setLiveStatus(state.liveConnected ? "实时更新中" : "实时连接中…"); scheduleLiveRefresh(); }
});

function startLive() {
  const stream = new EventSource("/api/events");
  const watchdog = setTimeout(() => {
    if (!state.liveConnected && !state.livePaused) setLiveStatus("实时连接超时 · 可手动重新扫描", "interrupted");
  }, 5000);
  stream.onopen = () => {
    state.liveConnected = true;
    clearTimeout(watchdog);
    if (!state.livePaused) setLiveStatus("实时更新中");
  };
  stream.onerror = () => {
    state.liveConnected = false;
    if (!state.livePaused) setLiveStatus("实时更新已中断 · 可手动重新扫描", "interrupted");
  };
  stream.addEventListener("interrupted", () => {
    if (!state.livePaused) setLiveStatus("实时更新已中断 · 可手动重新扫描", "interrupted");
  });
  stream.addEventListener("reset", event => {
    try {
      for (const sessionId of JSON.parse(event.data)) {
        state.selectedRequestIds.delete(sessionId);
        if (state.selected && state.selected.id === sessionId) {
          state.requestId = null;
          state.newMatchingRequests = 0;
          closeDetail();
        }
      }
      renderSelectedRequestCount();
      scheduleLiveRefresh();
    } catch (_) { scheduleLiveRefresh(); }
  });
  stream.addEventListener("requests", scheduleLiveRefresh);
  stream.addEventListener("refresh", scheduleLiveRefresh);
  window.addEventListener("beforeunload", () => stream.close());
}

loadSessions(false).then(startLive);
