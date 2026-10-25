const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require("playwright");

const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};

(async () => {
  const create = deferred(), nextPage = deferred(), detail = deferred();
  let remove = deferred();
  const user = { id: 1, username: "interaction-fixture", role: "admin", status: "active", model_mode: "all", models: [] };
  const calls = [], errors = [];
  let createCalls = 0, deleteCalls = 0, pageCalls = 0, deleted = false;
  let forbidUsage = false;
  const failedPages = new Set();
  const record = index => ({ id: "usage-" + index, request_id: "request-" + index, model: "model-alpha", operation: "chat",
    transport: "http", state: "sealed", attempt_count: 1, created_at: Date.now(),
    host: { protocol_status: "completed", protocol_complete: true, delivery_committed: true } });
  const json = (res, code, value, headers = {}) => {
    res.writeHead(code, { "Content-Type": "application/json", "Cache-Control": "no-store", ...headers });
    res.end(JSON.stringify(value));
  };
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    calls.push({ path: url.pathname, query: url.search, method: req.method });
    const asset = url.pathname === "/" ? "index.html" : url.pathname.startsWith("/ui/") ? url.pathname.slice(4) : "";
    if (["index.html", "app.mjs", "api.mjs", "workspace.mjs", "app.css"].includes(asset)) {
      res.writeHead(200, { "Content-Type": asset.endsWith(".mjs") ? "text/javascript" : asset.endsWith(".css") ? "text/css" : "text/html",
        "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'" });
      res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", asset))); return;
    }
    if (url.pathname === "/api/auth/login") { req.resume(); return json(res, 200, { session_token: "interaction-synthetic-session", user }); }
    if (url.pathname === "/api/me") return json(res, 200, { data: user });
    if (url.pathname === "/api/me/models") return json(res, 200, { data: ["model-alpha", "model-beta"], policy: { mode: "all", models: [] } });
    if (url.pathname === "/api/me/keys" && req.method === "GET") return json(res, 200, { data: deleted ? [] : [
      { id: 7, name: "fixture application", key_prefix: "fixture-masked", status: "active", model_mode: "all", models: [] },
    ] });
    if (url.pathname === "/api/me/keys" && req.method === "POST") {
      createCalls++;
      let raw = ""; for await (const chunk of req) raw += chunk;
      create.resolve({ res, body: JSON.parse(raw) }); return;
    }
    if (url.pathname === "/api/me/keys/7" && req.method === "DELETE") { deleteCalls++; remove.resolve(res); return; }
    if (url.pathname === "/api/me/usage-records") {
      if (forbidUsage) return json(res, 403, { error: { message: "fixture-usage-forbidden" } });
      if (url.searchParams.get("offset") === "50") {
        pageCalls++;
        if (pageCalls === 1) return json(res, 503, { error: { message: "fixture-next-page-unavailable" } });
        nextPage.resolve(res); return;
      }
      return json(res, 200, { enabled: true, data: Array.from({ length: 50 }, (_, i) => record(i)), has_more: true });
    }
    if (url.pathname === "/api/me/usage-records/usage-50") { detail.resolve(res); return; }
    const kind = url.pathname.endsWith("/logs") ? "log" : url.pathname === "/api/admin/billing/reservations" ? "billing" :
      url.pathname.endsWith("/routing") ? "routing" : url.pathname.endsWith("/background") ? "task" : "";
    if (kind) {
      if (url.searchParams.get("offset") === "50" && !failedPages.has(url.pathname)) {
        failedPages.add(url.pathname); return json(res, 503, { error: { message: "fixture-paged-unavailable" } });
      }
      const data = Array.from({ length: 50 }, (_, i) => kind === "log" ? {
        created_at: new Date().toISOString(), request_id: "log-" + i, origin_model: "model-alpha", status: 200, total_tokens: 1, cost: 0, latency_ms: 1,
      } : kind === "billing" ? {
        id: "billing-" + i, user_id: 1, token_id: 7, origin_model: "model-alpha", price_version: "a".repeat(64),
        state: "pending", reason: "unknown", reserved_units: "0", provider_held_units: "0", provider_budget_units: "0",
      } : kind === "task" ? {
        id: "task-" + i, user_id: 1, token_id: 7, model: "model-alpha", created_at: Date.now(), stage: "queued", event_count: 1,
      } : {
        id: "route-" + i, request_id: "routing-" + i, user_id: 1, token_id: 7, model: "model-alpha", operation: "chat",
        reason: "completed", finished_at: Date.now(), selections: [], attempts: [],
      });
      return json(res, 200, { data, has_more: true, enabled: true, capacity: 100, retention_seconds: 60 });
    }
    req.resume(); json(res, 404, { error: { message: "fixture-route-not-found" } });
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}) });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: "reduce" });
    const page = await context.newPage(); page.setDefaultTimeout(10000);
    page.on("pageerror", error => errors.push(error.message));
    const origin = "http://127.0.0.1:" + server.address().port;
    await context.route("**/*", route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    await page.goto(origin + "/#keys");
    await page.getByLabel("用户名", { exact: true }).fill("fixture");
    await page.getByLabel("密码", { exact: true }).fill("fixture-password-only");
    await page.getByRole("button", { name: "登录工作空间", exact: true }).click();
    await page.getByRole("heading", { name: "我的 API Key", exact: true }).waitFor();
    assert.equal(await page.title(), "我的 API Key · TXAPI");
    assert.equal(await page.locator("#view h1").evaluate(node => document.activeElement === node), true);

    // Round 1: one command window, full keyboard navigation and contextual model handoff.
    await page.keyboard.press("Control+k"); await page.keyboard.press("Control+k");
    assert.equal(await page.getByRole("dialog", { name: "跳转页面", exact: true }).count(), 1);
    await page.getByLabel("搜索页面", { exact: true }).fill(" models ");
    await page.keyboard.press("ArrowDown");
    assert.equal(await page.getByRole("dialog").getByRole("button", { name: "模型目录", exact: true }).evaluate(node => document.activeElement === node), true);
    await page.keyboard.press("Enter");
    await page.getByRole("heading", { name: "模型目录", exact: true }).waitFor();
    assert.equal(await page.title(), "模型目录 · TXAPI");
    await page.locator(".model-grid .panel").filter({ has: page.getByRole("heading", { name: "model-beta", exact: true }) }).getByRole("link").click();
    await page.getByRole("heading", { name: "调用测试", exact: true }).waitFor();
    assert.equal(await page.getByLabel("模型", { exact: true }).inputValue(), "model-beta");
    await page.locator("nav a[data-page=keys]").click();
    await page.getByRole("heading", { name: "我的 API Key", exact: true }).waitFor();

    // Round 4: meaningful validation, pending ownership, draft confirmation and one mutation per button.
    const opener = page.getByRole("button", { name: "+ 创建 Key", exact: true });
    await opener.click();
    await page.getByLabel("Key 名称", { exact: true }).fill("   ");
    await page.getByRole("button", { name: "保存更改", exact: true }).click();
    assert.equal(createCalls, 0);
    assert.equal(await page.getByLabel("Key 名称", { exact: true }).getAttribute("aria-invalid"), "true");
    await page.getByLabel("Key 名称", { exact: true }).fill("keep my draft");
    assert.equal(await page.getByLabel("Key 名称", { exact: true }).getAttribute("aria-invalid"), null);
    await page.getByRole("button", { name: "保存更改", exact: true }).click();
    const submitted = await create.promise;
    assert.equal(submitted.body.name, "keep my draft");
    assert.equal(await page.getByLabel("Key 名称", { exact: true }).isDisabled(), true);
    assert.equal(await page.getByRole("button", { name: "正在保存…", exact: true }).getAttribute("aria-busy"), "true");
    json(submitted.res, 503, { error: { message: "fixture-save-unavailable" } }, { "X-Request-Id": "fixture-form-error" });
    await page.getByRole("dialog").getByRole("alert").filter({ hasText: "fixture-save-unavailable" }).waitFor();
    assert.equal(await page.getByRole("dialog").getByRole("alert").evaluate(node => document.activeElement === node), true);
    assert.match(await page.getByRole("dialog").getByRole("alert").innerText(), /fixture-form-error/);
    assert.equal(await page.getByLabel("Key 名称", { exact: true }).inputValue(), "keep my draft");
    assert.equal(await page.getByLabel("Key 名称", { exact: true }).isEnabled(), true);
    page.once("dialog", dialog => { assert.match(dialog.message(), /放弃尚未保存/); dialog.dismiss(); });
    await page.getByRole("button", { name: "取消", exact: true }).click();
    assert.equal(await page.getByLabel("Key 名称", { exact: true }).inputValue(), "keep my draft");
    page.once("dialog", dialog => dialog.accept()); await page.keyboard.press("Escape");
    await page.getByRole("dialog").waitFor({ state: "detached" });
    assert.equal(await opener.evaluate(node => document.activeElement === node), true);
    page.once("dialog", dialog => { assert.match(dialog.message(), /撤销 Key/); dialog.accept(); });
    await page.getByRole("button", { name: "撤销", exact: true }).evaluate(node => { node.click(); node.click(); });
    const removed = await remove.promise;
    assert.equal(deleteCalls, 1);
    assert.equal(await page.getByRole("button", { name: "撤销", exact: true }).isDisabled(), true);
    deleted = true; json(removed, 200, {});
    await page.getByText("当前没有记录。可创建项目或调整筛选条件。", { exact: true }).waitFor();

    // Cross-review: non-editor mutations have the same view/session ownership as editors.
    for (const outcome of ["success", "saved-error", "forbidden"]) {
      deleted = false; remove = deferred();
      await page.locator("nav a[data-page=models]").click();
      await page.getByRole("heading", { name: "模型目录", exact: true }).waitFor();
      await page.locator("nav a[data-page=keys]").click();
      await page.getByRole("button", { name: "撤销", exact: true }).waitFor();
      page.once("dialog", dialog => dialog.accept());
      await page.getByRole("button", { name: "撤销", exact: true }).click();
      const oldMutation = await remove.promise;
      await page.locator("nav a[data-page=models]").click();
      await page.getByRole("heading", { name: "模型目录", exact: true }).waitFor();
      await page.locator("nav a[data-page=keys]").click();
      await page.getByRole("button", { name: "+ 创建 Key", exact: true }).click();
      await page.getByLabel("Key 名称", { exact: true }).fill("keep-after-old-delete");
      if (outcome === "forbidden") user.role = "user";
      deleted = outcome !== "forbidden";
      json(oldMutation, outcome === "success" ? 200 : outcome === "forbidden" ? 403 : 500,
        outcome === "success" ? {} : { saved: outcome === "saved-error", error: { message: "fixture-old-delete-" + outcome } },
        outcome === "saved-error" ? { "X-TXAPI-Publication-State": "pending" } : {});
      if (outcome === "forbidden") {
        await page.locator("nav a[data-page=channels]").waitFor({ state: "detached" });
        assert.equal(await page.getByRole("dialog").count(), 0, "a late current-session 403 did not clear revoked administrator UI");
      } else {
        await page.waitForLoadState("networkidle");
        assert.equal(await page.getByLabel("Key 名称", { exact: true }).inputValue(), "keep-after-old-delete", "old mutation destroyed a new draft");
        if (outcome === "saved-error") assert.match(await page.locator("#notice").innerText(), /配置已保存/);
        page.once("dialog", dialog => dialog.accept()); await page.keyboard.press("Escape");
        await page.getByRole("dialog").waitFor({ state: "detached" });
      }
    }
    user.role = "admin";
    await page.locator("nav a[data-page=models]").click();
    await page.getByRole("heading", { name: "模型目录", exact: true }).waitFor();

    // Round 7: unapplied filters and failed/new pages must never export stale rows.
    await page.locator("nav a[data-page=usage]").click();
    await page.getByText("usage-0", { exact: true }).waitFor();
    const exportButton = page.getByRole("button", { name: "导出本页", exact: true });
    assert.equal(await exportButton.isEnabled(), true);
    await page.getByLabel("用量 Key ID", { exact: true }).fill("12");
    await page.getByText("筛选条件尚未应用，请刷新后查看或导出匹配结果。", { exact: true }).waitFor();
    assert.equal(await exportButton.isDisabled(), true);
    assert.equal(await page.getByRole("button", { name: "下一页", exact: true }).isDisabled(), true);
    await page.getByRole("button", { name: "清除筛选", exact: true }).click();
    await page.getByText("usage-0", { exact: true }).waitFor();
    assert.equal(await page.getByLabel("用量 Key ID", { exact: true }).inputValue(), "");
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    await page.getByRole("alert").filter({ hasText: "fixture-next-page-unavailable" }).waitFor();
    assert.equal(await page.getByText("usage-0", { exact: true }).count(), 0);
    assert.equal(await exportButton.isDisabled(), true);
    await page.getByLabel("用量 Key ID", { exact: true }).fill("99");
    await page.getByText("筛选条件已改变，请刷新后从第一页读取。", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "重试读取", exact: true }).isDisabled(), true);
    await page.getByRole("button", { name: "重试读取", exact: true }).dispatchEvent("click");
    assert.equal(pageCalls, 1, "retry dispatched a changed filter at the failed page offset");
    await page.getByLabel("用量 Key ID", { exact: true }).fill("");
    assert.equal(await page.getByRole("button", { name: "重试读取", exact: true }).isEnabled(), true);
    await page.getByRole("button", { name: "重试读取", exact: true }).click();
    const pendingPage = await nextPage.promise;
    assert.equal(await exportButton.isDisabled(), true);
    assert.equal(await page.getByRole("button", { name: "下一页", exact: true }).isDisabled(), true);
    json(pendingPage, 200, { enabled: true, data: [record(50)], has_more: false });
    await page.getByText("记录 51–51", { exact: true }).waitFor();
    assert.equal(await exportButton.isEnabled(), true);
    assert.equal(pageCalls, 2);
    await page.getByRole("button", { name: "查看原生用量", exact: true }).click();
    const pendingDetail = await detail.promise;
    assert.equal(await page.getByRole("region", { name: "记录详情", exact: true }).getAttribute("aria-busy"), "true");
    json(pendingDetail, 200, { data: { record: record(50), attempts: [] } });
    await page.getByRole("heading", { name: "调用观察", exact: true }).waitFor();
    assert.equal(await page.getByRole("heading", { name: "调用观察", exact: true }).evaluate(node => document.activeElement === node), true);
    for (const scenario of [
      { page: "logs", row: "log-0", field: "筛选模型", value: "other-model", parameter: "model", path: "/api/admin/logs", refresh: "刷新" },
      { page: "billing", row: "billing-0", field: "财务用户 ID", value: "99", parameter: "user_id", path: "/api/admin/billing/reservations", refresh: "刷新账本" },
      { page: "tasks", row: "task-0", field: "创建 Key ID", value: "99", parameter: "token_id", path: "/api/me/background", refresh: "刷新任务" },
      { page: "routing", row: "routing-0", field: "精确 Request ID", value: "filtered-request", parameter: "request_id", path: "/api/me/routing", refresh: "刷新诊断" },
    ]) {
      await page.locator("nav a[data-page='" + scenario.page + "']").click();
      await page.getByText(scenario.row, { exact: true }).waitFor();
      if (scenario.page === "logs") {
        const scoped = page.waitForResponse(response => new URL(response.url()).pathname === scenario.path);
        await page.getByLabel("日志范围", { exact: true }).selectOption("admin");
        await (await scoped).finished();
      }
      await page.getByRole("button", { name: "下一页", exact: true }).click();
      await page.getByRole("alert").filter({ hasText: "fixture-paged-unavailable" }).waitFor();
      assert.equal(await page.getByText(scenario.row, { exact: true }).count(), 0, scenario.page + " retained an old table after a failed page");
      assert.equal(await page.getByRole("button", { name: "下一页", exact: true }).isEnabled(), true, "same-filter next-page retry was lost");
      await page.getByLabel(scenario.field, { exact: true }).fill(scenario.value);
      assert.equal(await page.getByRole("button", { name: "下一页", exact: true }).isDisabled(), true);
      assert.equal(await page.getByRole("button", { name: "重试读取", exact: true }).isDisabled(), true);
      const response = page.waitForResponse(response => {
        const url = new URL(response.url());
        return url.pathname === scenario.path && url.searchParams.get(scenario.parameter) === scenario.value && url.searchParams.get("offset") === "0";
      });
      await page.getByRole("button", { name: scenario.refresh, exact: true }).click();
      await (await response).finished();
      await page.getByText(scenario.row, { exact: true }).waitFor();
    }
    forbidUsage = true;
    const boundary = calls.length;
    await page.locator("nav a[data-page=usage]").click();
    await page.getByRole("heading", { name: "读取失败", exact: true }).waitFor();
    assert.equal(calls.slice(boundary).filter(call => call.path === "/api/me/usage-records").length, 2, "persistent 403 caused unbounded collection recovery");
    assert.equal(calls.some(call => call.path.startsWith("/v1/")), false);
    assert.deepEqual(errors, []);
    await context.close();
    console.log("PASS: rounds 1/4/7 keyboard navigation/model handoff, draft/error/pending safeguards, one destructive mutation, all list filters/page retry, stale export/detail and bounded 403 recovery");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
