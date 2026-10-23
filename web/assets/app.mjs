import { createClient, list, policyInput, userInput, keyInput, channelInput, ledgerMoney, ledgerAmounts as rawLedgerAmounts, resolutionInput } from "./api.mjs";

import { icon, knownCount, ownerAmounts, logSample, sampleChart, downloadCSV } from "./workspace.mjs";

const root = document.querySelector("#app");
let user = null;
let generation = 0;
let disposeView = () => {};
let noticeTimer;
let pendingPublication = false;
let fieldSequence = 0;
const api = createClient({
  onUnauthorized: () => signOut("登录已过期，请重新登录。"),
  onPending: () => {
    pendingPublication = true;
    notice("配置已保存，发布待处理。请到价格与发布页面重试发布。", true);
  },
});

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs)) {
    if (name.startsWith("on")) node.addEventListener(name.slice(2).toLowerCase(), value);
    else if (value !== false && value !== undefined && value !== null) node.setAttribute(name, String(value));
  }
  for (const child of children.flat(Infinity)) {
    if (child !== null && child !== undefined && child !== false) {
      node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    }
  }
  return node;
}
const text = (tag, value, className) => el(tag, { class: className }, value);
const button = (label, action, style = "secondary") => el("button", { type: "button", class: `button ${style}`, onClick: action }, label);
const fmt = value => Number(value || 0).toLocaleString(undefined, { maximumFractionDigits: 4 });
const badge = value => text("span", stateLabel(value), `badge ${["active", "enabled", "applied", "200"].includes(String(value)) ? "good" : ""}`);
const empty = message => text("p", message, "empty");
const actions = (...items) => el("div", { class: "actions" }, items);
const panel = (...items) => el("section", { class: "panel" }, items);
const heading = (title, subtitle, action) => el("div", { class: "section-heading" },
  el("div", {}, text("h2", title), text("p", subtitle, "muted")), action);
const ledgerAmounts = row => Object.fromEntries(Object.entries(rawLedgerAmounts(row)).map(([key, value]) => [
  key, value === "Unknown" ? "未知" : value === "Not finalized" ? "未结算" : value,
]));
const financialBadge = state => text("span", ({ reserved: "已预留", active: "执行中", pending: "待核对", settled: "已结算" })[state] || "未知状态",
  "badge " + (state === "settled" ? "good" : ""));
const listText = values => list(values).join(", ") || "None";

function notice(message, error = false) {
  clearTimeout(noticeTimer);
  let node = document.querySelector("#notice");
  if (!node) {
    node = el("div", { id: "notice", role: "status", "aria-live": "polite" });
    document.body.append(node);
  }
  node.className = error ? "notice error" : "notice";
  node.replaceChildren(text("span", message), button("关闭提示", () => node.remove(), "quiet"));
  noticeTimer = setTimeout(() => node.remove(), error ? 18000 : 6000);
}

function savedConfigurationMessage(error) {
  if (error.publication === "applied") return "配置已保存并发布，但回读或管理缓存刷新失败。请先刷新页面再编辑。";
  if (error.publication === "pending") return "配置已保存，发布待处理。请到价格与发布页面重试发布。";
  return "配置已保存，但发布状态未知。进一步操作前请先刷新状态。";
}

async function run(action) {
  try { await action(); }
  catch (error) {
    notice(error.saved ? savedConfigurationMessage(error) : error.message, true);
    if ((error.saved || error.status === 403) && user) await navigate();
  }
}

function field({ name, label, type = "text", value = "", options, hint, required = false, min, full = false }) {
  let input;
  if (options) {
    input = el("select", { name, required }, options.map(option => {
      const [key, title] = Array.isArray(option) ? option : [option, option];
      return el("option", { value: key }, key === title && ["active", "enabled", "disabled", "cooldown", "banned"].includes(key) ? stateLabel(key) : key === title && ["user", "admin"].includes(key) ? roleName(key) : title);
    }));
    input.value = value;
  } else if (type === "textarea") {
    input = el("textarea", { name, rows: 3, required });
    input.value = value;
  } else {
    input = el("input", {
      name, type, value, required, min, step: type === "number" ? "any" : undefined,
      autocomplete: type === "password" ? "new-password" : "off",
    });
  }
  input.setAttribute("aria-label", label);
  const hintID = `field-hint-${++fieldSequence}`;
  if (hint) input.setAttribute("aria-describedby", hintID);
  return el("label", { class: `field ${full ? "full" : ""}` },
    text("span", label), input, hint ? el("small", { id: hintID }, hint) : null);
}

function policyFields(value = {}) {
  return [
    { name: "model_mode", label: "模型访问策略", value: value.model_mode || "allowlist",
      options: [["allowlist", "仅允许列出的模型"], ["all", "上级策略授权范围内全部"]] },
    { name: "models", label: "允许的模型", value: listText(value.models) === "None" ? "" : listText(value.models),
      hint: "以逗号或空格分隔，空白名单拒绝所有模型。" },
  ];
}
function limitFields(value = {}) {
  return [
    { name: "quota", label: "额度（CNY）", type: "number", value: value.quota || 0, min: 0 },
    { name: "rpm_limit", label: "每分钟请求上限", type: "number", value: value.rpm_limit || 0, min: 0 },
    { name: "tpm_limit", label: "每分钟 Token 上限（估算）", type: "number", value: value.tpm_limit || 0, min: 0 },
    { name: "concurrency_limit", label: "并发请求上限", type: "number", value: value.concurrency_limit || 0, min: 0 },
  ];
}

function manageDialog(dialog, cleanup = () => {}) {
  const close = dialog.close.bind(dialog);
  let disposed = false;
  const dispose = () => {
    if (disposed) return;
    disposed = true;
    cleanup();
    dialog.remove();
  };
  // Native close events are queued; private fields must be cleared immediately.
  dialog.close = () => { close(); dispose(); };
  dialog.addEventListener("cancel", event => { event.preventDefault(); dialog.close(); });
  dialog.addEventListener("close", dispose, { once: true });
}

function editDialog(title, fields, save, description = "所有变更均由服务端校验。",
  { submitLabel = "保存更改", successMessage = "更改已保存。" } = {}) {
  const error = el("p", { class: "form-error", role: "alert" });
  const submit = el("button", { type: "submit", class: "button primary" }, submitLabel);
  const form = el("form", {},
    heading(title, description), el("div", { class: "form-grid" }, fields.map(field)), error,
    actions(button("取消", () => dialog.close()), submit));
  const dialog = el("dialog", { class: "editor", "aria-label": title }, form);
  manageDialog(dialog, () => form.reset());
  form.addEventListener("submit", async event => {
    event.preventDefault();
    if (submit.disabled) return;
    submit.disabled = true;
    error.textContent = "";
    const epoch = generation;
    pendingPublication = false;
    try {
      const result = await save(Object.fromEntries(new FormData(form)));
      if (epoch !== generation) return;
      dialog.close();
      if (!pendingPublication) notice(successMessage);
      await navigate();
      if (result?.secret && user && generation === epoch + 1) showSecret(result.secret);
    } catch (failure) {
      if (failure.saved) {
        form.reset();
        dialog.close();
        if (epoch === generation && user) {
          notice(savedConfigurationMessage(failure), true);
          await navigate();
        }
        return;
      }
      if (failure.status === 403 && epoch === generation && user) {
        dialog.close();
        notice(failure.message, true);
        await navigate();
        return;
      }
      if (dialog.isConnected) error.textContent = failure.message;
    } finally { submit.disabled = false; }
  });
  document.body.append(dialog);
  dialog.showModal();
}

function showSecret(secret) {
  const value = el("textarea", { readonly: true, rows: 3, "aria-label": "新 API Key", class: "secret" });
  value.value = secret;
  const dialog = el("dialog", { class: "editor", "aria-label": "保存你的 API Key" },
    text("p", "仅展示一次", "eyebrow"),
    text("h2", "保存你的 API Key"),
    text("p", "完整密钥仅展示这一次，请安全保存，之后无法再次读取。", "muted"),
    value, actions(button("复制密钥", () => run(async () => {
      if (!navigator.clipboard) { value.select(); notice("请手动选择并复制密钥。"); return; }
      await navigator.clipboard.writeText(value.value);
      notice("密钥已复制，使用后请清空剪贴板。");
    })), button("我已保存", () => dialog.close(), "primary")));
  manageDialog(dialog, () => { value.value = ""; });
  document.body.append(dialog);
  dialog.showModal();
}

function table(columns, rows, render) {
  if (!rows.length) return empty("当前没有记录。可创建项目或调整筛选条件。");
  return el("div", { class: "table-wrap", tabindex: "0", "aria-label": "可横向滚动的数据表格" },
    el("table", {}, el("thead", {}, el("tr", {}, columns.map(name => el("th", { scope: "col" }, name)))),
      el("tbody", {}, rows.map(row => el("tr", {}, render(row).map(cell => el("td", {}, cell)))))));
}

function login(message = "") {
  const error = el("p", { class: "form-error", role: "alert" }, message);
  const submit = el("button", { class: "button primary", type: "submit" }, "登录工作空间");
  const form = el("form", { class: "login-form" },
    text("p", "TXAPI 工作空间", "eyebrow"), text("h2", "欢迎回来"),
    text("p", "登录后管理调用凭据、模型与请求。", "muted"),
    field({ name: "username", label: "用户名", required: true }),
    field({ name: "password", label: "密码", type: "password", required: true }),
    error, submit, text("p", "账户由管理员创建，当前不开放公开注册。", "fine"));
  form.querySelector('[name="username"]').autocomplete = "username";
  form.querySelector('[name="password"]').autocomplete = "current-password";
  form.addEventListener("submit", async event => {
    event.preventDefault();
    submit.disabled = true;
    error.textContent = "";
    try {
      const response = await api.request("/api/auth/login", { method: "POST", body: Object.fromEntries(new FormData(form)) });
      api.setSession(response.session_token);
      user = response.user;
      form.reset();
      shell();
      await navigate();
    } catch (failure) { error.textContent = failure.message; }
    finally { submit.disabled = false; }
  });
  root.replaceChildren(el("main", { class: "login" },
    el("section", { class: "login-story" }, text("a", "TXAPI", "wordmark"),
      text("span", "统一 API 接入", "eyebrow"), text("h1", "你的模型，\n一个工作空间。"),
      text("p", "连接模型与应用，\n让每次调用的状态都有据可查。", "story-copy"),
      el("div", { class: "route-art", "aria-hidden": true },
        text("span", "OPENAI"), text("span", "ANTHROPIC"), text("span", "GEMINI"), text("strong", "TX / API")),
      text("p", "自托管 · 单二进制 · 数据由你管理", "story-footer")),
    el("section", { class: "login-access" }, form,
      text("p", "首次安装请在服务端使用 -init-admin 初始化管理员。", "setup-note"))));
}

function signOut(message = "") {
  generation++;
  disposeView();
  disposeView = () => {};
  api.setSession("");
  user = null;
  for (const dialog of document.querySelectorAll("dialog")) dialog.close();
  document.querySelector("#notice")?.remove();
  login(message);
}

const pages = [
  ["overview", "总览", "overview"], ["keys", "我的 API Key", "keys"], ["models", "模型目录", "models"],
  ["playground", "调用测试", "playground"], ["logs", "请求日志", "logs"], ["usage", "原生用量", "logs"],
  ["finance", "个人账本", "billing"], ["tasks", "后台任务", "tasks"], ["routing", "路由诊断", "routing"],
];
const adminPages = [
  ["users", "用户与权限", "users"], ["admin-keys", "全站 API Key", "keys"],
  ["channels", "渠道管理", "channels"], ["settings", "价格与发布", "settings"],
  ["billing", "财务核对", "billing"], ["quotation", "报价诊断", "billing"],
];
const roleName = role => role === "admin" ? "管理员" : "用户";
const stateLabel = value => ({
  active: "已启用", enabled: "已启用", disabled: "已停用", applied: "已发布", pending: "待核对",
  reserved: "已预留", settled: "已结算", queued: "等待执行", dispatching: "提交中",
  remote_active: "远端执行中", terminal: "已终止", uncertain: "未知", cooldown: "冷却中",
  banned: "不可用", open: "记录中", sealed: "已封存", interrupted: "已中断", "200": "HTTP 200",
}[String(value)] || String(value ?? "未知"));

function allowedPages() { return [...pages, ...(user?.role === "admin" ? adminPages : [])]; }
function navGroup(items, label) {
  return el("div", { class: "nav-group" }, text("p", label, "nav-label"),
    el("nav", { "aria-label": label }, items.map(([id, label, symbol]) =>
      el("a", { href: "#" + id, "data-page": id }, icon(symbol), text("span", label)))));
}
function setMenu(open) {
  const workspace = document.querySelector(".workspace");
  if (!workspace) return;
  const mobile = matchMedia("(max-width: 740px)").matches;
  workspace.classList.toggle("menu-open", mobile && open);
  document.querySelector("#menu-toggle")?.setAttribute("aria-expanded", String(mobile && open));
  const sidebar = workspace.querySelector(".sidebar");
  sidebar.inert = mobile && !open;
  workspace.querySelector(".main-column").inert = mobile && open;
  if (mobile && open) sidebar.querySelector("a, button")?.focus();
}
function pageSearch() {
  const list = el("div", { class: "command-results" });
  const input = el("input", { type: "search", "aria-label": "搜索页面", placeholder: "搜索模型、渠道、账本…" });
  const dialog = el("dialog", { class: "editor command", "aria-label": "跳转页面" },
    heading("跳转页面", "仅搜索当前角色可访问的页面", button("关闭", () => dialog.close(), "quiet")), input, list);
  function filter() {
    const matches = allowedPages().filter(([, name]) => name.toLowerCase().includes(input.value.toLowerCase()));
    list.replaceChildren(...(matches.length ? matches.map(([id, label, symbol]) =>
      button(el("span", { class: "actions" }, icon(symbol), label), () => {
        dialog.close(); location.hash = id;
      }, "command-item")) : [empty("没有匹配页面，请换一个名称。")]));
  }
  input.addEventListener("input", filter);
  manageDialog(dialog);
  filter(); document.body.append(dialog); dialog.showModal(); input.focus();
}
function shell() {
  root.replaceChildren(el("div", { class: "workspace" },
    el("aside", { class: "sidebar", id: "navigation" },
      el("div", { class: "brand-row" }, el("a", { href: "#overview", class: "wordmark" },
        text("span", "↯", "brand-mark"), "TXAPI", text("span", "console", "wordmark-sub")),
        el("button", { class: "icon-button mobile-close", type: "button", "aria-label": "关闭导航",
          onClick: () => { setMenu(false); document.querySelector("#menu-toggle")?.focus(); } }, icon("close"))),
      el("div", { class: "project-box" }, text("span", user.username.slice(0, 1).toUpperCase(), "project-avatar"),
        el("div", {}, text("strong", user.username), text("span", "个人工作空间", "block"))),
      navGroup(pages, "工作空间"),
      user.role === "admin" ? navGroup(adminPages, "管理") : null,
      el("div", { class: "sidebar-footer" }, text("span", "静态 API 中转", "fine"),
        el("div", { class: "profile" }, text("span", user.username.slice(0, 2).toUpperCase(), "avatar"),
          el("div", {}, text("strong", user.username), text("small", roleName(user.role), "block"))))),
    el("button", { class: "nav-shade", type: "button", "aria-label": "收起导航", onClick: () => setMenu(false) }),
    el("div", { class: "main-column" },
      el("header", { class: "topbar" },
        el("div", { class: "breadcrumbs" },
          el("button", { id: "menu-toggle", type: "button", class: "icon-button mobile-menu",
            "aria-label": "打开导航", "aria-controls": "navigation", "aria-expanded": "false",
            onClick: () => setMenu(true) }, icon("menu")),
          text("span", "工作空间", "breadcrumb-root"), text("span", "/", "breadcrumb-root"),
          text("span", "总览", "breadcrumb-current")),
        actions(el("button", { class: "search-trigger", type: "button", onClick: pageSearch },
          icon("search"), text("span", "搜索页面"), text("kbd", "Ctrl K")),
          text("span", user.username + " / " + roleName(user.role), "identity"),
          button("退出登录", () => run(async () => {
            try { await api.request("/api/auth/logout", { method: "POST" }); }
            finally { signOut(); }
          }), "quiet"))),
      el("main", { id: "view", class: "content" }),
      el("footer", { class: "workspace-footer" }, text("span", "TXAPI · 自托管 API 工作空间"),
        el("a", { href: "/healthz", target: "_blank", rel: "noopener" }, "实例存活检查")))));
  setMenu(false);
}
function pageHeader(name, description, action) {
  return el("div", { class: "page-header" },
    el("div", {}, text("h1", name), text("p", description, "muted")), action);
}
document.addEventListener("keydown", event => {
  if (user && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
    event.preventDefault(); setMenu(false); pageSearch();
  }
  const open = document.querySelector(".workspace.menu-open");
  if (open && event.key === "Escape") { setMenu(false); document.querySelector("#menu-toggle")?.focus(); }
  if (open && event.key === "Tab") {
    const focusable = [...open.querySelectorAll(".sidebar a, .sidebar button")].filter(n => n.getClientRects().length);
    if (event.shiftKey && document.activeElement === focusable[0]) { event.preventDefault(); focusable.at(-1)?.focus(); }
    else if (!event.shiftKey && document.activeElement === focusable.at(-1)) { event.preventDefault(); focusable[0]?.focus(); }
  }
});
window.addEventListener("resize", () => setMenu(false));

async function navigate({ refreshIdentity = true, recoverForbidden = true } = {}) {
  if (!user) return;
  const epoch = ++generation;
  for (const dialog of document.querySelectorAll("dialog")) dialog.close();
  disposeView();
  disposeView = () => {};
  try {
    if (refreshIdentity) {
      const current = await api.request("/api/me");
      if (epoch !== generation || !user) return;
      const changedRole = current.data.role !== user.role;
      user = current.data;
      if (changedRole) shell();
      else document.querySelector(".identity").textContent = `${user.username} / ${roleName(user.role)}`;
    }
  } catch (error) {
    if (epoch === generation && user) {
      document.querySelector("#view").replaceChildren(panel(text("p", error.message, "form-error"), button("重试", () => run(navigate))));
    }
    return;
  }
  const name = location.hash.slice(1) || "overview";
  const allowed = allowedPages();
  const page = allowed.find(item => item[0] === name)?.[0] || "overview";
  for (const link of document.querySelectorAll("[data-page]")) {
    if (link.dataset.page === page) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  setMenu(false);
  const crumb = document.querySelector(".breadcrumb-current");
  if (crumb) crumb.textContent = allowed.find(item => item[0] === page)?.[1] || "总览";
  const view = document.querySelector("#view");
  view.replaceChildren(text("p", "正在读取真实数据…", "empty"));
  try {
    const content = await ({ overview, keys, models: modelPage, playground, logs, users, channels, settings, tasks: backgroundTasks, routing: routingDiagnostics, billing: billingLedger, finance: ownerLedger, usage: usageRecords, "admin-keys": adminKeys, quotation: quotations })[page]();
    if (epoch === generation && user) view.replaceChildren(content);
  } catch (error) {
    if (error.status === 403 && recoverForbidden && epoch === generation && user) {
      try {
        const current = await api.request("/api/me");
        if (epoch !== generation || !user) return;
        user = current.data;
        shell();
        await navigate({ refreshIdentity: false, recoverForbidden: false });
        return;
      } catch (failure) { error = failure; }
    }
    if (epoch === generation && user) view.replaceChildren(
      pageHeader("读取失败", "服务端未完成请求，请查看原因后重试。"),
      panel(text("p", error.message, "form-error"), button("重试", () => run(navigate))));
  }
}

function moneyText(value) {
  return typeof value === "string" && /^(0|[1-9]\d*)$/.test(value) ? ledgerMoney(value) : "未知";
}
function metricCard(label, value, note) {
  return el("div", { class: "stat" }, text("span", label, "eyebrow"), text("strong", value), text("small", note, "muted"));
}
async function overview() {
  const epoch = generation;
  const controller = new AbortController();
  let observer;
  disposeView = () => { controller.abort(); observer?.disconnect(); };
  const read = path => api.request(path, { signal: controller.signal });
  const owner = user;
  const [usageResponse, keysResponse] = await Promise.all([read("/api/me/usage"), read("/api/me/keys")]);
  if (epoch !== generation || !user) return el("div");
  const extra = [
    read("/api/me/logs?limit=100&offset=0"), read("/api/me/billing/summary"),
    ...(owner.role === "admin" ? [read("/api/admin/channels")] : []),
  ];
  const [logResult, balanceResult, channelResult] = await Promise.allSettled(extra);
  if (epoch !== generation || !user) return el("div");
  const usage = usageResponse.data.usage || {};
  const rows = logResult.status === "fulfilled" ? list(logResult.value.data).slice(0, 100) : [];
  const range = el("select", { "aria-label": "样本时间范围" }, el("option", { value: "24" }, "过去 24 小时"), el("option", { value: "6" }, "过去 6 小时"));
  const chart = el("div");
  const distribution = el("div");
  let chartWidth = 600;
  function updateSample() {
    const selected = logSample(rows, Date.now(), Number(range.value));
    chart.replaceChildren(selected.count ? sampleChart(selected, chartWidth) : empty("这个保留样本在所选时间内没有请求。"),
      el("div", { class: "sample-caption" }, text("span", "仅最近 " + rows.length + " 条保留日志"),
        text("span", "范围内 " + selected.count + " 次" + (selected.unknownTime ? " · " + selected.unknownTime + " 条时间未知" : ""))),
      el("details", { class: "sample-data" }, el("summary", {}, "查看按小时样本数据"),
        table(["小时", "样本请求数"], selected.buckets, bucket => [new Date(bucket.time).toLocaleString("zh-CN"), bucket.requests])));
    distribution.replaceChildren(...(selected.models.length ? selected.models.slice(0, 5).map(m =>
      el("div", { class: "distribution-row" }, el("div", {}, text("strong", m.name), text("span", m.requests + " 次")),
        el("progress", { value: m.requests, max: selected.count, "aria-label": m.name + " 的样本请求占比" }))) :
      [empty("尚无可统计的模型样本。")]));
  }
  updateSample();
  range.addEventListener("change", updateSample);
  observer = new ResizeObserver(entries => {
    const width = Math.round(entries[0].contentRect.width);
    if (controller.signal.aborted || !width || width === chartWidth) return;
    chartWidth = width;
    updateSample();
  });
  observer.observe(chart);
  const flowPanel = panel(heading("请求趋势", "有限日志样本，按小时分布；不代表全量请求。", range),
    logResult.status === "fulfilled" ? chart : el("div", {}, text("p", "日志样本暂不可用。汇总数据仍来自独立接口。", "muted"), button("重试加载", () => run(navigate))));
  const balance = balanceResult.status === "fulfilled" ? balanceResult.value.data : null;
  const financePanel = panel(heading("我的资金", "持久账本 · 账户单位", el("a", { href: "#finance", class: "button quiet" }, "查看账本")),
    balance ? el("dl", { class: "facts" },
      text("dt", "累计已入账"), text("dd", balance.balance_known ? moneyText(balance.posted_used_units) : "未知"),
      text("dt", "当前预算占用"), text("dd", balance.balance_known ? moneyText(balance.held_units) : "未知"),
      text("dt", "待核对记录"), text("dd", knownCount(balance.pending_count))) :
      text("p", "账本暂不可用，请进入个人账本重试。", "muted"),
    text("p", "日志观测费用、最终收费与预算预留分别记账。未知用量不会视为零。", "fine"));
  const recent = panel(heading("最近请求", "当前用户最近保留的日志", el("a", { href: "#logs", class: "button quiet" }, "查看全部")),
    rows.length ? table(["模型", "HTTP 状态", "Token", "延迟", "时间"], rows.slice(0, 5), row => [
      row.origin_model, badge(row.status), row.is_estimated ? fmt(row.total_tokens) + "（估算）" : fmt(row.total_tokens),
      fmt(row.latency_ms) + " ms", new Date(row.created_at).toLocaleTimeString("zh-CN"),
    ]) : empty(logResult.status === "fulfilled" ? "还没有保留的请求日志。使用 API Key 发起调用后再刷新。" : "最近请求暂不可用。"));
  const connection = panel(heading("开始接入", "同源接口地址，兼容标准客户端。"),
    text("code", location.origin + "/v1", "code-block"), el("div", { class: "actions" },
      button("复制接口地址", () => copyValue(location.origin + "/v1")),
      el("a", { href: "#playground", class: "button quiet" }, "调用测试")),
    text("p", "调用使用 API Key，登录会话不能代替调用凭据。", "fine"));
  const channelRows = channelResult?.status === "fulfilled" ? list(channelResult.value.data) : [];
  const channelPanel = owner.role === "admin" ? panel(heading("渠道配置", "启用状态不等于即时健康探测。", el("a", { href: "#channels", class: "button quiet" }, "管理")),
    channelRows.length ? table(["渠道", "配置状态"], channelRows.slice(0, 3), c => [c.name, badge(c.status)]) :
      empty(channelResult?.status === "fulfilled" ? "尚未配置静态上游渠道。" : "渠道配置暂不可用。")) : connection;
  return el("div", {},
    pageHeader("工作空间总览", "请求、用量与持久账本，各自保持真实口径。", actions(
      button("刷新", () => run(navigate)), el("a", { href: "#keys", class: "button primary" }, "+ 创建 Key"))),
    el("div", { class: "stats" },
      metricCard("API 请求", fmt(usage.total_requests), "当前用户保留日志汇总"),
      metricCard("Token 用量", fmt(usage.total_tokens), "日志口径，可能包含估算"),
      metricCard("日志观测费用", fmt(usage.total_cost), "CNY · 不作为最终账单"),
      metricCard("活跃 Key", list(keysResponse.data).filter(k => k.status === "active" && (!k.expires_at || k.expires_at * 1000 > Date.now())).length, "当前用户、未过期")),
    el("div", { class: "overview-grid" }, flowPanel, panel(heading("模型用量分布", "同一保留样本，按请求计数"), distribution)),
    el("div", { class: "overview-secondary" }, recent, el("div", {}, channelPanel, financePanel)),
    owner.role === "admin" ? connection : null,
    text("p", "汇总来自可丢弃的保留日志，不代表完整财务历史。图表最多使用最近 100 条当前用户日志，不展示推测的增长率、延迟或渠道健康。", "fine"));
}
async function copyValue(value) {
  try {
    if (!navigator.clipboard) throw new Error("clipboard unavailable");
    await navigator.clipboard.writeText(value);
    notice("已复制。");
  } catch {
    const input = el("textarea", { readonly: true, rows: 3, "aria-label": "可复制内容" });
    input.value = value;
    const dialog = el("dialog", { class: "editor", "aria-label": "复制内容" },
      heading("复制内容", "浏览器不可自动复制，请手动选择复制。", button("关闭", () => dialog.close())), input);
    manageDialog(dialog, () => { input.value = ""; });
    document.body.append(dialog); dialog.showModal(); input.select();
  }
}

async function keys() {
  const [response, available] = await Promise.all([api.request("/api/me/keys"), api.request("/api/me/models")]);
  const defaultPolicy = { model_mode: available.policy.mode, models: available.policy.models };
  function edit(key = {}) {
    editDialog(key.id ? "编辑 API Key" : "创建 API Key", [
      { name: "name", label: "Key 名称", value: key.name || "", required: true },
      { name: "status", label: "状态", value: key.status || "active", options: ["active", "disabled"] },
      ...policyFields(key.id ? key : defaultPolicy),
      ...limitFields(key),
      ...(key.id ? [{ name: "expiry_action", label: "到期时间更新", value: "keep",
        options: [["keep", "保留原到期时间"], ["replace", "从保存时刻重新设置"]],
        hint: key.expires_at ? `当前到期时间：${new Date(key.expires_at * 1000).toLocaleString()}` : "当前 Key 没有到期时间。" }] : []),
      { name: "expires_in_seconds", label: "从现在起的有效秒数", type: "number", min: 0,
        value: 0,
        hint: key.id ? "仅重新设置时生效。0 移除到期时间；保留选项也保留已过期状态。" : "0 表示无到期时间。", full: true },
    ], async values => {
      const result = await api.request(`/api/me/keys${key.id ? `/${key.id}` : ""}`, {
        method: key.id ? "PUT" : "POST", body: keyInput(values),
      });
      return { secret: result.data.key };
    }, "Key 不能扩大用户权限；零限制继承用户限制，仍受实例上限约束。");
  }
  return el("div", {}, pageHeader("我的 API Key", "为每个应用创建独立、权限明确的调用凭据。", button("+ 创建 Key", () => edit(), "primary")),
    panel(table(["名称 / 前缀", "模型范围", "状态", "到期时间", "操作"], list(response.data), key => [
      el("div", {}, text("strong", key.name), text("code", key.key_prefix, "block")),
      key.model_mode === "all" ? "授权范围内全部" : listText(key.models),
      badge(key.status), key.expires_at ? new Date(key.expires_at * 1000).toLocaleString() : "无到期时间",
      actions(button("编辑", () => edit(key), "quiet"), button("撤销", () => run(async () => {
        if (!confirm(`Revoke key "${key.name}"? Clients using it will stop working.`)) return;
        await api.request(`/api/me/keys/${key.id}`, { method: "DELETE" });
        notice("Key 已撤销。"); await navigate();
      }), "danger")),
    ])));
}

async function modelPage() {
  const response = await api.request("/api/me/models");
  const search = el("input", { type: "search", "aria-label": "搜索模型", placeholder: "搜索已授权模型名称" });
  const grid = el("div", { class: "model-grid" });
  const names = list(response.data);
  const count = text("span", "", "muted");
  function filter() {
    const filtered = names.filter(name => name.toLowerCase().includes(search.value.toLowerCase()));
    count.textContent = filtered.length + " / " + names.length + " 个模型";
    grid.replaceChildren(...(filtered.length ? filtered.map(name =>
      panel(el("span", { class: "model-symbol" }, icon("models")), text("h2", name),
        text("p", "模型目录由当前系统、用户与渠道权限共同决定。", "muted"),
        el("a", { href: "#playground", class: "text-link" }, "进入调用测试 ›"))) :
      [panel(empty(names.length ? "没有匹配的模型，请尝试其他名称。" : "尚无可见模型，请联系管理员配置静态上游与权限。"))]));
  }
  search.addEventListener("input", filter); filter();
  return el("div", {}, pageHeader("模型目录", "只展示当前账户可见的名称，不推测能力、价格或供应商支持。"),
    el("div", { class: "filter-bar" }, search, count), grid,
    text("p", response.policy.mode === "all" ? "通配渠道可能接受额外模型名；目录可见不保证当前有健康上游。调用 Key 仍可进一步收窄权限。" :
      "调用 Key 的模型范围仍受当前系统与用户权限约束。", "fine"));
}

async function playground() {
  const epoch = generation;
  const response = await api.request("/api/me/models");
  let controller;
  const output = el("pre", { class: "response", tabindex: "0", "aria-live": "polite" }, "调用结果会显示在这里。");
  const submit = el("button", { type: "submit", class: "button primary" }, "发送请求");
  const cancel = button("取消请求", () => controller?.abort());
  cancel.disabled = true;
  const form = el("form", {}, heading("连接测试", "真实的非流式调用，按当前实例和上游计费策略执行。"),
    el("div", { class: "form-grid" },
      field({ name: "model", label: "模型", value: list(response.data)[0] || "", required: true }),
      field({ name: "key", label: "调用 API Key", type: "password", required: true, hint: "仅用于本次调用，不写入浏览器存储。" }),
      field({ name: "prompt", label: "消息", type: "textarea", value: "Say hello in one sentence.", required: true, full: true })),
    actions(submit, cancel));
  if (epoch === generation) {
    disposeView = () => { controller?.abort(); form.reset(); };
  }
  form.addEventListener("submit", async event => {
    event.preventDefault();
    const values = Object.fromEntries(new FormData(form));
    controller = new AbortController();
    submit.disabled = true; cancel.disabled = false;
    output.textContent = "正在等待上游…";
    try {
      const result = await api.request("/v1/chat/completions", { method: "POST", callKey: values.key, timeout: 610000,
        signal: controller.signal, body: { model: values.model, messages: [{ role: "user", content: values.prompt }], max_tokens: 128 } });
      output.textContent = JSON.stringify(result, null, 2);
    } catch (error) { output.textContent = error.message; }
    finally { submit.disabled = false; cancel.disabled = true; form.querySelector('[name="key"]').value = ""; }
  });
  return el("div", {}, pageHeader("调用测试", "验证调用 Key、模型路由及真实响应。"),
    el("div", { class: "playground-grid" }, panel(form), panel(heading("响应", "网关返回的原始 JSON"), output)),
    text("p", `Client base URL: ${location.origin}/v1. Login sessions cannot be used as API keys.`, "fine"));
}

async function logs() {
  const global = user.role === "admin";
  const host = el("div");
  const limit = 50;
  let offset = 0;
  let loadVersion = 0;
  let rowCount = 0;
  const previous = button("上一页", () => run(() => load(Math.max(0, offset - limit))));
  const next = button("下一页", () => run(() => load(offset + limit)));
  const count = text("span", "", "muted");
  const model = el("input", { name: "model", placeholder: "筛选模型", "aria-label": "筛选模型" });
  const scope = el("select", { "aria-label": "日志范围" }, el("option", { value: "me" }, "我的请求"),
    global ? el("option", { value: "admin" }, "全站请求（管理员）") : null);
  const filter = el("form", { class: "filter-bar" }, scope, global ? model : null,
    el("button", { class: "button secondary" }, "刷新"));
  async function load(targetOffset = offset) {
    previous.disabled = next.disabled = true;
    const epoch = generation;
    const version = ++loadVersion;
    const path = scope.value === "admin" ? "/api/admin/logs" : "/api/me/logs";
    try {
      const response = await api.request(`${path}?limit=${limit}&offset=${targetOffset}${scope.value === "admin" && model.value ? `&model=${encodeURIComponent(model.value)}` : ""}`);
      if (epoch !== generation || version !== loadVersion) return;
      const rows = list(response.data);
      offset = targetOffset;
      rowCount = rows.length;
      host.replaceChildren(table(["时间 / 请求 ID", "模型", "状态", "Token", "日志费用 / CNY", "延迟"], rows, row => [
        el("div", {}, text("span", new Date(row.created_at).toLocaleString()), text("code", row.request_id, "block")),
        row.origin_model, badge(row.status), `${fmt(row.total_tokens)}${row.is_estimated ? " (est.)" : ""}`,
        el("div", {}, `${fmt(row.cost)}${row.price_missing ? " / no price" : ""}`,
          row.billing_state ? el("span", { class: "block", title: row.billing_id }, row.billing_state === "pending" ? "预算保留中 / 待核对" : row.billing_state) : null,
          row.price_version ? el("code", { class: "block", title: row.price_version }, `Price ${row.price_version.slice(0, 12)}`) : null),
        `${fmt(row.latency_ms)} ms`,
      ]));
      count.textContent = `记录 ${rows.length ? offset + 1 : 0}-${offset + rows.length}`;
    } catch (error) {
      if (epoch === generation && version === loadVersion) throw error;
    } finally {
      if (epoch === generation && version === loadVersion) {
        previous.disabled = offset === 0; next.disabled = rowCount < limit;
      }
    }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); run(() => load(0)); });
  await load();
  return el("div", {}, pageHeader("请求日志", "保留的访问日志，不是持久账本；新日志可能稍后出现。"),
    panel(filter, host, actions(previous, count, next)));
}

async function billingLedger() {
  let offset = 0;
  let loadVersion = 0;
  let rowCount = 0;
  const limit = 50;
  const host = el("div");
  const state = el("select", { "aria-label": "财务状态" },
    [["pending", "待核对"], ["", "全部状态"], ["settled", "已结算"], ["active", "执行中"], ["reserved", "已预留"]]
      .map(([value, label]) => el("option", { value }, label)));
  const owner = el("input", { "aria-label": "财务用户 ID", placeholder: "用户 ID（可选）", inputmode: "numeric" });
  const key = el("input", { "aria-label": "财务 Key ID", placeholder: "Key ID（可选）", inputmode: "numeric" });
  const count = text("span", "", "muted");
  const previous = button("上一页", () => run(() => load(Math.max(0, offset - limit))));
  const next = button("下一页", () => run(() => load(offset + limit)));
  const filter = el("form", { class: "filter-bar" }, state, owner, key, el("button", { class: "button secondary" }, "刷新账本"));
  async function load(targetOffset = offset) {
    const epoch = generation;
    const version = ++loadVersion;
    previous.disabled = next.disabled = true;
    const query = new URLSearchParams({ state: state.value, limit, offset: targetOffset });
    if (owner.value.trim()) query.set("user_id", owner.value.trim());
    if (key.value.trim()) query.set("token_id", key.value.trim());
    try {
      const response = await api.request(`/api/admin/billing/reservations?${query}`);
      if (epoch !== generation || version !== loadVersion) return;
      const rows = list(response.data);
      offset = targetOffset;
      rowCount = rows.length;
      host.replaceChildren(rows.length ? table(["预留记录 / 归属", "模型 / 价格版本", "状态", "最终用户费用", "当前用户占用", "上游包络 / 预算", "操作"], rows, row => {
        const amounts = ledgerAmounts(row);
        return [
          el("div", {}, text("code", row.id), text("span", `User ${row.user_id} / Key ${row.token_id}`, "block")),
          el("div", {}, row.origin_model, text("code", row.price_version, "block")),
          el("div", {}, financialBadge(row.state), text("span", row.reason, "block")),
          amounts.finalCharge, amounts.retainedBudget,
          el("div", {}, `Envelope ${amounts.providerEnvelope}`, text("span", `Budget ${amounts.providerBudget}`, "block")),
          button("查看", () => run(() => inspectBilling(row.id)), "quiet"),
        ];
      }) : empty("没有匹配记录。待核对费用会继续保留预算，直到验证或明确豁免。"));
      count.textContent = `记录 ${rows.length ? offset + 1 : 0}-${offset + rows.length}`;
    } catch (error) {
      if (epoch === generation && version === loadVersion) throw error;
    } finally {
      if (epoch === generation && version === loadVersion) {
        previous.disabled = offset === 0; next.disabled = rowCount < limit;
      }
    }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); run(() => load(0)); });
  await load();
  return el("div", {}, pageHeader("财务核对", "持久预留与核对决定，独立于访问日志保留。"),
    panel(text("p", "用户费用与占用使用账户单位，上游包络使用合同记录币种，二者不自动换算或比较。待核对不代表免费，已结算记录已释放用户占用。此处不记录实际上游账单；仅 strict 模式下租约过期的待核对记录可作最终决定。", "muted"),
      filter, host, actions(previous, count, next)));
}

async function inspectBilling(id) {
  const epoch = generation;
  const response = await api.request(`/api/admin/billing/reservations/${encodeURIComponent(id)}`);
  if (epoch !== generation || !user) return;
  const { reservation: row, attempts, resolution, price_snapshot: snapshot } = response.data;
  const amounts = ledgerAmounts(row);
  const dialog = el("dialog", { class: "editor billing-detail", "aria-label": "财务预留记录" });
  manageDialog(dialog);
  dialog.append(heading("财务预留记录", row.id, button("关闭", () => dialog.close())),
    text("p", `${row.origin_model} / User ${row.user_id} / Key ${row.token_id}`),
    text("p", `State: ${row.state}.`, "muted"),
    heading("用户费用与预算", "金额使用账户单位，最终费用、当前占用与原始预留分别展示。"),
    el("dl", { class: "facts" },
      text("dt", "最终用户费用"), text("dd", amounts.finalCharge),
      text("dt", "当前用户预算占用"), text("dd", amounts.retainedBudget),
      text("dt", "原始用户预留"), text("dd", amounts.originalReservation)),
    text("p", row.state === "settled" ? "原始用户预留已释放，历史预留金额仍保留在记录中。" :
      row.state === "pending" ? "待核对继续占用用户预算；未知执行或用量不代表免费。" :
      ["reserved", "active"].includes(row.state) ? "用户预算仍保留，最终费用尚未确定。" : "预算占用与最终费用状态未知。", "muted"),
    heading("上游预算观察", "累计上界使用合同记录币种，不换算为账户单位，也不是实际上游账单或用户占用。用户结算不会清除这些观察。"),
    el("dl", { class: "facts" },
      text("dt", "累计上游包络"), text("dd", amounts.providerEnvelope),
      text("dt", "上游请求预算"), text("dd", amounts.providerBudget),
      text("dt", "实际上游费用"), text("dd", "未记录")),
    text("p", `Lease deadline: ${new Date(row.lease_until).toLocaleString()}. ${row.reason}`, "fine"),
    heading("上游尝试", "用量按历史用户售价计算，上游包络使用独立合同；二者都不是额外用户费用或实际上游账单。"),
    table(["尝试 / 目标", "Outcome", "Token", "按历史售价观察", "上游成本包络"], list(attempts), attempt => [
      el("div", {}, `#${attempt.number} / Channel ${attempt.channel_id}`, text("span", `Credential ${attempt.credential_id} / ${attempt.actual_model} / ${attempt.group}`, "block")),
      `${attempt.status} / ${attempt.termination || attempt.state}`, `${attempt.prompt_tokens} + ${attempt.output_tokens}`,
      attempt.cost_known ? ledgerMoney(attempt.cost_units) : "未知（不代表免费）",
      el("div", {}, attempt.provider_contract_version ? ledgerMoney(attempt.provider_bound_units) : "未记录历史合同",
        attempt.provider_contract_version ? el("details", {}, el("summary", {}, `Snapshot ${attempt.provider_contract_version.slice(0, 12)}`),
          text("pre", attempt.provider_contract_snapshot)) : null),
    ]));
  if (resolution) {
    dialog.append(heading("最终管理员决定", `${resolution.action} / Admin ${resolution.actor_id}`),
      text("p", resolution.reason), text("p", `Evidence: ${resolution.evidence}`, "fine"),
      text("p", `Tariff-priced usage: ${resolution.cost_known ? ledgerMoney(resolution.calculated_units) : "Unknown"}. Final user charge: ${ledgerMoney(resolution.charge_units)}.`, "muted"),
      resolution.action === "waive" ? text("p", "用户费用已豁免，这不证明上游执行免费。", "muted") : null);
  } else if (row.can_resolve) {
    dialog.append(actions(button("核对保留费用", () => {
      dialog.close();
      editDialog("核对保留费用", [
        { name: "action", label: "核对决定", value: "settle_usage", options: [
          ["settle_usage", "按已验证用量结算"], ["cap_usage", "已验证超额费用按预留封顶"], ["waive", "豁免费用并释放占用"],
        ] },
        { name: "prompt_tokens", label: "已验证输入 Token", hint: "用量决定必须填写；豁免时留空。" },
        { name: "completion_tokens", label: "已验证输出 Token", hint: "没有输出时明确填写 0。" },
        { name: "reason", label: "理由", type: "textarea", required: true, full: true },
        { name: "evidence", label: "验证 / 批准依据", type: "textarea", required: true, full: true,
          hint: "不要填入凭据或私有请求正文。" },
        { name: "confirmation", label: "确认完整财务 ID", required: true, full: true, hint: row.id },
      ], values => api.request(`/api/admin/billing/reservations/${encodeURIComponent(row.id)}/resolve`, {
        method: "POST", body: resolutionInput(values, row.id),
      }), "最终决定原子更新双预算且不可修改，采用历史价格和原目标。豁免不代表上游执行免费。",
      { submitLabel: "提交最终决定", successMessage: "财务决定已提交。" });
    }, "primary")));
  } else {
    dialog.append(text("p", "仅 strict 模式下租约过期的待核对记录可作决定；已结算决定不可改写。", "fine"));
  }
  const history = el("details", {}, text("summary", `Historical tariff / ${row.price_version}`),
    text("pre", snapshot, "code-block"));
  dialog.append(history);
  document.body.append(dialog);
  dialog.showModal();
}

async function users() {
  const response = await api.request("/api/admin/users");
  function edit(value = {}) {
    editDialog(value.id ? `Edit ${value.username}` : "创建用户", [
      { name: "username", label: "用户名", value: value.username || "", required: true },
      { name: "password", label: value.id ? "新密码（可选）" : "初始密码", type: "password", required: !value.id,
        hint: "12–72 字节，请通过安全渠道交付。" },
      { name: "role", label: "角色", options: ["user", "admin"], value: value.role || "user" },
      { name: "status", label: "状态", options: ["active", "disabled"], value: value.status || "active" },
      ...policyFields(value), ...limitFields(value),
    ], values => api.request(`/api/admin/users${value.id ? `/${value.id}` : ""}`, { method: value.id ? "PUT" : "POST", body: userInput(values) }),
    "停用用户会拒绝其新的 Key 和会话请求；最后一个活跃管理员受保护。");
  }
  return el("div", {}, pageHeader("用户与权限", "为每个用户授予明确的模型权限、额度与容量。", button("+ 创建用户", () => edit(), "primary")),
    panel(table(["用户", "角色", "模型访问策略", "状态", "限制", "操作"], list(response.data), value => [
      text("strong", value.username), value.role, value.model_mode === "all" ? "All" : listText(value.models), badge(value.status),
      `${value.rpm_limit || "-"} RPM / ${value.concurrency_limit || "-"} concurrent`,
      button("编辑权限", () => edit(value), "quiet"),
    ])));
}

async function channels() {
  const [response, status] = await Promise.all([api.request("/api/admin/channels"), api.request("/api/admin/config-status")]);
  function edit(value = {}) {
    editDialog(value.id ? `Edit ${value.name}` : "创建渠道", [
      { name: "name", label: "名称", value: value.name || "", required: true },
      { name: "type", label: "协议", options: ["openai", "anthropic", "gemini"], value: value.type || "openai" },
      { name: "base_url", label: "上游地址", value: value.base_url || "", required: true },
      { name: "group_name", label: "渠道组", value: value.group_name || "default" },
      { name: "models", label: "模型目录", value: list(value.models).join(", "), required: true, full: true },
      ...(!value.id ? [{ name: "api_key", label: "首个上游 API Key", type: "password", required: true, full: true }] : []),
      { name: "priority", label: "优先级", type: "number", value: value.priority || 0 },
      { name: "weight", label: "权重", type: "number", value: value.weight || 1, min: 1 },
      { name: "status", label: "状态", options: ["enabled", "disabled"], value: value.status || "enabled" },
      { name: "model_mapping", label: "模型映射（JSON）", type: "textarea", value: JSON.stringify(value.model_mapping || {}, null, 2), full: true },
    ], values => api.request(`/api/admin/channels${value.id ? `/${value.id}` : ""}`, {
      method: value.id ? "PUT" : "POST", body: channelInput(values),
    }), "配置先保存再发布。凭据单独管理；编辑渠道不会覆盖凭据池。");
  }
  const content = [pageHeader("渠道管理", "配置静态上游、模型、权重与独立凭据。", button("+ 创建渠道", () => edit(), "primary")),
    publicationCard("渠道发布状态", status.data.channels)];
  for (const value of list(response.data)) {
    content.push(panel(
      heading(value.name, `${value.type.toUpperCase()} / ${value.base_url}`, actions(badge(value.status),
        button("编辑渠道", () => edit(value)), button("删除", () => run(async () => {
          if (!confirm(`Delete channel "${value.name}" and all of its credentials?`)) return;
          pendingPublication = false;
          await api.request(`/api/admin/channels/${value.id}`, { method: "DELETE" });
          if (!pendingPublication) notice("渠道已删除并发布。"); await navigate();
        }), "danger"))),
      text("p", `${listText(value.models)} / Priority ${value.priority} / Weight ${value.weight} / Group ${value.group_name}`, "fine"),
      heading("凭据池", "密钥仅显示掩码，恢复与停用状态由服务端校验。",
        button("+ 添加凭据", () => credentialDialog(value.id))),
      table(["凭据 / 密钥掩码", "状态", "权重", "失败次数", "操作"], list(value.credentials), credential => [
        el("div", {}, text("strong", credential.name), text("code", credential.api_key, "block")), badge(credential.status),
        credential.weight, credential.fail_count, actions(
          button("编辑", () => credentialDialog(value.id, credential), "quiet"),
          button(credential.status === "disabled" ? "启用" : "停用", () => setCredential(credential, credential.status === "disabled" ? "active" : "disabled"), "quiet"),
          ["cooldown", "banned"].includes(credential.status) ? button("恢复", () => setCredential(credential, "active"), "quiet") : null,
          button("删除", () => run(async () => {
            if (!confirm(`Delete credential "${credential.name}"? The last credential cannot be deleted.`)) return;
            await api.request(`/api/admin/credentials/${credential.id}`, { method: "DELETE" }); await navigate();
          }), "danger")),
      ])));
  }
  if (!list(response.data).length) content.push(panel(empty("还没有渠道，请配置上游及其对外模型名称。")));
  return el("div", {}, content);
}

function credentialDialog(channelID, value = {}) {
  editDialog(value.id ? "编辑凭据" : "添加凭据", [
    { name: "name", label: "名称", value: value.name || "", required: true },
    { name: "weight", label: "权重", value: value.weight || 1, type: "number", min: 1 },
    { name: "api_key", label: value.id ? "替换密钥（可选）" : "上游密钥", type: "password", required: !value.id, full: true },
  ], values => api.request(value.id ? `/api/admin/credentials/${value.id}` : `/api/admin/channels/${channelID}/credentials`, {
    method: value.id ? "PUT" : "POST", body: { name: values.name, weight: Number(values.weight), ...(values.api_key ? { api_key: values.api_key } : {}) },
  }), "替换密钥留空会保留旧密钥，不读取已保存的完整密钥。");
}
function setCredential(value, status) {
  run(async () => {
    await api.request(`/api/admin/credentials/${value.id}/status`, { method: "POST", body: { status } });
    await navigate();
  });
}
function publicationCard(title, value) {
  const applied = value.desired_revision === value.applied_revision && !value.last_error;
  return el("section", { class: `publication ${applied ? "" : "pending"}` },
    el("div", {}, text("strong", title), text("p", `Desired v${value.desired_revision} / Applied v${value.applied_revision}`, "muted")),
    badge(applied ? "applied" : "pending"),
    value.last_error ? text("p", value.last_error, "form-error") : null);
}

async function settings() {
  const [status, policy, prices] = await Promise.all([
    api.request("/api/admin/config-status"), api.request("/api/admin/model-policy"), api.request("/api/admin/prices"),
  ]);
  const policyValue = { model_mode: policy.data.mode, models: policy.data.models };
  const publication = el("div");
  function showPublication(value) {
    publication.replaceChildren(publicationCard("渠道与凭据", value.channels), publicationCard("售价与组倍率", value.prices),
      text("p", `Publication epoch: ${value.epoch}. Revisions are per-process and restart at boot, not durable schema versions.`, "fine"));
  }
  showPublication(status.data);
  const retry = button("重试发布", () => run(async () => {
    if (retry.disabled) return;
    retry.disabled = true;
    const epoch = generation;
    try {
      const response = await api.request("/api/admin/config-status/apply", { method: "POST" });
      if (epoch !== generation) return;
      showPublication(response.data);
      notice("配置已发布。");
    } catch (error) {
      if (epoch !== generation) return;
      if (error.data?.channels && error.data?.prices) showPublication(error.data);
      else {
        const current = await api.request("/api/admin/config-status");
        if (epoch !== generation) return;
        showPublication(current.data);
      }
      throw error;
    } finally { retry.disabled = false; }
  }), "primary");
  return el("div", {}, pageHeader("价格与发布", "已保存配置与运行快照分别展示，发布失败可单独重试。",
    retry), publication,
    panel(heading("系统模型策略", "下次认证直接采用，不属于运行快照修订号。",
      button("编辑策略", () => editDialog("系统模型策略", policyFields(policyValue), values => {
        const p = policyInput(values);
        return api.request("/api/admin/model-policy", { method: "PUT", body: { mode: p.model_mode, models: p.models } });
      }, "空白名单拒绝所有模型调用，包括管理员的 API Key。"))),
      text("p", policy.data.mode === "all" ? "系统策略允许全部模型" : `Allowlist: ${listText(policy.data.models)}`)),
    panel(heading("模型售价", "每千 Token 的 CNY 售价。运行快照不可变；strict 结算另需已配置账本和费用上限。",
      button("+ 设置售价", () => priceDialog())),
      table(["模型", "输入 / 千 Token", "输出 / 千 Token", "操作"], list(prices.data), price => [
        text("code", price.model), fmt(price.input_price_per_1k), fmt(price.output_price_per_1k),
        actions(button("编辑", () => priceDialog(price), "quiet"), button("删除", () => run(async () => {
          if (!confirm(`Remove price for ${price.model}? Strict mode rejects new calls without a matching price; legacy may count them as zero.`)) return;
          await api.request(`/api/admin/prices/${encodeURIComponent(price.model)}`, { method: "DELETE" }); await navigate();
        }), "danger")),
      ])),
    panel(heading("渠道组倍率", "完整替换，未列出的组倍率默认为 1.0。", button("编辑倍率", () =>
      editDialog("渠道组倍率", [{ name: "rates", label: "倍率（JSON）", type: "textarea", full: true, value: JSON.stringify(prices.group_rates, null, 2) }],
        values => {
          const rates = JSON.parse(values.rates);
          if (!rates || Array.isArray(rates) || typeof rates !== "object" ||
            Object.values(rates).some(rate => typeof rate !== "number" || !Number.isFinite(rate) || rate <= 0)) {
            throw new Error("倍率必须是由有限正数组成的 JSON 对象。");
          }
          return api.request("/api/admin/group-rates", { method: "PUT", body: rates });
        }))), text("pre", JSON.stringify(prices.group_rates, null, 2), "code-block")),
    panel(heading("部署说明", "服务端启动设置由 YAML 或环境变量管理。"),
      text("p", "在反向代理处启用 HTTPS，并限制 /metrics 的访问来源。SQLite 必须停写后备份，不能原地降级已升级的数据库。", "muted"),
      text("p", "并发和速率限制作用于单实例。访问日志可能丢弃或过期，不能替代可靠账本。", "fine")));
}

function priceDialog(value = {}) {
  editDialog("模型售价", [
    { name: "model", label: "模型名或匹配模式", value: value.model || "", required: true, full: true },
    { name: "input_price_per_1k", label: "输入 / 千 Token（CNY）", type: "number", value: value.input_price_per_1k || 0, min: 0 },
    { name: "output_price_per_1k", label: "输出 / 千 Token（CNY）", type: "number", value: value.output_price_per_1k || 0, min: 0 },
  ], values => api.request(`/api/admin/prices/${encodeURIComponent(values.model)}`, { method: "PUT", body: {
    input_price_per_1k: Number(values.input_price_per_1k), output_price_per_1k: Number(values.output_price_per_1k),
  } }));
}

window.addEventListener("hashchange", () => run(navigate));
login();

async function routingDiagnostics() {
  const epoch = generation;
  let disposed = false, offset = 0, hasMore = false, version = 0, detailVersion = 0;
  let controller, detailController;
  const current = () => !disposed && epoch === generation && user;
  disposeView = () => { disposed = true; controller?.abort(); detailController?.abort(); };
  const host = el("div"), detail = el("div", { class: "routing-detail" });
  const error = el("p", { role: "alert", class: "form-error" }), state = text("p", "", "muted"), count = text("span", "", "muted");
  const scope = el("select", { "aria-label": "路由查看范围" }, el("option", { value: "me" }, "我的请求"),
    user.role === "admin" ? el("option", { value: "admin" }, "全站请求（管理员）") : null);
  const requestID = el("input", { maxlength: 64, "aria-label": "精确 Request ID", placeholder: "精确 Request ID" });
  const owner = el("input", { type: "number", min: 1, "aria-label": "路由用户 ID", placeholder: "用户 ID（管理员）", disabled: true });
  const key = el("input", { type: "number", min: 1, "aria-label": "路由 Key ID", placeholder: "Key ID" });
  const filter = el("form", { class: "filter-bar" }, scope, requestID, owner, key, el("button", { class: "button secondary" }, "刷新诊断"));
  const previous = button("上一页", () => load(Math.max(0, offset - 50))), next = button("下一页", () => load(offset + 50));
  const base = () => "/api/" + scope.value + "/routing";
  const date = value => value ? new Date(value).toLocaleString() : "未观察到";
  const target = row => row.channel_id ? "Channel " + row.channel_id + " / Key " + row.credential_id : "未选择";
  async function load(targetOffset = offset) {
    const ownVersion = ++version;
    ++detailVersion; detailController?.abort(); detail.replaceChildren();
    controller?.abort(); controller = new AbortController();
    previous.disabled = next.disabled = true; error.textContent = ""; state.textContent = "正在读取保留的观察…";
    host.replaceChildren();
    const query = new URLSearchParams({ limit: "50", offset: String(targetOffset) });
    if (requestID.value) query.set("request_id", requestID.value);
    if (key.value) query.set("token_id", key.value);
    if (scope.value === "admin" && owner.value) query.set("user_id", owner.value);
    try {
      const response = await api.request(base() + "?" + query, { signal: controller.signal });
      if (!current() || ownVersion !== version) return;
      offset = targetOffset; hasMore = response.has_more === true;
      state.textContent = response.enabled ?
        "诊断已启用，最多保留 " + response.capacity + " completed HTTP scopes for " + response.retention_seconds / 60 + " minutes; restart or capacity eviction may remove earlier rows." :
        "诊断记录未启用。启用 server.route_diagnostics 并重启后可保留最近的 HTTP 路由事实。";
      const rows = list(response.data);
      host.replaceChildren(rows.length ? table(["请求 / 结束时间", "用户 / Key", "模型 / 操作", "结果 / 观察", "操作"], rows, row => [
        el("div", {}, text("code", row.request_id || "Request ID 未保留", "block"), date(row.finished_at)),
        "User " + row.user_id + " / Key " + row.token_id, el("div", {}, row.model || "未解析", text("span", row.operation, "block")),
        el("div", {}, row.reason.replaceAll("_", " "), text("span", list(row.selections).length + " choices / " + list(row.attempts).length + " attempts", "block")),
        button("查看路由", () => inspect(row.id), "quiet"),
      ]) : empty("没有匹配的保留观察，缺少记录不证明请求从未执行。"));
      count.textContent = "记录 " + (rows.length ? offset + 1 : 0) + "-" + (offset + rows.length);
    } catch (failure) {
      if (current() && ownVersion === version && failure.name !== "AbortError") { error.textContent = failure.message; state.textContent = "观察读取失败。"; }
    } finally { if (current() && ownVersion === version) { previous.disabled = offset === 0; next.disabled = !hasMore || offset >= 10000; } }
  }
  async function inspect(id) {
    const ownVersion = ++detailVersion, admin = scope.value === "admin", requestBase = base();
    detailController?.abort(); detailController = new AbortController();
    error.textContent = ""; detail.replaceChildren();
    try {
      const response = await api.request(requestBase + "/" + encodeURIComponent(id), { signal: detailController.signal });
      if (!current() || ownVersion !== detailVersion) return;
      const row = response.data;
      detail.replaceChildren(heading("路由事实", row.id),
        text("p", "只读观察。选择目标不证明已发送，协议完成不证明客户端已消费或财务已结算。", "muted"),
        el("dl", { class: "facts" },
          text("dt", "Request ID"), text("dd", row.request_id || "未保留"),
          text("dt", "开始 / 结束"), text("dd", date(row.started_at) + " / " + date(row.finished_at)),
          text("dt", "网关结果"), text("dd", String(row.status) + " / " + row.reason.replaceAll("_", " ")),
          text("dt", "客户端 HTTP"), text("dd", row.committed ? "已提交 / " + row.http_status : "未提交"),
          text("dt", "发送观察"), text("dd", row.phase),
          text("dt", "协议完成"), text("dd", String(row.protocol_complete)),
          text("dt", "调用中断"), text("dd", String(row.interrupted)),
          text("dt", "元数据截断"), text("dd", String(row.truncated))),
        heading("选择过程", "现有资格与选择事实，选择后仍可能在实际尝试前被拒绝。"),
        list(row.selections).length ? table(["结果 / 选择器", "符合资格 / 候选", "凭据数量", "优先级 / 耗时", ...(admin ? ["目标（管理员）"] : [])], list(row.selections), choice => [
          choice.outcome + " / " + choice.picker, choice.eligible_channels + " / " + choice.candidate_channels + " (" + choice.selection_pool_channels + " in priority pool)",
          choice.eligible_credentials + " usable / " + choice.total_credentials + " total; " + choice.tried_credentials + " tried / " + choice.unavailable_credentials + " unavailable",
          (choice.priority_fallback ? "优先级回退" : "当前优先级池") + " / " + choice.duration_micros + " us",
          ...(admin ? [target(choice)] : []),
        ]) : empty("此次调用没有观察到目标选择。"),
        heading("上游尝试", "已观察的尝试和提交事实，不保留原始上游错误、请求正文或凭据。"),
        list(row.attempts).length ? table(["尝试 / 结果", "发送 / 终止", "交付 / 协议", ...(admin ? ["目标 / 模型（管理员）"] : [])], list(row.attempts), attempt => [
          "#" + attempt.number + " / " + attempt.status, attempt.phase + " / " + attempt.termination,
          (attempt.committed ? "已提交 / " + attempt.http_status : "未提交") + "; protocol complete: " + attempt.protocol_complete,
          ...(admin ? [el("div", {}, target(attempt), text("span", attempt.actual_model || "未观察到", "block"))] : []),
        ]) : empty("此次调用没有观察到上游尝试。"));
    } catch (failure) { if (current() && ownVersion === detailVersion && failure.name !== "AbortError") error.textContent = failure.message; }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); load(0); });
  scope.addEventListener("change", () => { owner.disabled = scope.value !== "admin"; offset = 0; hasMore = false; load(0); });
  await load(0);
  return el("div", {}, pageHeader("路由诊断", "通过 Request ID 关联的已结束 HTTP 调用观察，每次调用独立记录。"),
    panel(text("p", "临时诊断不是请求账本或恢复日志；不包含活跃调用、认证失败、后台控制或 WS 轮次。", "muted"),
      filter, state, error, host, actions(previous, count, next)), panel(detail));
}

async function backgroundTasks() {
  const epoch = generation;
  let disposed = false, offset = 0, hasMore = false, version = 0, detailVersion = 0;
  let controller, detailController;
  const current = () => !disposed && epoch === generation && user;
  disposeView = () => { disposed = true; controller?.abort(); detailController?.abort(); };
  const host = el("div"), detail = el("div", { class: "task-detail" }), error = el("p", { role: "alert", class: "form-error" });
  const count = text("span", "", "muted");
  const scope = el("select", { "aria-label": "任务查看范围" }, el("option", { value: "me" }, "我的任务"),
    user.role === "admin" ? el("option", { value: "admin" }, "全站元数据（管理员）") : null);
  const stage = el("select", { "aria-label": "任务阶段" }, ["", "queued", "dispatching", "remote_active", "terminal", "uncertain"]
    .map(value => el("option", { value }, value || "全部阶段")));
  const owner = el("input", { type: "number", min: 1, "aria-label": "任务用户 ID", placeholder: "用户 ID（管理员）", disabled: true });
  const key = el("input", { type: "number", min: 1, "aria-label": "创建 Key ID", placeholder: "创建 Key ID" });
  const filter = el("form", { class: "filter-bar" }, scope, stage, owner, key, el("button", { class: "button secondary" }, "刷新任务"));
  const previous = button("上一页", () => load(Math.max(0, offset - 50)));
  const next = button("下一页", () => load(offset + 50));
  const base = () => "/api/" + scope.value + "/background";
  const date = value => value ? new Date(value).toLocaleString() : "未记录";
  async function load(target = offset) {
    const ownVersion = ++version;
    ++detailVersion; detailController?.abort(); detail.replaceChildren();
    controller?.abort(); controller = new AbortController();
    previous.disabled = next.disabled = true; error.textContent = "";
    const query = new URLSearchParams({ limit: "50", offset: String(target) });
    if (stage.value) query.set("stage", stage.value);
    if (key.value) query.set("token_id", key.value);
    if (scope.value === "admin" && owner.value) query.set("user_id", owner.value);
    try {
      const response = await api.request(base() + "?" + query, { signal: controller.signal });
      if (!current() || ownVersion !== version) return;
      offset = target; hasMore = response.has_more === true;
      host.replaceChildren(table(["任务 / 创建时间", "用户 / Key", "模型 / 阶段", "内容 / 事件", "操作"], list(response.data), row => [
        el("div", {}, text("code", row.id, "block"), date(row.created_at)),
        "User " + row.user_id + " / Key " + row.token_id,
        el("div", {}, row.model, text("span", row.stage + (row.cancel_requested ? " / cancel requested" : ""), "block")),
        el("div", {}, row.content_available ? "已保留（不展示正文）" : "已过期或删除", text("span", String(row.event_count) + (row.replay_pending ? " / replay pending" : "") , "block")),
        button("查看任务", () => inspect(row.id), "quiet")
      ]));
      count.textContent = "记录 " + (response.data?.length ? offset + 1 : 0) + "-" + (offset + list(response.data).length);
    } catch (failure) { if (current() && ownVersion === version && failure.name !== "AbortError") error.textContent = failure.message; }
    finally { if (current() && ownVersion === version) { previous.disabled = offset === 0; next.disabled = !hasMore || offset >= 10000; } }
  }
  async function inspect(id) {
    const ownVersion = ++detailVersion, requestBase = base();
    detailController?.abort(); detailController = new AbortController();
    error.textContent = "";
    try {
      const response = await api.request(requestBase + "/" + encodeURIComponent(id), { signal: detailController.signal });
      if (!current() || ownVersion !== detailVersion) return;
      const row = response.data;
      const requestCancel = button("请求取消", async () => {
        if (!current() || ownVersion !== detailVersion || !confirm("Request cancellation for " + row.id + "? This does not prove remote work has stopped.")) return;
        requestCancel.disabled = true;
        try {
          await api.request("/api/me/background/" + encodeURIComponent(row.id) + "/cancel", { method: "POST", body: { revision: row.revision }, signal: detailController.signal });
          if (!current() || ownVersion !== detailVersion) return;
          notice("取消请求已记录，请刷新查看远端结果；未知执行继续保持未知。");
          await load();
        } catch (failure) {
          if (!current() || ownVersion !== detailVersion) return;
          if (failure.status === 409) await inspect(row.id);
          if (current()) error.textContent = failure.message;
        } finally { if (requestCancel.isConnected) requestCancel.disabled = false; }
      }, "danger");
      detail.replaceChildren(heading("任务元数据", row.id),
        text("p", "Stage: " + row.stage + ". Terminal is not proof of success or settlement. No content or upstream handle is exposed.", "muted"),
        el("dl", { class: "facts" },
          text("dt", "修订号"), text("dd", row.revision),
          text("dt", "截止时间"), text("dd", date(row.deadline)),
          text("dt", "内容到期时间"), text("dd", date(row.content_expires_at)),
          text("dt", "元数据可清理时间"), text("dd", date(row.metadata_expires_at)),
          text("dt", "最后持久事件序号"), text("dd", row.last_sequence === null ? "None" : String(row.last_sequence)),
          text("dt", "事件回放待处理"), text("dd", String(row.replay_pending)),
          text("dt", "已请求取消"), text("dd", String(row.cancel_requested))),
        row.can_cancel && scope.value === "me" ? requestCancel : text("p", "仅可读取或当前无法再次请求取消。", "fine"));
    } catch (failure) { if (current() && ownVersion === detailVersion && failure.name !== "AbortError") error.textContent = failure.message; }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); load(0); });
  scope.addEventListener("change", () => { owner.disabled = scope.value !== "admin"; host.replaceChildren(); offset = 0; hasMore = false; load(0); });
  await load(0);
  return el("div", {}, pageHeader("后台任务", "登录会话授权的任务元数据；正文读取与收费由各自入口管理。"),
    panel(text("p", "取消不是退款，也不证明远端工作已停止；过期元数据可能为安全恢复继续保留。", "muted"), filter, error, host, actions(previous, count, next)), panel(detail));
}

function dateText(value) {
  return value ? new Date(value).toLocaleString("zh-CN") : "未记录";
}
function collection({ title, subtitle, filters = [], base, columns, row, renderDetail, exportRow, exportColumns, exportName, note }) {
  const epoch = generation;
  let disposed = false, offset = 0, hasMore = false, version = 0, detailVersion = 0, rows = [];
  let controller, detailController;
  const current = () => !disposed && epoch === generation && user;
  disposeView = () => { disposed = true; controller?.abort(); detailController?.abort(); };
  const host = el("div"), detail = el("div");
  const error = el("p", { class: "form-error", role: "alert" });
  const count = text("span", "", "muted");
  const previous = button("上一页", () => load(Math.max(0, offset - 50)));
  const next = button("下一页", () => load(offset + 50));
  const refresh = el("button", { type: "submit", class: "button" }, "刷新");
  const filter = el("form", { class: "filter-bar" }, filters.map(f => f.input), refresh);
  const exportButton = exportRow ? button("导出本页", () => {
    if (current()) downloadCSV(exportName, exportColumns, rows.map(exportRow));
  }) : null;
  const root = el("div", {}, pageHeader(title, subtitle, exportButton),
    panel(note ? text("p", note, "muted") : null, filter, error, host, actions(previous, count, next)), detail);
  async function load(target = offset) {
    if (!current()) return;
    const ownVersion = ++version;
    ++detailVersion; detailController?.abort(); detail.replaceChildren();
    controller?.abort(); controller = new AbortController();
    previous.disabled = next.disabled = true;
    error.textContent = "";
    host.setAttribute("aria-busy", "true");
    const query = new URLSearchParams({ limit: "50", offset: String(target) });
    for (const f of filters) if (f.key && f.input.value.trim()) query.set(f.key, f.input.value.trim());
    try {
      const response = await api.request(base() + "?" + query, { signal: controller.signal });
      if (!current() || ownVersion !== version) return;
      rows = list(response.data); offset = target;
      hasMore = response.has_more === true || (response.has_more === undefined && rows.length === 50);
      host.replaceChildren(response.enabled === false ? empty("原生用量记录未启用。请联系管理员核对实例配置；未启用不等于零用量。") :
        table(columns, rows, r => row(r, () => inspect(r.id))));
      count.textContent = "记录 " + (rows.length ? offset + 1 : 0) + "–" + (offset + rows.length);
    } catch (failure) {
      if (current() && ownVersion === version) {
        error.textContent = "读取失败：" + failure.message + "。可以刷新重试；不可用不代表没有记录。";
      }
    } finally {
      if (current() && ownVersion === version) {
        previous.disabled = offset === 0; next.disabled = !hasMore || offset >= 10000;
        host.removeAttribute("aria-busy");
      }
    }
  }
  async function inspect(id) {
    if (!current()) return;
    const ownVersion = ++detailVersion;
    detailController?.abort(); detailController = new AbortController();
    detail.replaceChildren();
    error.textContent = "";
    try {
      const response = await api.request(base() + "/" + encodeURIComponent(id), { signal: detailController.signal });
      if (!current() || ownVersion !== detailVersion) return;
      detail.replaceChildren(renderDetail(response.data));
    } catch (failure) {
      if (current() && ownVersion === detailVersion) error.textContent = "详情不可用：" + failure.message;
    }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); load(0); });
  return { root, load, current };
}
function selectFilter(label, values) {
  return el("select", { "aria-label": label }, values.map(([value, name]) => el("option", { value }, name)));
}
function idFilter(label) {
  return el("input", { "aria-label": label, placeholder: label, type: "number", min: "1", step: "1" });
}
async function ownerLedger() {
  const epoch = generation;
  const summaryResult = await api.request("/api/me/billing/summary").then(value => ({ value }), error => ({ error }));
  if (epoch !== generation || !user) return el("div");
  const state = selectFilter("个人财务状态", [["", "全部状态"], ["pending", "待核对"], ["settled", "已结算"], ["reserved", "已预留"], ["active", "执行中"]]);
  const key = idFilter("调用 Key ID");
  const summary = summaryResult.value?.data;
  const view = collection({
    title: "个人账本", subtitle: "仅当前账户的持久资金与预留记录；不具备修改或核对权限。",
    filters: [{ key: "state", input: state }, { key: "token_id", input: key }],
    base: () => "/api/me/billing/reservations",
    columns: ["记录 / 调用 Key", "模型 / 价格版本", "状态", "最终费用", "当前占用", "时间", "操作"],
    row: (r, inspect) => {
      const amounts = ownerAmounts(r);
      return [el("div", {}, text("code", r.id), text("span", "Key " + r.token_id, "block")),
        el("div", {}, r.origin_model, text("code", r.price_version, "block")), financialBadge(r.state),
        amounts.charge, amounts.held, dateText(r.created_at), button("查看资金事实", inspect, "quiet")];
    },
    renderDetail: data => {
      const r = data.reservation, amounts = ownerAmounts(r);
      return panel(heading("个人资金事实", r.id), el("dl", { class: "facts" },
        text("dt", "最终费用"), text("dd", amounts.charge),
        text("dt", "当前占用"), text("dd", amounts.held),
        text("dt", "原始预留"), text("dd", amounts.reserved),
        text("dt", "租约期限"), text("dd", dateText(r.lease_until)),
        text("dt", "历史价格版本"), text("dd", r.price_version),
        text("dt", "核对决定"), text("dd", data.resolution_action || "未记录")),
        text("p", "租约过期不会自动释放预算。原始预留是历史事实；只有明确结算的费用可以作为最终费用。", "fine"));
    },
    note: "所有金额使用账户单位；不换算上游合同币种，不把用户与 Key 的同笔预算重复相加。",
  });
  view.root.insertBefore(summary ? el("div", { class: "stats" },
    metricCard("累计已入账", summary.balance_known ? moneyText(summary.posted_used_units) : "未知", "含可能的历史期初余额"),
    metricCard("当前预算占用", summary.balance_known ? moneyText(summary.held_units) : "未知", "租约过期仍可能保留"),
    metricCard("待核对记录", knownCount(summary.pending_count), "待核对不代表免费"),
    metricCard("账本时间", summary.as_of ? new Date(summary.as_of).toLocaleTimeString("zh-CN") : "未知", "独立于访问日志")) :
    panel(text("p", "资金汇总暂不可用：" + summaryResult.error.message, "form-error")), view.root.children[1]);
  await view.load();
  return view.root;
}
async function usageRecords() {
  const scope = selectFilter("用量查看范围", [["me", "我的用量"], ...(user.role === "admin" ? [["admin", "全站用量（管理员）"]] : [])]);
  const state = selectFilter("用量记录状态", [["", "全部记录"], ["open", "记录中"], ["sealed", "已封存"], ["interrupted", "已中断"]]);
  const key = idFilter("用量 Key ID");
  const view = collection({
    title: "原生用量", subtitle: "逐次调用与 attempt 的原生观察；未知、部分和完整分别展示。",
    filters: [{ input: scope }, { key: "state", input: state }, { key: "token_id", input: key }],
    base: () => "/api/" + scope.value + "/usage-records",
    columns: ["记录 / Request ID", "模型 / 操作", "传输", "状态", "尝试数", "时间", "操作"],
    row: (r, inspect) => [el("div", {}, text("code", r.id), text("code", r.request_id || "未记录", "block")),
      el("div", {}, r.model, text("span", r.operation, "block")), r.transport === "websocket" ? "WebSocket 单轮" : "HTTP",
      badge(r.state), r.attempt_count, dateText(r.created_at), button("查看原生用量", inspect, "quiet")],
    renderDetail: data => panel(heading("调用观察", data.record.id),
      el("dl", { class: "facts" },
        text("dt", "协议终态快照"), text("dd", data.record.host.protocol_status || "未记录"),
        text("dt", "协议完成"), text("dd", data.record.host.protocol_complete ? "是" : "否 / 未确认"),
        text("dt", "下游提交"), text("dd", data.record.host.delivery_committed ? "已提交" : "未提交"),
        text("dt", "观察终止原因"), text("dd", data.record.host.termination || "未知")),
      table(["Attempt", "状态", "输入", "输出", "总量", "完整度 / 来源", "细分 Token"], list(data.attempts), a => [
        a.number, a.state, knownCount(a.usage.input_tokens), knownCount(a.usage.output_tokens), knownCount(a.usage.total_tokens),
        a.usage.completeness + " / " + a.usage.source,
        el("div", {}, "缓存命中 " + knownCount(a.usage.cached_input_tokens),
          text("span", "缓存写入 " + knownCount(a.usage.cache_write_input_tokens), "block"),
          text("span", "推理输出 " + knownCount(a.usage.reasoning_output_tokens), "block")),
      ]), text("p", "封存只说明观察已保存，不证明生成成功或财务结算。字段缺失保持未知；细分数量不从总量倒推。", "fine")),
    note: "此功能受实例开关、保留期和容量限制。后台任务用量由独立任务 owner 管理。",
    exportName: "txapi-usage-metadata.csv", exportColumns: ["ID", "Request ID", "模型", "操作", "传输", "状态"],
    exportRow: r => [r.id, r.request_id, r.model, r.operation, r.transport, r.state],
  });
  scope.addEventListener("change", () => view.load(0));
  await view.load();
  return view.root;
}
function ownerID(value) {
  const raw = String(value);
  const n = Number(raw);
  if (!/^[1-9]\d*$/.test(raw) || !Number.isSafeInteger(n)) throw new Error("用户 ID 必须是浏览器可精确表示的正整数。");
  return n;
}
async function adminKeys() {
  const epoch = generation;
  const response = await api.request("/api/admin/keys");
  const usersResponse = await api.request("/api/admin/users");
  if (epoch !== generation || !user) return el("div");
  const owners = list(usersResponse.data).filter(u => u.status === "active").map(u => [String(u.id), u.username + " · #" + u.id]);
  function edit(key = {}) {
    if (!key.id && !owners.length) { notice("没有可用的归属用户，请先启用一个用户。", true); return; }
    editDialog(key.id ? "编辑全站 Key" : "为用户创建 Key", [
      { name: "user_id", label: "归属用户", options: key.id ? [[String(key.user_id), "#" + key.user_id + "（已有归属）"]] : owners, value: String(key.user_id || owners[0][0]), required: true },
      { name: "name", label: "Key 名称", value: key.name || "", required: true },
      { name: "status", label: "Key 状态", options: [["active", "已启用"], ["disabled", "已停用"]], value: key.status || "active" },
      ...policyFields(key), ...limitFields(key),
      ...(key.id ? [{ name: "expiry_action", label: "到期更新方式", options: [["keep", "保留原到期时间"], ["replace", "重新设置"]], value: "keep" }] : []),
      { name: "expires_in_seconds", label: "从现在起的有效秒数", type: "number", min: 0, value: 0, hint: "新建时 0 为无到期时间；编辑默认保留原时间。" },
    ], async values => {
      const body = { ...keyInput(values), user_id: ownerID(values.user_id) };
      if (key.id && body.user_id !== key.user_id) throw new Error("现有 Key 不能在编辑时改变归属；无主旧 Key 请使用显式认领。");
      const result = await api.request("/api/admin/keys" + (key.id ? "/" + key.id : ""), { method: key.id ? "PUT" : "POST", body });
      return { secret: result.data?.key };
    }, "密钥不会扩大归属用户的权限。无主旧 Key 必须显式认领；已有 Key 不展示完整密钥。");
  }
  const rows = list(response.data);
  const search = el("input", { type: "search", "aria-label": "筛选全站 Key", placeholder: "按名称或用户 ID 筛选" });
  const host = el("div");
  function filter() {
    const q = search.value.toLowerCase();
    host.replaceChildren(table(["名称 / 前缀", "归属用户", "状态", "模型范围", "操作"], rows.filter(k =>
      String(k.name).toLowerCase().includes(q) || String(k.user_id).includes(q)), key => [
      el("div", {}, text("strong", key.name), text("code", key.key_prefix, "block")), key.user_id ? "#" + key.user_id : "无主旧 Key",
      badge(key.status), key.model_mode === "all" ? "用户授权范围内全部" : listText(key.models),
      actions(key.user_id ? button("编辑", () => edit(key), "quiet") : button("显式认领", () =>
        editDialog("认领无主旧 Key", [{ name: "user_id", label: "归属用户", options: owners, required: true },
          { name: "confirmation", label: "确认 Key ID", required: true, hint: String(key.id) }],
        values => {
          if (values.confirmation !== String(key.id)) throw new Error("请输入完整 Key ID 确认认领。");
          return api.request("/api/admin/keys/" + key.id + "/claim", { method: "POST", body: { user_id: ownerID(values.user_id) } });
        }, "仅认领当前无主的旧 Key。已有归属不可在此迁移。"), "quiet"),
        button("撤销", () => run(async () => {
          if (!confirm("撤销 Key " + key.name + "？正在使用它的客户端会失去后续调用权限。")) return;
          await api.request("/api/admin/keys/" + key.id, { method: "DELETE" });
          await navigate();
        }), "danger")),
    ]));
  }
  search.addEventListener("input", filter); filter();
  return el("div", {}, pageHeader("全站 API Key", "管理员维护归属与访问范围；真实权限仍由后端逐次校验。", button("+ 为用户创建 Key", () => edit(), "primary")),
    panel(el("div", { class: "filter-bar" }, search), host));
}
async function quotations() {
  const epoch = generation;
  const result = await api.request("/api/admin/quotations/current");
  if (epoch !== generation || !user) return el("div");
  const current = result.data;
  const output = el("div");
  const error = el("p", { class: "form-error", role: "alert" });
  const submit = el("button", { type: "submit", class: "button primary", disabled: !current.enabled }, "计算假设报价");
  const form = el("form", {}, el("div", { class: "form-grid" },
    field({ name: "version", label: "明确价格版本", value: current.version || "", required: true, full: true }),
    field({ name: "model", label: "精确模型名称", required: true }),
    field({ name: "group", label: "精确渠道组", value: "default", required: true }),
    field({ name: "input", label: "假设输入 Token", hint: "留空表示未知；0 是明确零。" }),
    field({ name: "output", label: "假设输出 Token", hint: "不包含于输入；输出已包括推理部分。" }),
    field({ name: "total", label: "假设总 Token", hint: "须显式提供，不由前端推导。" }),
    field({ name: "completeness", label: "用量完整度", options: [["unknown", "未知"], ["partial", "部分"], ["complete", "完整"]], value: "unknown" })),
    error, actions(submit));
  let controller, disposed = false;
  disposeView = () => { disposed = true; controller?.abort(); };
  form.addEventListener("submit", async event => {
    event.preventDefault();
    if (submit.disabled || disposed || epoch !== generation) return;
    error.textContent = ""; output.replaceChildren(); submit.disabled = true;
    const values = Object.fromEntries(new FormData(form));
    function count(value) {
      const raw = value.trim();
      if (!raw) return null;
      if (!/^(0|[1-9]\d*)$/.test(raw) || BigInt(raw) > 9223372036854775807n) throw new Error("Token 必须为非负 int64 十进制整数或留空。");
      return raw;
    }
    try {
      controller = new AbortController();
      const input = count(values.input), out = count(values.output), total = count(values.total);
      const reply = await api.request("/api/admin/quotations/calculate", { method: "POST", signal: controller.signal,
        body: { price_version: values.version, model: values.model, group: values.group,
          usage: { input_tokens: input, output_tokens: out, total_tokens: total, completeness: values.completeness,
            source: input === null && out === null && total === null ? "unknown" : "provider_reported", profile: current.usage_profile } } });
      if (disposed || epoch !== generation) return;
      output.replaceChildren(heading("假设售价结果", reply.data.reason),
        text("strong", reply.data.known ? moneyText(reply.data.amount_units) + " " + reply.data.currency : "未知，不能当作零费用"),
        text("p", "只读诊断，不发起生成、不扣费、不释放预算，也不证明输入数量真实发生。", "fine"));
    } catch (failure) {
      if (!disposed && epoch === generation) error.textContent = failure.message;
    } finally { if (!disposed && epoch === generation) submit.disabled = !current.enabled; }
  });
  return el("div", {}, pageHeader("报价诊断", "使用明确历史版本与假设数量进行只读计算；不替代实际账本。"),
    panel(text("p", current.enabled ? "当前报价已启用 · " + current.currency : "当前报价能力未启用。", "muted"), form), panel(output));
}
