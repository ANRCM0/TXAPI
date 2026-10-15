import { createClient, list, policyInput, userInput, keyInput, channelInput } from "./api.mjs";

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
    notice("Saved, but publication failed. Check Settings before relying on this change.", true);
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

async function run(action) {
  try { await action(); }
  catch (error) { notice(error.message, true); }
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

function editDialog(title, fields, save, description = "Changes are validated by the server.") {
  const error = el("p", { class: "form-error", role: "alert" });
  const submit = el("button", { type: "submit", class: "button primary" }, "Save changes");
  const form = el("form", {},
    heading(title, description), el("div", { class: "form-grid" }, fields.map(field)), error,
    actions(button("Cancel", () => dialog.close()), submit));
  const dialog = el("dialog", { class: "editor", "aria-label": title }, form);
  dialog.addEventListener("close", () => { form.reset(); dialog.remove(); });
  form.addEventListener("submit", async event => {
    event.preventDefault();
    submit.disabled = true;
    error.textContent = "";
    const epoch = generation;
    pendingPublication = false;
    try {
      const result = await save(Object.fromEntries(new FormData(form)));
      if (epoch !== generation) return;
      dialog.close();
      if (!pendingPublication) notice("Changes saved.");
      if (result?.secret) showSecret(result.secret);
      await navigate();
    } catch (failure) {
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
  ["playground", "Try a request", "04"], ["logs", "Request logs", "05"],
];
const adminPages = [["users", "Users", "06"], ["channels", "Channels", "07"], ["settings", "Settings", "08"]];
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

async function navigate() {
  if (!user) return;
  const epoch = ++generation;
  disposeView();
  disposeView = () => {};
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
    const content = await ({ overview, keys, models: modelPage, playground, logs, users, channels, settings })[page]();
    if (epoch === generation && user) view.replaceChildren(content);
  } catch (error) {
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
  const previous = button("Previous", () => { offset = Math.max(0, offset - limit); run(load); });
  const next = button("Next", () => { offset += limit; run(load); });
  const count = text("span", "", "muted");
  const model = el("input", { name: "model", placeholder: "Filter model", "aria-label": "Filter model" });
  const scope = el("select", { "aria-label": "Log scope" }, el("option", { value: "me" }, "My requests"),
    global ? el("option", { value: "admin" }, "All users (admin)") : null);
  const filter = el("form", { class: "filter-bar" }, scope, global ? model : null,
    el("button", { class: "button secondary" }, "Refresh"));
  async function load() {
    previous.disabled = next.disabled = true;
    const epoch = generation;
    const path = scope.value === "admin" ? "/api/admin/logs" : "/api/me/logs";
    const response = await api.request(`${path}?limit=${limit}&offset=${offset}${scope.value === "admin" && model.value ? `&model=${encodeURIComponent(model.value)}` : ""}`);
    if (epoch !== generation) return;
    const rows = list(response.data);
    host.replaceChildren(table(["Time / request", "Model", "Status", "Tokens", "Cost / CNY", "Latency"], rows, row => [
      el("div", {}, text("span", new Date(row.created_at).toLocaleString()), text("code", row.request_id, "block")),
      row.origin_model, badge(row.status), `${fmt(row.total_tokens)}${row.is_estimated ? " (est.)" : ""}`,
      `${fmt(row.cost)}${row.price_missing ? " / no price" : ""}`, `${fmt(row.latency_ms)} ms`,
    ]));
    previous.disabled = offset === 0; next.disabled = rows.length < limit;
    count.textContent = `Rows ${rows.length ? offset + 1 : 0}-${offset + rows.length}`;
  }
  filter.addEventListener("submit", event => { event.preventDefault(); offset = 0; run(load); });
  await load();
  return el("div", {}, pageHeader("Request history", "Retained access logs, not a durable billing ledger. New records may take a moment to appear."),
    panel(filter, host, actions(previous, count, next)));
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
  return el("div", {}, pageHeader("Settings & publication", "Saved database configuration and running snapshots are distinct.",
    button("Retry publication", () => run(async () => {
      await api.request("/api/admin/config-status/apply", { method: "POST" }); notice("Configuration applied."); await navigate();
    }), "primary")),
    publicationCard("Channels & credentials", status.data.channels), publicationCard("Prices & group rates", status.data.prices),
    text("p", `Publication epoch: ${status.data.epoch}. Revisions are per-process and restart at boot, not durable schema versions.`, "fine"),
    panel(heading("System model policy", "Applied directly at the next authentication; not part of snapshot revisions.",
      button("Edit policy", () => editDialog("System model policy", policyFields(policyValue), values => {
        const p = policyInput(values);
        return api.request("/api/admin/model-policy", { method: "PUT", body: { mode: p.model_mode, models: p.models } });
      }, "An empty allowlist blocks all model calls, including administrator API keys."))),
      text("p", policy.data.mode === "all" ? "All models allowed by system policy" : `Allowlist: ${listText(policy.data.models)}`)),
    panel(heading("Model pricing", "CNY per 1K tokens. This is basic usage accounting, not strict budget enforcement.",
      button("+ Set price", () => priceDialog())),
      table(["Model", "Input / 1K", "Output / 1K", "Actions"], list(prices.data), price => [
        text("code", price.model), fmt(price.input_price_per_1k), fmt(price.output_price_per_1k),
        actions(button("Edit", () => priceDialog(price), "quiet"), button("Delete", () => run(async () => {
          if (!confirm(`Remove price for ${price.model}? Missing prices are counted as zero cost.`)) return;
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
