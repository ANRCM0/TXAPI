import { createClient, list, policyInput, userInput, keyInput, channelInput, ledgerMoney, resolutionInput } from "./api.mjs";

const root = document.querySelector("#app");
let user = null;
let generation = 0;
let disposeView = () => {};
let noticeTimer;
let pendingPublication = false;
let fieldSequence = 0;
const api = createClient({
  onUnauthorized: () => signOut("Your session has expired. Please sign in again."),
  onPending: () => {
    pendingPublication = true;
    notice("Configuration saved; publication is pending. Open Settings to retry publication.", true);
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
const badge = value => text("span", value || "-", `badge ${["active", "enabled", "applied", "200"].includes(String(value)) ? "good" : ""}`);
const empty = message => text("p", message, "empty");
const actions = (...items) => el("div", { class: "actions" }, items);
const panel = (...items) => el("section", { class: "panel" }, items);
const heading = (title, subtitle, action) => el("div", { class: "section-heading" },
  el("div", {}, text("h2", title), text("p", subtitle, "muted")), action);
const listText = values => list(values).join(", ") || "None";

function notice(message, error = false) {
  clearTimeout(noticeTimer);
  let node = document.querySelector("#notice");
  if (!node) {
    node = el("div", { id: "notice", role: "status", "aria-live": "polite" });
    document.body.append(node);
  }
  node.className = error ? "notice error" : "notice";
  node.replaceChildren(text("span", message), button("Dismiss", () => node.remove(), "quiet"));
  noticeTimer = setTimeout(() => node.remove(), error ? 18000 : 6000);
}

function savedConfigurationMessage(error) {
  if (error.publication === "applied") return "Configuration saved and published, but its readback or management cache refresh failed. Refresh this page before making another change.";
  if (error.publication === "pending") return "Configuration saved; publication is pending. Open Settings to retry publication.";
  return "Configuration saved, but publication status is unknown. Refresh its status before taking further action.";
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
      return el("option", { value: key }, title);
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
    { name: "model_mode", label: "Model access", value: value.model_mode || "allowlist",
      options: [["allowlist", "Only listed models"], ["all", "All permitted by parent policy"]] },
    { name: "models", label: "Allowed models", value: listText(value.models) === "None" ? "" : listText(value.models),
      hint: "Comma or space separated. An empty allowlist denies all models." },
  ];
}
function limitFields(value = {}) {
  return [
    { name: "quota", label: "Quota (CNY)", type: "number", value: value.quota || 0, min: 0 },
    { name: "rpm_limit", label: "Requests / minute", type: "number", value: value.rpm_limit || 0, min: 0 },
    { name: "tpm_limit", label: "Tokens / minute (estimated)", type: "number", value: value.tpm_limit || 0, min: 0 },
    { name: "concurrency_limit", label: "Concurrent requests", type: "number", value: value.concurrency_limit || 0, min: 0 },
  ];
}

function editDialog(title, fields, save, description = "Changes are validated by the server.",
  { submitLabel = "Save changes", successMessage = "Changes saved." } = {}) {
  const error = el("p", { class: "form-error", role: "alert" });
  const submit = el("button", { type: "submit", class: "button primary" }, submitLabel);
  const form = el("form", {},
    heading(title, description), el("div", { class: "form-grid" }, fields.map(field)), error,
    actions(button("Cancel", () => dialog.close()), submit));
  const dialog = el("dialog", { class: "editor", "aria-label": title }, form);
  dialog.addEventListener("close", () => { form.reset(); dialog.remove(); });
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
      if (result?.secret) showSecret(result.secret);
      await navigate();
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
  const value = el("textarea", { readonly: true, rows: 3, "aria-label": "New API key", class: "secret" });
  value.value = secret;
  const dialog = el("dialog", { class: "editor", "aria-label": "Save your API key" },
    text("p", "ONE-TIME REVEAL", "eyebrow"),
    text("h2", "Save your API key"),
    text("p", "This key is shown only now. Store it securely; it cannot be retrieved later.", "muted"),
    value, actions(button("Copy key", () => run(async () => {
      if (!navigator.clipboard) { value.select(); notice("Select and copy the key manually."); return; }
      await navigator.clipboard.writeText(value.value);
      notice("Key copied. Clear your clipboard when finished.");
    })), button("I saved it", () => dialog.close(), "primary")));
  dialog.addEventListener("close", () => { value.value = ""; dialog.remove(); });
  document.body.append(dialog);
  dialog.showModal();
}

function table(columns, rows, render) {
  if (!rows.length) return empty("Nothing here yet. Create your first item to get started.");
  return el("div", { class: "table-wrap", tabindex: "0", "aria-label": "Scrollable data table" },
    el("table", {}, el("thead", {}, el("tr", {}, columns.map(name => el("th", { scope: "col" }, name)))),
      el("tbody", {}, rows.map(row => el("tr", {}, render(row).map(cell => el("td", {}, cell)))))));
}

function login(message = "") {
  const error = el("p", { class: "form-error", role: "alert" }, message);
  const submit = el("button", { class: "button primary", type: "submit" }, "Enter control room");
  const form = el("form", { class: "login-form" },
    text("p", "YOUR GATEWAY, UNDER CONTROL", "eyebrow"), text("h2", "Welcome back."),
    text("p", "Sign in to manage your access and keep traffic moving.", "muted"),
    field({ name: "username", label: "Username", required: true }),
    field({ name: "password", label: "Password", type: "password", required: true }),
    error, submit, text("p", "Accounts are created by your administrator. No public registration.", "fine"));
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
      text("span", "01 / ACCESS LAYER", "eyebrow"), text("h1", "One gateway.\nA clearer view."),
      text("p", "Models, credentials and people.\nConnected in one quiet control room.", "story-copy"),
      el("div", { class: "route-art", "aria-hidden": true },
        text("span", "OPENAI"), text("span", "ANTHROPIC"), text("span", "GEMINI"), text("strong", "TX / API")),
      text("p", "SELF-HOSTED / SINGLE BINARY / YOUR INFRASTRUCTURE", "story-footer")),
    el("section", { class: "login-access" }, form,
      text("p", "First installation? Initialize the administrator with -init-admin on the server.", "setup-note"))));
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
  ["overview", "Overview", "01"], ["keys", "My API keys", "02"], ["models", "Models", "03"],
  ["playground", "Try a request", "04"], ["logs", "Request logs", "05"], ["tasks", "Background tasks", "10"],
  ["routing", "Routing diagnostics", "11"],
];
const adminPages = [["users", "Users", "06"], ["channels", "Channels", "07"], ["settings", "Settings", "08"], ["billing", "Billing ledger", "09"]];
function shell() {
  const nav = [...pages, ...(user.role === "admin" ? adminPages : [])];
  root.replaceChildren(el("div", { class: "workspace" },
    el("aside", { class: "sidebar" },
      el("a", { href: "#overview", class: "wordmark" }, "TXAPI", text("span", "CONTROL ROOM", "wordmark-sub")),
      text("p", "WORKSPACE", "eyebrow"),
      el("nav", { "aria-label": "Main navigation" }, nav.map(([id, label, n]) =>
        el("a", { href: `#${id}`, "data-page": id }, text("span", n, "nav-number"), label))),
      el("div", { class: "sidebar-footer" }, text("span", "SELF-HOSTED GATEWAY", "eyebrow"), text("p", "Small core. Clear boundaries."))),
    el("div", { class: "main-column" },
      el("header", { class: "topbar" }, text("span", "WORKSPACE / LIVE API", "eyebrow"),
        actions(text("span", `${user.username} / ${user.role}`, "identity"),
          button("Sign out", () => run(async () => {
            try { await api.request("/api/auth/logout", { method: "POST" }); }
            finally { signOut(); }
          })),)),
      el("main", { id: "view", class: "content" }))));
}

function pageHeader(name, description, action) {
  return el("div", { class: "page-header" },
    el("div", {}, text("p", "TXAPI / CONTROL ROOM", "eyebrow"), text("h1", name), text("p", description, "muted")), action);
}

async function navigate({ refreshIdentity = true, recoverForbidden = true } = {}) {
  if (!user) return;
  const epoch = ++generation;
  disposeView();
  disposeView = () => {};
  try {
    if (refreshIdentity) {
      const current = await api.request("/api/me");
      if (epoch !== generation || !user) return;
      const changedRole = current.data.role !== user.role;
      user = current.data;
      if (changedRole) shell();
      else document.querySelector(".identity").textContent = `${user.username} / ${user.role}`;
    }
  } catch (error) {
    if (epoch === generation && user) {
      document.querySelector("#view").replaceChildren(panel(text("p", error.message, "form-error"), button("Try again", () => run(navigate))));
    }
    return;
  }
  const name = location.hash.slice(1) || "overview";
  const allowed = [...pages, ...(user.role === "admin" ? adminPages : [])];
  const page = allowed.find(item => item[0] === name)?.[0] || "overview";
  for (const link of document.querySelectorAll("[data-page]")) {
    if (link.dataset.page === page) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  const view = document.querySelector("#view");
  view.replaceChildren(text("p", "Loading live data...", "empty"));
  try {
    const content = await ({ overview, keys, models: modelPage, playground, logs, users, channels, settings, tasks: backgroundTasks, routing: routingDiagnostics, billing: billingLedger })[page]();
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
      pageHeader("Unable to load", "The server did not complete this request."),
      panel(text("p", error.message, "form-error"), button("Try again", () => run(navigate))));
  }
}

async function overview() {
  const [usageResponse, keysResponse] = await Promise.all([api.request("/api/me/usage"), api.request("/api/me/keys")]);
  const usage = usageResponse.data.usage;
  const stat = (label, value, suffix) => el("div", { class: "stat" }, text("span", label, "eyebrow"),
    text("strong", fmt(value)), text("small", suffix, "muted"));
  return el("div", {},
    pageHeader("Your gateway, at a glance.", "Live data from your account. Refresh after requests settle.", button("Refresh", () => run(navigate))),
    el("div", { class: "stats" }, stat("REQUESTS", usage.total_requests, "Your retained request history"),
      stat("TOKENS", usage.total_tokens, "Input + output"),
      stat("COST / CNY", usage.total_cost, "Observed usage, not a reliable ledger"),
      stat("ACTIVE KEYS", list(keysResponse.data).filter(key => key.status === "active" && (!key.expires_at || key.expires_at * 1000 > Date.now())).length, "Keys belonging to you")),
    el("div", { class: "overview-grid" },
      panel(text("p", "START HERE", "eyebrow"), text("h2", "From access to your first request."),
        el("ol", { class: "steps" }, [
          ["01", "Check your models", "Your permissions are the intersection of system, user and key policies.", "#models"],
          ["02", "Create an API key", "The full key is shown once. Keep it outside shared logs and screenshots.", "#keys"],
          ["03", "Send a request", "Use your API key, not your login session, with any compatible client.", "#playground"],
        ].map(([n, title, desc, href]) => el("li", {}, text("span", n, "step-number"),
          el("div", {}, el("a", { href }, title), text("p", desc, "muted")))))),
      panel(text("p", "ACCOUNT BOUNDARIES", "eyebrow"), text("h2", user.username),
        el("dl", { class: "facts" },
          text("dt", "Model policy"), text("dd", user.model_mode),
          text("dt", "Quota / used (CNY)"), text("dd", `${fmt(usageResponse.data.quota)} / ${fmt(usageResponse.data.used_quota)}`),
          text("dt", "User RPM"), text("dd", user.rpm_limit || "Not set"),
          text("dt", "Concurrency"), text("dd", user.concurrency_limit || "Server ceiling")),
        text("p", "Usage comes from retained, best-effort logs. Missing prices and estimated tokens affect totals. A zero quota is not a strict budget.", "fine"))));
}

async function keys() {
  const [response, available] = await Promise.all([api.request("/api/me/keys"), api.request("/api/me/models")]);
  const defaultPolicy = { model_mode: available.policy.mode, models: available.policy.models };
  function edit(key = {}) {
    editDialog(key.id ? "Edit API key" : "Create API key", [
      { name: "name", label: "Key name", value: key.name || "", required: true },
      { name: "status", label: "Status", value: key.status || "active", options: ["active", "disabled"] },
      ...policyFields(key.id ? key : defaultPolicy),
      ...limitFields(key),
      ...(key.id ? [{ name: "expiry_action", label: "Expiry update", value: "keep",
        options: [["keep", "Keep existing expiry"], ["replace", "Replace expiry from save time"]],
        hint: key.expires_at ? `Current expiry: ${new Date(key.expires_at * 1000).toLocaleString()}` : "Current key does not expire." }] : []),
      { name: "expires_in_seconds", label: "Expiry from now (seconds)", type: "number", min: 0,
        value: 0,
        hint: key.id ? "Used only when replacing expiry. 0 removes expiry; Keep preserves expired keys too." : "0 means no expiry.", full: true },
    ], async values => {
      const result = await api.request(`/api/me/keys${key.id ? `/${key.id}` : ""}`, {
        method: key.id ? "PUT" : "POST", body: keyInput(values),
      });
      return { secret: result.data.key };
    }, "Keys cannot widen your permissions. Zero limits inherit user limits; server ceilings still apply.");
  }
  return el("div", {}, pageHeader("My API keys", "Create narrowly scoped access for each client.", button("+ Create key", () => edit(), "primary")),
    panel(table(["Name / prefix", "Access", "Status", "Expires", "Actions"], list(response.data), key => [
      el("div", {}, text("strong", key.name), text("code", key.key_prefix, "block")),
      key.model_mode === "all" ? "All permitted models" : listText(key.models),
      badge(key.status), key.expires_at ? new Date(key.expires_at * 1000).toLocaleString() : "Never",
      actions(button("Edit", () => edit(key), "quiet"), button("Revoke", () => run(async () => {
        if (!confirm(`Revoke key "${key.name}"? Clients using it will stop working.`)) return;
        await api.request(`/api/me/keys/${key.id}`, { method: "DELETE" });
        notice("Key revoked."); await navigate();
      }), "danger")),
    ])));
}

async function modelPage() {
  const response = await api.request("/api/me/models");
  return el("div", {}, pageHeader("Your model library", "Model visibility follows your current user and system permissions."),
    el("div", { class: "model-grid" }, list(response.data).length ? response.data.map((name, i) =>
      panel(text("span", `MODEL / ${String(i + 1).padStart(2, "0")}`, "eyebrow"), text("h2", name),
        text("p", "OpenAI-compatible access through this gateway.", "muted"), el("a", { href: "#playground", class: "text-link" }, "Try a request >"))) :
      panel(empty("No explicit models are available. Ask your administrator to configure a channel and grant access."))),
    text("p", response.policy.mode === "all" ? "Wildcard channels may accept additional model names. Visibility does not guarantee healthy upstream capacity." :
      "Your key may narrow this list further. Availability depends on healthy upstream credentials.", "fine"));
}

async function playground() {
  const epoch = generation;
  const response = await api.request("/api/me/models");
  let controller;
  const output = el("pre", { class: "response", tabindex: "0", "aria-live": "polite" }, "Your response will appear here.");
  const submit = el("button", { type: "submit", class: "button primary" }, "Send request");
  const cancel = button("Cancel request", () => controller?.abort());
  cancel.disabled = true;
  const form = el("form", {}, heading("A small connection test", "A real non-streaming request, billed by your upstream."),
    el("div", { class: "form-grid" },
      field({ name: "model", label: "Model", value: list(response.data)[0] || "", required: true }),
      field({ name: "key", label: "API key", type: "password", required: true, hint: "Used only for this call. Never saved in browser storage." }),
      field({ name: "prompt", label: "Message", type: "textarea", value: "Say hello in one sentence.", required: true, full: true })),
    actions(submit, cancel));
  if (epoch === generation) {
    disposeView = () => { controller?.abort(); form.reset(); };
  }
  form.addEventListener("submit", async event => {
    event.preventDefault();
    const values = Object.fromEntries(new FormData(form));
    controller = new AbortController();
    submit.disabled = true; cancel.disabled = false;
    output.textContent = "Waiting for upstream...";
    try {
      const result = await api.request("/v1/chat/completions", { method: "POST", callKey: values.key, timeout: 610000,
        signal: controller.signal, body: { model: values.model, messages: [{ role: "user", content: values.prompt }], max_tokens: 128 } });
      output.textContent = JSON.stringify(result, null, 2);
    } catch (error) { output.textContent = error.message; }
    finally { submit.disabled = false; cancel.disabled = true; form.querySelector('[name="key"]').value = ""; }
  });
  return el("div", {}, pageHeader("Try a request", "Verify key access, model routing and usage end to end."),
    el("div", { class: "playground-grid" }, panel(form), panel(heading("Response", "Raw JSON from the gateway"), output)),
    text("p", `Client base URL: ${location.origin}/v1. Login sessions cannot be used as API keys.`, "fine"));
}

async function logs() {
  const global = user.role === "admin";
  const host = el("div");
  const limit = 50;
  let offset = 0;
  let loadVersion = 0;
  let rowCount = 0;
  const previous = button("Previous", () => run(() => load(Math.max(0, offset - limit))));
  const next = button("Next", () => run(() => load(offset + limit)));
  const count = text("span", "", "muted");
  const model = el("input", { name: "model", placeholder: "Filter model", "aria-label": "Filter model" });
  const scope = el("select", { "aria-label": "Log scope" }, el("option", { value: "me" }, "My requests"),
    global ? el("option", { value: "admin" }, "All users (admin)") : null);
  const filter = el("form", { class: "filter-bar" }, scope, global ? model : null,
    el("button", { class: "button secondary" }, "Refresh"));
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
      host.replaceChildren(table(["Time / request", "Model", "Status", "Tokens", "Cost / CNY", "Latency"], rows, row => [
        el("div", {}, text("span", new Date(row.created_at).toLocaleString()), text("code", row.request_id, "block")),
        row.origin_model, badge(row.status), `${fmt(row.total_tokens)}${row.is_estimated ? " (est.)" : ""}`,
        el("div", {}, `${fmt(row.cost)}${row.price_missing ? " / no price" : ""}`,
          row.billing_state ? el("span", { class: "block", title: row.billing_id }, row.billing_state === "pending" ? "Budget held / reconciliation pending" : row.billing_state) : null,
          row.price_version ? el("code", { class: "block", title: row.price_version }, `Price ${row.price_version.slice(0, 12)}`) : null),
        `${fmt(row.latency_ms)} ms`,
      ]));
      count.textContent = `Rows ${rows.length ? offset + 1 : 0}-${offset + rows.length}`;
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
  return el("div", {}, pageHeader("Request history", "Retained access logs, not a durable billing ledger. New records may take a moment to appear."),
    panel(filter, host, actions(previous, count, next)));
}

async function billingLedger() {
  let offset = 0;
  let loadVersion = 0;
  let rowCount = 0;
  const limit = 50;
  const host = el("div");
  const state = el("select", { "aria-label": "Billing state" },
    [["pending", "Pending reconciliation"], ["", "All states"], ["settled", "Settled"], ["active", "In flight"], ["reserved", "Reserved"]]
      .map(([value, label]) => el("option", { value }, label)));
  const owner = el("input", { "aria-label": "Billing user ID", placeholder: "User ID (optional)", inputmode: "numeric" });
  const count = text("span", "", "muted");
  const previous = button("Previous", () => run(() => load(Math.max(0, offset - limit))));
  const next = button("Next", () => run(() => load(offset + limit)));
  const filter = el("form", { class: "filter-bar" }, state, owner, el("button", { class: "button secondary" }, "Refresh ledger"));
  async function load(targetOffset = offset) {
    const epoch = generation;
    const version = ++loadVersion;
    previous.disabled = next.disabled = true;
    const query = new URLSearchParams({ state: state.value, limit, offset: targetOffset });
    if (owner.value.trim()) query.set("user_id", owner.value.trim());
    try {
      const response = await api.request(`/api/admin/billing/reservations?${query}`);
      if (epoch !== generation || version !== loadVersion) return;
      const rows = list(response.data);
      offset = targetOffset;
      rowCount = rows.length;
      host.replaceChildren(rows.length ? table(["Reservation / owner", "Model / price version", "State", "Hold / charge", "Actions"], rows, row => [
        el("div", {}, text("code", row.id), text("span", `User ${row.user_id} / Key ${row.token_id}`, "block")),
        el("div", {}, row.origin_model, text("code", row.price_version, "block")),
        el("div", {}, badge(row.state), text("span", row.reason, "block")),
        el("div", {}, `Hold ${ledgerMoney(row.reserved_units)}`, text("span", `Charged ${ledgerMoney(row.charge_units)}`, "block")),
        button("Inspect", () => run(() => inspectBilling(row.id)), "quiet"),
      ]) : empty("No reservations match this filter. Pending charges remain held until verified or explicitly waived."));
      count.textContent = `Rows ${rows.length ? offset + 1 : 0}-${offset + rows.length}`;
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
  return el("div", {}, pageHeader("Billing ledger", "Durable reservations and audited decisions, independent of access-log retention."),
    panel(text("p", "Amounts use the configured currency. Pending is not free. Only expired pending holds in strict mode can be resolved.", "muted"),
      filter, host, actions(previous, count, next)));
}

async function inspectBilling(id) {
  const epoch = generation;
  const response = await api.request(`/api/admin/billing/reservations/${encodeURIComponent(id)}`);
  if (epoch !== generation || !user) return;
  const { reservation: row, attempts, resolution, price_snapshot: snapshot } = response.data;
  const dialog = el("dialog", { class: "editor billing-detail", "aria-label": "Billing reservation" });
  dialog.addEventListener("close", () => dialog.remove());
  dialog.append(heading("Billing reservation", row.id, button("Close", () => dialog.close())),
    text("p", `${row.origin_model} / User ${row.user_id} / Key ${row.token_id}`),
    text("p", `State: ${row.state}. Hold: ${ledgerMoney(row.reserved_units)}. Charged: ${ledgerMoney(row.charge_units)}.`, "muted"),
    text("p", `Provider attempt envelopes: ${ledgerMoney(row.provider_held_units)} / ${ledgerMoney(row.provider_budget_units)}. Cumulative upper bounds, not actual provider invoices or user holds.`, "muted"),
    text("p", `Lease deadline: ${new Date(row.lease_until).toLocaleString()}. ${row.reason}`, "fine"),
    heading("Upstream attempts", "Receipt cost uses the selling-price snapshot; provider envelopes use independent contracts. Neither is an additional user charge."),
    table(["Attempt / target", "Outcome", "Tokens", "Receipt cost", "Provider envelope"], list(attempts), attempt => [
      el("div", {}, `#${attempt.number} / Channel ${attempt.channel_id}`, text("span", `Credential ${attempt.credential_id} / ${attempt.actual_model} / ${attempt.group}`, "block")),
      `${attempt.status} / ${attempt.termination || attempt.state}`, `${attempt.prompt_tokens} + ${attempt.output_tokens}`,
      attempt.cost_known ? ledgerMoney(attempt.cost_units) : "Unknown (not free)",
      el("div", {}, attempt.provider_contract_version ? ledgerMoney(attempt.provider_bound_units) : "No historical contract",
        attempt.provider_contract_version ? el("details", {}, el("summary", {}, `Snapshot ${attempt.provider_contract_version.slice(0, 12)}`),
          text("pre", attempt.provider_contract_snapshot)) : null),
    ]));
  if (resolution) {
    dialog.append(heading("Final administrator decision", `${resolution.action} / Admin ${resolution.actor_id}`),
      text("p", resolution.reason), text("p", `Evidence: ${resolution.evidence}`, "fine"),
      text("p", `Calculated: ${resolution.cost_known ? ledgerMoney(resolution.calculated_units) : "Unknown"}. Charged: ${ledgerMoney(resolution.charge_units)}.`, "muted"));
  } else if (row.can_resolve) {
    dialog.append(actions(button("Resolve held charge", () => {
      dialog.close();
      editDialog("Resolve held charge", [
        { name: "action", label: "Decision", value: "settle_usage", options: [
          ["settle_usage", "Settle verified usage"], ["cap_usage", "Cap verified excess cost to the hold"], ["waive", "Waive charge and release hold"],
        ] },
        { name: "prompt_tokens", label: "Verified input tokens", hint: "Required for usage decisions; leave blank for waiver." },
        { name: "completion_tokens", label: "Verified output tokens", hint: "Enter an explicit 0 if none." },
        { name: "reason", label: "Reason", type: "textarea", required: true, full: true },
        { name: "evidence", label: "Verification / approval reference", type: "textarea", required: true, full: true,
          hint: "Do not include credentials or private request content." },
        { name: "confirmation", label: "Confirm full billing ID", required: true, full: true, hint: row.id },
      ], values => api.request(`/api/admin/billing/reservations/${encodeURIComponent(row.id)}/resolve`, {
        method: "POST", body: resolutionInput(values, row.id),
      }), "This final decision updates both budgets atomically and cannot be edited. Historical prices and the recorded target apply. A waiver does not mean upstream execution was free.",
      { submitLabel: "Commit decision", successMessage: "Billing decision committed." });
    }, "primary")));
  } else {
    dialog.append(text("p", "Resolution requires strict mode and an expired pending lease. Settled decisions cannot be rewritten. Refresh the ledger after the deadline.", "fine"));
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
    editDialog(value.id ? `Edit ${value.username}` : "Create user", [
      { name: "username", label: "Username", value: value.username || "", required: true },
      { name: "password", label: value.id ? "New password (optional)" : "Initial password", type: "password", required: !value.id,
        hint: "12-72 bytes. Share via a secure channel." },
      { name: "role", label: "Role", options: ["user", "admin"], value: value.role || "user" },
      { name: "status", label: "Status", options: ["active", "disabled"], value: value.status || "active" },
      ...policyFields(value), ...limitFields(value),
    ], values => api.request(`/api/admin/users${value.id ? `/${value.id}` : ""}`, { method: value.id ? "PUT" : "POST", body: userInput(values) }),
    "Disabling a user blocks new key and session requests. The last active administrator is protected.");
  }
  return el("div", {}, pageHeader("People & permissions", "Grant only the models and capacity each person needs.", button("+ Create user", () => edit(), "primary")),
    panel(table(["User", "Role", "Model access", "Status", "Limits", "Actions"], list(response.data), value => [
      text("strong", value.username), value.role, value.model_mode === "all" ? "All" : listText(value.models), badge(value.status),
      `${value.rpm_limit || "-"} RPM / ${value.concurrency_limit || "-"} concurrent`,
      button("Edit access", () => edit(value), "quiet"),
    ])));
}

async function channels() {
  const [response, status] = await Promise.all([api.request("/api/admin/channels"), api.request("/api/admin/config-status")]);
  function edit(value = {}) {
    editDialog(value.id ? `Edit ${value.name}` : "Create channel", [
      { name: "name", label: "Name", value: value.name || "", required: true },
      { name: "type", label: "Protocol", options: ["openai", "anthropic", "gemini"], value: value.type || "openai" },
      { name: "base_url", label: "Upstream base URL", value: value.base_url || "", required: true },
      { name: "group_name", label: "Group", value: value.group_name || "default" },
      { name: "models", label: "Models", value: list(value.models).join(", "), required: true, full: true },
      ...(!value.id ? [{ name: "api_key", label: "First upstream API key", type: "password", required: true, full: true }] : []),
      { name: "priority", label: "Priority", type: "number", value: value.priority || 0 },
      { name: "weight", label: "Weight", type: "number", value: value.weight || 1, min: 1 },
      { name: "status", label: "Status", options: ["enabled", "disabled"], value: value.status || "enabled" },
      { name: "model_mapping", label: "Model mapping (JSON)", type: "textarea", value: JSON.stringify(value.model_mapping || {}, null, 2), full: true },
    ], values => api.request(`/api/admin/channels${value.id ? `/${value.id}` : ""}`, {
      method: value.id ? "PUT" : "POST", body: channelInput(values),
    }), "Configuration is saved first, then published. Edit individual keys in the credential pool; channel edits preserve it.");
  }
  const content = [pageHeader("Upstream channels", "Route models through healthy credential pools.", button("+ Create channel", () => edit(), "primary")),
    publicationCard("Channel publication", status.data.channels)];
  for (const value of list(response.data)) {
    content.push(panel(
      heading(value.name, `${value.type.toUpperCase()} / ${value.base_url}`, actions(badge(value.status),
        button("Edit channel", () => edit(value)), button("Delete", () => run(async () => {
          if (!confirm(`Delete channel "${value.name}" and all of its credentials?`)) return;
          pendingPublication = false;
          await api.request(`/api/admin/channels/${value.id}`, { method: "DELETE" });
          if (!pendingPublication) notice("Channel deleted and published."); await navigate();
        }), "danger"))),
      text("p", `${listText(value.models)} / Priority ${value.priority} / Weight ${value.weight} / Group ${value.group_name}`, "fine"),
      heading("Credential pool", "Keys are masked. Recovery and disabled state are enforced by the server.",
        button("+ Add credential", () => credentialDialog(value.id))),
      table(["Credential / masked key", "Status", "Weight", "Failures", "Actions"], list(value.credentials), credential => [
        el("div", {}, text("strong", credential.name), text("code", credential.api_key, "block")), badge(credential.status),
        credential.weight, credential.fail_count, actions(
          button("Edit", () => credentialDialog(value.id, credential), "quiet"),
          button(credential.status === "disabled" ? "Enable" : "Disable", () => setCredential(credential, credential.status === "disabled" ? "active" : "disabled"), "quiet"),
          ["cooldown", "banned"].includes(credential.status) ? button("Recover", () => setCredential(credential, "active"), "quiet") : null,
          button("Delete", () => run(async () => {
            if (!confirm(`Delete credential "${credential.name}"? The last credential cannot be deleted.`)) return;
            await api.request(`/api/admin/credentials/${credential.id}`, { method: "DELETE" }); await navigate();
          }), "danger")),
      ])));
  }
  if (!list(response.data).length) content.push(panel(empty("No channels configured. Add an upstream and the models you want to expose.")));
  return el("div", {}, content);
}

function credentialDialog(channelID, value = {}) {
  editDialog(value.id ? "Edit credential" : "Add credential", [
    { name: "name", label: "Name", value: value.name || "", required: true },
    { name: "weight", label: "Weight", value: value.weight || 1, type: "number", min: 1 },
    { name: "api_key", label: value.id ? "Replacement key (optional)" : "Upstream key", type: "password", required: !value.id, full: true },
  ], values => api.request(value.id ? `/api/admin/credentials/${value.id}` : `/api/admin/channels/${channelID}/credentials`, {
    method: value.id ? "PUT" : "POST", body: { name: values.name, weight: Number(values.weight), ...(values.api_key ? { api_key: values.api_key } : {}) },
  }), "Leave replacement key blank to keep the existing key. Existing secrets are never retrieved.");
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
    publication.replaceChildren(publicationCard("Channels & credentials", value.channels), publicationCard("Prices & group rates", value.prices),
      text("p", `Publication epoch: ${value.epoch}. Revisions are per-process and restart at boot, not durable schema versions.`, "fine"));
  }
  showPublication(status.data);
  const retry = button("Retry publication", () => run(async () => {
    if (retry.disabled) return;
    retry.disabled = true;
    const epoch = generation;
    try {
      const response = await api.request("/api/admin/config-status/apply", { method: "POST" });
      if (epoch !== generation) return;
      showPublication(response.data);
      notice("Configuration applied.");
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
  return el("div", {}, pageHeader("Settings & publication", "Saved database configuration and running snapshots are distinct.",
    retry), publication,
    panel(heading("System model policy", "Applied directly at the next authentication; not part of snapshot revisions.",
      button("Edit policy", () => editDialog("System model policy", policyFields(policyValue), values => {
        const p = policyInput(values);
        return api.request("/api/admin/model-policy", { method: "PUT", body: { mode: p.model_mode, models: p.models } });
      }, "An empty allowlist blocks all model calls, including administrator API keys."))),
      text("p", policy.data.mode === "all" ? "All models allowed by system policy" : `Allowlist: ${listText(policy.data.models)}`)),
    panel(heading("Model pricing", "CNY per 1K tokens. Applied snapshots are immutable; strict settlement also requires its configured ledger and fee ceiling.",
      button("+ Set price", () => priceDialog())),
      table(["Model", "Input / 1K", "Output / 1K", "Actions"], list(prices.data), price => [
        text("code", price.model), fmt(price.input_price_per_1k), fmt(price.output_price_per_1k),
        actions(button("Edit", () => priceDialog(price), "quiet"), button("Delete", () => run(async () => {
          if (!confirm(`Remove price for ${price.model}? Strict mode rejects new calls without a matching price; legacy may count them as zero.`)) return;
          await api.request(`/api/admin/prices/${encodeURIComponent(price.model)}`, { method: "DELETE" }); await navigate();
        }), "danger")),
      ])),
    panel(heading("Group rates", "Complete replacement; omitted groups default to 1.0.", button("Edit rates", () =>
      editDialog("Group rates", [{ name: "rates", label: "Rates (JSON)", type: "textarea", full: true, value: JSON.stringify(prices.group_rates, null, 2) }],
        values => {
          const rates = JSON.parse(values.rates);
          if (!rates || Array.isArray(rates) || typeof rates !== "object" ||
            Object.values(rates).some(rate => typeof rate !== "number" || !Number.isFinite(rate) || rate <= 0)) {
            throw new Error("Rates must be a JSON object of positive finite numbers.");
          }
          return api.request("/api/admin/group-rates", { method: "PUT", body: rates });
        }))), text("pre", JSON.stringify(prices.group_rates, null, 2), "code-block")),
    panel(heading("Deployment boundaries", "Server settings are managed in YAML / environment, not in this page."),
      text("p", "Use HTTPS at a reverse proxy. Restrict /metrics by source IP. Back up SQLite only after stopping writes; never downgrade an upgraded database in place.", "muted"),
      text("p", "Concurrency and rate limits are single-process. Access logs are best-effort and subject to retention; they are not the S5 reliable ledger.", "fine")));
}

function priceDialog(value = {}) {
  editDialog("Model price", [
    { name: "model", label: "Model or pattern", value: value.model || "", required: true, full: true },
    { name: "input_price_per_1k", label: "Input / 1K (CNY)", type: "number", value: value.input_price_per_1k || 0, min: 0 },
    { name: "output_price_per_1k", label: "Output / 1K (CNY)", type: "number", value: value.output_price_per_1k || 0, min: 0 },
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
  const scope = el("select", { "aria-label": "Routing scope" }, el("option", { value: "me" }, "My requests"),
    user.role === "admin" ? el("option", { value: "admin" }, "All users (admin)") : null);
  const requestID = el("input", { maxlength: 64, "aria-label": "Exact Request ID", placeholder: "Exact Request ID" });
  const owner = el("input", { type: "number", min: 1, "aria-label": "Routing owner ID", placeholder: "Owner ID (admin)", disabled: true });
  const key = el("input", { type: "number", min: 1, "aria-label": "Routing Key ID", placeholder: "Key ID" });
  const filter = el("form", { class: "filter-bar" }, scope, requestID, owner, key, el("button", { class: "button secondary" }, "Refresh routes"));
  const previous = button("Previous", () => load(Math.max(0, offset - 50))), next = button("Next", () => load(offset + 50));
  const base = () => "/api/" + scope.value + "/routing";
  const date = value => value ? new Date(value).toLocaleString() : "Not observed";
  const target = row => row.channel_id ? "Channel " + row.channel_id + " / Key " + row.credential_id : "Not selected";
  async function load(targetOffset = offset) {
    const ownVersion = ++version;
    ++detailVersion; detailController?.abort(); detail.replaceChildren();
    controller?.abort(); controller = new AbortController();
    previous.disabled = next.disabled = true; error.textContent = ""; state.textContent = "Loading retained observations...";
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
        "Recording enabled. Up to " + response.capacity + " completed HTTP scopes for " + response.retention_seconds / 60 + " minutes; restart or capacity eviction may remove earlier rows." :
        "Recording is disabled. Enable server.route_diagnostics and restart to retain recent HTTP routing facts.";
      const rows = list(response.data);
      host.replaceChildren(rows.length ? table(["Request / finished", "Owner / Key", "Model / operation", "Result / observations", "Actions"], rows, row => [
        el("div", {}, text("code", row.request_id || "Request ID not retained", "block"), date(row.finished_at)),
        "User " + row.user_id + " / Key " + row.token_id, el("div", {}, row.model || "Not parsed", text("span", row.operation, "block")),
        el("div", {}, row.reason.replaceAll("_", " "), text("span", list(row.selections).length + " choices / " + list(row.attempts).length + " attempts", "block")),
        button("Inspect routing", () => inspect(row.id), "quiet"),
      ]) : empty("No matching retained observations. Missing data does not prove that a request never ran."));
      count.textContent = "Rows " + (rows.length ? offset + 1 : 0) + "-" + (offset + rows.length);
    } catch (failure) {
      if (current() && ownVersion === version && failure.name !== "AbortError") { error.textContent = failure.message; state.textContent = "Unable to load observations."; }
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
      detail.replaceChildren(heading("Routing facts", row.id),
        text("p", "Read-only observations. Selecting a target is not proof of dispatch; protocol completion is not proof of client consumption or settlement.", "muted"),
        el("dl", { class: "facts" },
          text("dt", "Request ID"), text("dd", row.request_id || "Not retained"),
          text("dt", "Started / finished"), text("dd", date(row.started_at) + " / " + date(row.finished_at)),
          text("dt", "Gateway result"), text("dd", String(row.status) + " / " + row.reason.replaceAll("_", " ")),
          text("dt", "Client HTTP"), text("dd", row.committed ? "Committed / " + row.http_status : "Not committed"),
          text("dt", "Dispatch observation"), text("dd", row.phase),
          text("dt", "Protocol complete"), text("dd", String(row.protocol_complete)),
          text("dt", "Scope interrupted"), text("dd", String(row.interrupted)),
          text("dt", "Metadata truncated"), text("dd", String(row.truncated))),
        heading("Selections", "Existing eligibility and picker facts; a choice may still be rejected before any attempt."),
        list(row.selections).length ? table(["Outcome / picker", "Eligible / candidates", "Credential counts", "Priority / duration", ...(admin ? ["Target (admin)"] : [])], list(row.selections), choice => [
          choice.outcome + " / " + choice.picker, choice.eligible_channels + " / " + choice.candidate_channels + " (" + choice.selection_pool_channels + " in priority pool)",
          choice.eligible_credentials + " usable / " + choice.total_credentials + " total; " + choice.tried_credentials + " tried / " + choice.unavailable_credentials + " unavailable",
          (choice.priority_fallback ? "Priority fallback" : "Current priority pool") + " / " + choice.duration_micros + " us",
          ...(admin ? [target(choice)] : []),
        ]) : empty("No target selection was observed in this scope."),
        heading("Upstream attempts", "Observed attempts and commit facts. No raw upstream errors, request content or credentials are retained."),
        list(row.attempts).length ? table(["Attempt / result", "Dispatch / termination", "Delivery / protocol", ...(admin ? ["Target / model (admin)"] : [])], list(row.attempts), attempt => [
          "#" + attempt.number + " / " + attempt.status, attempt.phase + " / " + attempt.termination,
          (attempt.committed ? "Committed / " + attempt.http_status : "Not committed") + "; protocol complete: " + attempt.protocol_complete,
          ...(admin ? [el("div", {}, target(attempt), text("span", attempt.actual_model || "Not observed", "block"))] : []),
        ]) : empty("No upstream attempt was observed in this scope."));
    } catch (failure) { if (current() && ownVersion === detailVersion && failure.name !== "AbortError") error.textContent = failure.message; }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); load(0); });
  scope.addEventListener("change", () => { owner.disabled = scope.value !== "admin"; offset = 0; hasMore = false; load(0); });
  await load(0);
  return el("div", {}, pageHeader("Routing diagnostics", "Recent completed HTTP scopes, linked by Request ID. Each invocation remains separate."),
    panel(text("p", "Ephemeral diagnostics are not a request ledger or recovery log. Active scopes, authentication failures, native background controls and WebSocket turns are outside this view.", "muted"),
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
  const scope = el("select", { "aria-label": "Task scope" }, el("option", { value: "me" }, "My tasks"),
    user.role === "admin" ? el("option", { value: "admin" }, "All users (metadata only)") : null);
  const stage = el("select", { "aria-label": "Task stage" }, ["", "queued", "dispatching", "remote_active", "terminal", "uncertain"]
    .map(value => el("option", { value }, value || "All stages")));
  const owner = el("input", { type: "number", min: 1, "aria-label": "Task owner ID", placeholder: "Owner ID (admin)", disabled: true });
  const key = el("input", { type: "number", min: 1, "aria-label": "Creation Key ID", placeholder: "Creation Key ID" });
  const filter = el("form", { class: "filter-bar" }, scope, stage, owner, key, el("button", { class: "button secondary" }, "Refresh tasks"));
  const previous = button("Previous", () => load(Math.max(0, offset - 50)));
  const next = button("Next", () => load(offset + 50));
  const base = () => "/api/" + scope.value + "/background";
  const date = value => value ? new Date(value).toLocaleString() : "Not recorded";
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
      host.replaceChildren(table(["Task / created", "Owner / Key", "Model / stage", "Content / events", "Actions"], list(response.data), row => [
        el("div", {}, text("code", row.id, "block"), date(row.created_at)),
        "User " + row.user_id + " / Key " + row.token_id,
        el("div", {}, row.model, text("span", row.stage + (row.cancel_requested ? " / cancel requested" : ""), "block")),
        el("div", {}, row.content_available ? "Retained (not displayed)" : "Expired or deleted", text("span", String(row.event_count) + (row.replay_pending ? " / replay pending" : "") , "block")),
        button("Inspect task", () => inspect(row.id), "quiet")
      ]));
      count.textContent = "Rows " + (response.data?.length ? offset + 1 : 0) + "-" + (offset + list(response.data).length);
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
      const requestCancel = button("Request cancellation", async () => {
        if (!current() || ownVersion !== detailVersion || !confirm("Request cancellation for " + row.id + "? This does not prove remote work has stopped.")) return;
        requestCancel.disabled = true;
        try {
          await api.request("/api/me/background/" + encodeURIComponent(row.id) + "/cancel", { method: "POST", body: { revision: row.revision }, signal: detailController.signal });
          if (!current() || ownVersion !== detailVersion) return;
          notice("Cancellation recorded. Refresh to observe the remote outcome; uncertain work remains uncertain.");
          await load();
        } catch (failure) {
          if (!current() || ownVersion !== detailVersion) return;
          if (failure.status === 409) await inspect(row.id);
          if (current()) error.textContent = failure.message;
        } finally { if (requestCancel.isConnected) requestCancel.disabled = false; }
      }, "danger");
      detail.replaceChildren(heading("Task metadata", row.id),
        text("p", "Stage: " + row.stage + ". Terminal is not proof of success or settlement. No content or upstream handle is exposed.", "muted"),
        el("dl", { class: "facts" },
          text("dt", "Revision"), text("dd", row.revision),
          text("dt", "Deadline"), text("dd", date(row.deadline)),
          text("dt", "Content expiry"), text("dd", date(row.content_expires_at)),
          text("dt", "Metadata cleanup eligibility"), text("dd", date(row.metadata_expires_at)),
          text("dt", "Last persisted sequence"), text("dd", row.last_sequence === null ? "None" : String(row.last_sequence)),
          text("dt", "Replay pending"), text("dd", String(row.replay_pending)),
          text("dt", "Cancellation requested"), text("dd", String(row.cancel_requested))),
        row.can_cancel && scope.value === "me" ? requestCancel : text("p", "Read-only or no further cancellation available.", "fine"));
    } catch (failure) { if (current() && ownVersion === detailVersion && failure.name !== "AbortError") error.textContent = failure.message; }
  }
  filter.addEventListener("submit", event => { event.preventDefault(); load(0); });
  scope.addEventListener("change", () => { owner.disabled = scope.value !== "admin"; host.replaceChildren(); offset = 0; hasMore = false; load(0); });
  await load(0);
  return el("div", {}, pageHeader("Background tasks", "Session-authorized metadata. Call-Key content access and billing remain separate."),
    panel(text("p", "Cancel requests are not refunds or proof that remote work stopped. Expired metadata may survive for safe recovery.", "muted"), filter, error, host, actions(previous, count, next)), panel(detail));
}
