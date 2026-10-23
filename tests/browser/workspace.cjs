const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require("playwright");

(async () => {
  let role = "user", enabled = true, balanceAvailable = true;
  const calls = [], writes = [], errors = [];
  const money = "9223372036854775807";
  const version = "a".repeat(64), now = Date.now();
  const keys = [{ id: 99, name: "Legacy unowned", user_id: 0, key_prefix: "sk-old-masked", status: "disabled", model_mode: "allowlist", models: [] }];
  const amounts = [
    { id: "own-pending", state: "pending", original_reserved_units: money, held_units: money, charge_units: null },
    { id: "own-zero", state: "settled", original_reserved_units: "100000001", held_units: "0", charge_units: "0" },
  ].map(row => ({ ...row, origin_model: "m1", token_id: 7, price_version: version, created_at: new Date(now).toISOString(), lease_until: now - 1000 }));
  const record = { id: "usage_" + "b".repeat(32), request_id: "req-demo", model: "m1", operation: "responses",
    transport: "websocket", state: "sealed", created_at: now, attempt_count: 1,
    host: { protocol_status: "completed", protocol_complete: true, delivery_committed: true } };
  const user = () => ({ id: role === "admin" ? 1 : 2, username: "fixture", role, status: "active", model_mode: "all", models: [] });
  const send = (res, code, value) => { res.writeHead(code, { "Content-Type": "application/json", "Cache-Control": "no-store" }); res.end(JSON.stringify(value)); };
  const server = http.createServer(async (req, res) => {
    const u = new URL(req.url, "http://127.0.0.1");
    calls.push({ method: req.method, url: req.url });
    const asset = u.pathname === "/" ? "index.html" : u.pathname.startsWith("/ui/") ? u.pathname.slice(4) : "";
    if (["index.html", "app.mjs", "api.mjs", "workspace.mjs", "app.css"].includes(asset)) {
      res.writeHead(200, { "Content-Type": asset.endsWith(".mjs") ? "text/javascript" : asset.endsWith(".css") ? "text/css" : "text/html",
        "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'" });
      res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", asset))); return;
    }
    let body = null;
    if (req.method === "POST" || req.method === "PUT") {
      let raw = ""; for await (const part of req) raw += part;
      body = raw ? JSON.parse(raw) : {};
      if (!u.pathname.includes("/auth/")) writes.push({ path: u.pathname, body });
    }
    if (u.pathname === "/api/auth/login") return send(res, 200, { session_token: "fixture-management-session", user: user() });
    if (u.pathname === "/api/auth/logout") return send(res, 200, {});
    if (u.pathname.startsWith("/api/admin/") && role !== "admin") return send(res, 403, { error: { message: "fixture-forbidden" } });
    if (u.pathname === "/api/me") return send(res, 200, { data: user() });
    if (u.pathname === "/api/me/usage") return send(res, 200, { data: { usage: { total_requests: 2, total_tokens: 6, total_cost: .001 }, quota: 1, used_quota: .001 } });
    if (u.pathname === "/api/me/logs") return send(res, 200, { data: [
      { created_at: new Date(now).toISOString(), request_id: "req-demo", origin_model: "m1", status: 200, total_tokens: 6, latency_ms: 10 },
    ] });
    if (u.pathname === "/api/me/keys") return send(res, 200, { data: [] });
    if (u.pathname === "/api/me/models") return send(res, 200, { data: ["m1", "<img src=x onerror=alert(1)>"], policy: { mode: "all", models: [] } });
    if (u.pathname === "/api/me/billing/summary") return send(res, balanceAvailable ? 200 : 503,
      balanceAvailable ? { data: { unit_scale: "1000000000", balance_known: true, posted_used_units: "0", held_units: money, pending_count: "1", as_of: now } } : { error: { message: "fixture-unavailable" } });
    if (u.pathname === "/api/me/billing/reservations") return send(res, 200, { data: amounts, has_more: false });
    if (u.pathname.startsWith("/api/me/billing/reservations/")) return send(res, 200, { data: { reservation: amounts.find(r => u.pathname.endsWith(r.id)) } });
    if (u.pathname === "/api/me/usage-records" || u.pathname === "/api/admin/usage-records") {
      return send(res, 200, { enabled, data: enabled ? [record] : [], has_more: false });
    }
    if (/\/usage-records\//.test(u.pathname)) return send(res, 200, { data: { record, attempts: [{ number: 1, state: "sealed",
      usage: { input_tokens: "0", output_tokens: null, total_tokens: money, cached_input_tokens: null, cache_write_input_tokens: null,
        reasoning_output_tokens: "0", completeness: "partial", source: "provider_reported" } }] } });
    if (u.pathname === "/api/admin/channels") return send(res, 200, { data: [] });
    if (u.pathname === "/api/admin/users") return send(res, 200, { data: [user(), { id: 2, username: "member", status: "active" }] });
    if (u.pathname === "/api/admin/keys" && req.method === "GET") return send(res, 200, { data: keys });
    if (u.pathname === "/api/admin/keys" && req.method === "POST") {
      assert.equal(body.user_id, 2);
      keys.push({ ...body, id: 100, key_prefix: "sk-generated-masked" });
      return send(res, 201, { data: { ...keys.at(-1), key: "sk-fixture-new-one-time-secret" } });
    }
    if (u.pathname === "/api/admin/keys/99/claim") {
      assert.deepEqual(body, { user_id: 2 }); keys[0].user_id = 2;
      return send(res, 200, { data: keys[0] });
    }
    if (u.pathname === "/api/admin/quotations/current") return send(res, 200, { data: { enabled: true, version, currency: "ACCOUNT", usage_profile: "responses_native_tokens_v1" } });
    if (u.pathname === "/api/admin/quotations/calculate") return send(res, 200, { data: { known: body.usage.input_tokens !== null,
      amount_units: body.usage.input_tokens === null ? "" : "0", reason: body.usage.input_tokens === null ? "usage_unknown" : "quoted", currency: "ACCOUNT" } });
    send(res, 404, { error: { message: "fixture-not-found" } });
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}) });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, reducedMotion: "reduce" });
    const page = await context.newPage(); page.setDefaultTimeout(10000);
    page.on("pageerror", e => errors.push(e.message));
    await context.route("**/*", route => new URL(route.request().url()).origin === "http://127.0.0.1:" + server.address().port ? route.continue() : route.abort());
    const login = async () => {
      await page.goto("http://127.0.0.1:" + server.address().port);
      await page.getByLabel("用户名", { exact: true }).fill("fixture"); await page.getByLabel("密码", { exact: true }).fill("fixture-password");
      await page.getByRole("button", { name: "登录工作空间" }).click(); await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
    };
    const nav = async id => {
      const toggle = page.getByRole("button", { name: "打开导航", exact: true });
      if (await toggle.isVisible()) await toggle.click();
      await page.locator("nav a[data-page='" + id + "']").click();
    };
    const noOverflow = async () => assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    await page.goto("http://127.0.0.1:" + server.address().port); await login();
    assert.equal(calls.some(c => c.url.startsWith("/api/admin/")), false, "user overview fetched administrator data");
    assert.equal(await page.locator(".chart-line").count(), 1);
    await page.getByLabel("样本时间范围").selectOption("6");
    await page.getByText("查看按小时样本数据", { exact: true }).click();
    assert.equal(await page.locator("details.sample-data tbody tr").count(), 6);
    await page.keyboard.press("Control+k");
    await page.getByRole("dialog").waitFor();
    assert.equal(await page.getByRole("dialog").getByRole("button", { name: "全站 API Key", exact: true }).count(), 0);
    await page.keyboard.press("Escape");
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.setViewportSize({ width: 320, height: 900 });
    await page.waitForFunction(() => Number(document.querySelector(".trend-chart").viewBox.baseVal.width) < 400);
    await noOverflow();
    const menu = page.getByRole("button", { name: "打开导航", exact: true });
    await menu.click();
    assert.equal(await menu.getAttribute("aria-expanded"), "true");
    assert.equal(await page.locator(".main-column").evaluate(n => n.inert), true);
    await page.keyboard.press("Escape");
    assert.equal(await menu.getAttribute("aria-expanded"), "false");
    assert.equal(await menu.evaluate(n => document.activeElement === n), true);
    await page.setViewportSize({ width: 1440, height: 1050 });
    await nav("models"); await page.getByLabel("搜索模型").fill("<img");
    assert.equal(await page.locator("#view img").count(), 0); await page.getByRole("heading", { name: "<img src=x onerror=alert(1)>", exact: true }).waitFor();
    await nav("finance"); await page.getByRole("button", { name: "查看资金事实" }).first().waitFor();
    assert.match(await page.locator("#view").innerText(), /9223372036\.854775807/);
    assert.match(await page.locator("tbody tr").first().innerText(), /未结算/);
    assert.match(await page.locator("tbody tr").nth(1).innerText(), /已结算\s+0\s+0/);
    await page.getByRole("button", { name: "查看资金事实" }).first().click();
    await page.getByRole("heading", { name: "个人资金事实" }).waitFor();
    for (const width of [1440, 390, 320]) { await page.setViewportSize({ width, height: 900 }); await noOverflow(); }
    await nav("usage"); await page.getByRole("button", { name: "查看原生用量" }).click();
    await page.getByRole("heading", { name: "调用观察" }).waitFor();
    assert.match(await page.locator("#view").innerText(), /9,223,372,036,854,775,807/);
    assert.match(await page.locator("#view").innerText(), /未知/);
    assert.equal(await page.getByLabel("用量查看范围").locator("option[value=admin]").count(), 0);
    const [download] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "导出本页" }).click()]);
    assert.equal(download.suggestedFilename(), "txapi-usage-metadata.csv");
    enabled = false; await page.getByRole("button", { name: "刷新", exact: true }).click();
    await page.getByText("原生用量记录未启用。", { exact: false }).waitFor();
    balanceAvailable = false; await nav("finance"); await page.getByText("资金汇总暂不可用：fixture-unavailable", { exact: true }).waitFor();
    balanceAvailable = true;
    await page.getByRole("button", { name: "退出登录", exact: true }).click(); await page.getByRole("heading", { name: "欢迎回来" }).waitFor();
    role = "admin"; await login();
    await nav("admin-keys"); await page.getByRole("button", { name: "显式认领" }).click();
    await page.getByLabel("归属用户", { exact: true }).selectOption("2"); await page.getByLabel("确认 Key ID").fill("99");
    await page.getByRole("button", { name: "保存更改" }).click(); await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.getByRole("button", { name: "显式认领" }).waitFor({ state: "detached" });
    assert.equal(await page.getByRole("button", { name: "显式认领" }).count(), 0);
    await page.getByRole("button", { name: "+ 为用户创建 Key", exact: true }).click();
    await page.getByLabel("归属用户", { exact: true }).selectOption("2"); await page.getByLabel("Key 名称", { exact: true }).fill("new-owned");
    await page.getByRole("button", { name: "保存更改" }).click(); await page.getByRole("dialog", { name: "保存你的 API Key" }).waitFor();
    assert.equal(await page.getByLabel("新 API Key").inputValue(), "sk-fixture-new-one-time-secret");
    assert.deepEqual(await page.getByRole("dialog", { name: "保存你的 API Key" }).evaluate(dialog => {
      const value = dialog.querySelector("textarea");
      dialog.close();
      return { connected: dialog.isConnected, secret: value.value };
    }), { connected: false, secret: "" }, "closing a secret dialog must synchronously clear and detach it");
    assert.equal(await page.locator("body").innerText().then(t => t.includes("sk-fixture-new-one-time-secret")), false);
    await nav("quotation"); await page.getByLabel("精确模型名称").fill("m1");
    for (const label of ["假设输入 Token", "假设输出 Token", "假设总 Token"]) await page.getByLabel(label, { exact: true }).fill("0");
    await page.getByLabel("用量完整度").selectOption("complete"); await page.getByRole("button", { name: "计算假设报价" }).click();
    await page.getByText("0 ACCOUNT", { exact: true }).waitFor();
    for (const label of ["假设输入 Token", "假设输出 Token", "假设总 Token"]) await page.getByLabel(label, { exact: true }).fill("");
    await page.getByLabel("用量完整度").selectOption("unknown"); await page.getByRole("button", { name: "计算假设报价" }).click();
    await page.getByText("未知，不能当作零费用", { exact: true }).waitFor();
    assert.equal(writes.filter(w => w.path.endsWith("/calculate")).length, 2);
    assert.equal(calls.some(c => c.url.startsWith("/v1/")), false, "read-only diagnostics dispatched a generation");
    assert.deepEqual(await page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } })), { local: {}, session: {} });
    const boundary = calls.length; role = "user"; await nav("admin-keys");
    await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
    assert.equal(await page.locator("nav a[data-page=admin-keys]").count(), 0);
    assert.equal(calls.slice(boundary).some(c => c.url.startsWith("/api/admin/")), false, "revoked role fetched administrator data");
    assert.deepEqual(errors, []);
    await context.close();
    console.log("PASS: owner precision/unknown/zero, native usage/disabled state, safe model text, CSV, explicit Key claim/create/one-time secret, read-only quote, role revocation, private storage and mobile layout");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
