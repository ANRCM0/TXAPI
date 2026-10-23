async function openNavigation(page) {
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible() && await menu.getAttribute("aria-expanded") !== "true") await menu.click();
}

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
  const firstModels = deferred();
  const callStarted = deferred();
  const callCanceled = deferred();
  let modelCalls = 0;
  let holdLog = false;
  let failLogPage = true;
  const delayedLog = deferred();
  const applyStarted = deferred();
  let channelSaves = 0;
  let savePublication = "pending";
  let applyCalls = 0;
  let savedChannel;
  const publication = { epoch: "fixture", channels: { desired_revision: 0, applied_revision: 0, last_error: "" },
    prices: { desired_revision: 0, applied_revision: 0, last_error: "" } };
  const logRows = label => Array.from({ length: 50 }, (_, index) => ({
    created_at: new Date().toISOString(), request_id: `${label}-${index}`, origin_model: "fixture-model", status: 200,
    total_tokens: 1, cost: 0, latency_ms: 1,
  }));
  const user = { id: 1, username: "fixture", role: "user", model_mode: "all", models: [] };
  const modelReply = { data: ["fixture-model"], policy: { mode: "all", models: [] } };
  function json(res, value) {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(value));
  }
  const server = http.createServer((req, res) => {
    const assets = {
      "/": ["index.html", "text/html"],
      "/ui/app.mjs": ["app.mjs", "text/javascript"],
      "/ui/api.mjs": ["api.mjs", "text/javascript"],
      "/ui/workspace.mjs": ["workspace.mjs", "text/javascript"],
      "/ui/app.css": ["app.css", "text/css"],
    };
    if (assets[req.url]) {
      const [name, type] = assets[req.url];
      res.writeHead(200, { "Content-Type": type });
      res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", name)));
    } else if (req.url === "/api/auth/login") {
      json(res, { session_token: "fixture-session-only", user });
    } else if (req.url === "/api/me") {
      json(res, { data: user });
    } else if (req.url === "/api/me/usage") {
      json(res, { data: { user_id: 1, quota: 0, used_quota: 0, usage: {} } });
    } else if (req.url === "/api/me/keys") {
      json(res, { data: [] });
    } else if (req.url === "/api/me/models") {
      if (++modelCalls === 1) firstModels.resolve(res);
      else json(res, modelReply);
    } else if (req.url.startsWith("/api/me/logs?")) {
      const offset = new URL(req.url, "http://127.0.0.1").searchParams.get("offset");
      if (holdLog) { holdLog = false; delayedLog.resolve(res); }
      else if (offset === "50" && failLogPage) {
        failLogPage = false;
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end('{"error":{"message":"fixture-page-failure"}}');
      } else json(res, { data: logRows(offset === "50" ? "page-two" : "current") });
    } else if (req.url === "/api/admin/users") {
      user.role = "user";
      res.writeHead(403, { "Content-Type": "application/json" });
      res.end('{"error":{"type":"forbidden","message":"administrator required"}}');
    } else if (req.url === "/api/admin/channels" && req.method === "POST") {
      channelSaves++;
      savedChannel = { id: 1, name: savePublication === "applied" ? "applied channel" : "saved channel", type: "openai", base_url: "https://fixture.invalid", models: ["fixture-model"], credentials: [] };
      if (savePublication === "applied") Object.assign(publication.channels, { desired_revision: 5, applied_revision: 5, last_error: "" });
      else Object.assign(publication.channels, { desired_revision: 1, last_error: "fixture-save-refresh-failed" });
      res.writeHead(500, { "Content-Type": "application/json", "X-TXAPI-Publication-State": savePublication });
      res.end('{"saved":true,"error":{"type":"saved_configuration_error","message":"Saved; refresh failed."}}');
    } else if (req.url === "/api/admin/channels") {
      json(res, { data: savedChannel ? [savedChannel] : [] });
    } else if (req.url === "/api/admin/config-status") {
      json(res, { data: publication });
    } else if (req.url === "/api/admin/config-status/apply") {
      if (++applyCalls === 1) applyStarted.resolve(res);
      else { Object.assign(publication.channels, { applied_revision: publication.channels.desired_revision, last_error: "" }); json(res, { data: publication }); }
    } else if (req.url === "/api/admin/model-policy") {
      json(res, { data: { mode: "all", models: [] } });
    } else if (req.url === "/api/admin/prices") {
      json(res, { data: [], group_rates: {} });
    } else if (req.url === "/v1/chat/completions") {
      res.on("close", () => { if (!res.writableEnded) callCanceled.resolve(true); });
      req.resume();
      callStarted.resolve();
    } else {
      res.writeHead(404); res.end();
    }
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}),
    });
    const context = await browser.newContext({ reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await page.getByLabel("用户名", { exact: true }).fill("fixture");
    await page.getByLabel("密码", { exact: true }).fill("fixture-password-only");
    await page.getByRole("button", { name: "登录工作空间" }).click();
    await page.getByRole("heading", { name: "工作空间总览" }).waitFor();
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "调用测试", exact: false }).click();
    const delayed = await firstModels.promise;
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "总览", exact: false }).click();
    await page.getByRole("heading", { name: "工作空间总览" }).waitFor();
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "调用测试", exact: false }).click();
    await page.getByRole("heading", { name: "连接测试" }).waitFor();
    const oldReply = page.waitForResponse(response => response.url().endsWith("/api/me/models"));
    json(delayed, modelReply);
    await (await oldReply).finished();
    await page.waitForTimeout(100);
    await page.getByLabel("调用 API Key", { exact: true }).fill("fixture-call-key-only");
    await page.getByRole("button", { name: "发送请求", exact: true }).click();
    await callStarted.promise;
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "总览", exact: false }).click();
    await page.getByRole("heading", { name: "工作空间总览" }).waitFor();
    const canceled = await Promise.race([callCanceled.promise, new Promise(done => setTimeout(() => done(false), 1500))]);
    assert.equal(canceled, true, "an old model-list response replaced the active view cleanup; leaving the page did not cancel its model call");
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "请求日志", exact: false }).click();
    await page.getByText("current-0", { exact: true }).waitFor();
    holdLog = true;
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    const staleLog = await delayedLog.promise;
    const freshLog = page.waitForResponse(response => response.url().includes("/api/me/logs?"));
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    await (await freshLog).finished();
    const staleReply = page.waitForResponse(response => response.url().includes("/api/me/logs?"));
    json(staleLog, { data: logRows("obsolete") });
    await (await staleReply).finished();
    await page.waitForTimeout(50);
    assert.equal(await page.getByText("obsolete-0", { exact: true }).count(), 0);
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    await page.getByRole("status").filter({ hasText: "fixture-page-failure" }).waitFor();
    assert.equal(await page.getByRole("button", { name: "下一页", exact: true }).isEnabled(), true);
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    await page.getByText("page-two-0", { exact: true }).waitFor();
    await page.getByText("记录 51-100", { exact: true }).waitFor();
    user.role = "admin";
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "总览", exact: false }).click();
    await openNavigation(page);
    const [forbidden] = await Promise.all([
      page.waitForResponse(response => new URL(response.url()).pathname === "/api/admin/users" && response.status() === 403),
      page.locator("nav").getByRole("link", { name: "用户与权限", exact: false }).click(),
    ]);
    await forbidden.finished();
    await page.locator("nav").getByRole("link", { name: "用户与权限", exact: false }).waitFor({ state: "detached" });
    await page.getByRole("heading", { name: "工作空间总览" }).waitFor();
    assert.equal(await page.locator("nav").getByRole("link", { name: "用户与权限", exact: false }).count(), 0);
    assert.equal(await page.getByRole("button", { name: "退出登录", exact: true }).count(), 1);
    user.role = "admin";
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "请求日志", exact: false }).click();
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "渠道管理", exact: false }).click();
    await page.getByRole("button", { name: "+ 创建渠道", exact: true }).click();
    await page.getByLabel("名称", { exact: true }).fill("saved channel");
    await page.getByLabel("上游地址", { exact: true }).fill("https://fixture.invalid");
    await page.getByLabel("模型目录", { exact: true }).fill("fixture-model");
    await page.getByLabel("首个上游 API Key", { exact: true }).fill("fixture-upstream-secret");
    await page.getByRole("button", { name: "保存更改", exact: true }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.getByRole("heading", { name: "saved channel", exact: true }).waitFor();
    assert.equal(channelSaves, 1);
    assert.equal(await page.locator('input[type="password"]').count(), 0);
    assert.match(await page.getByRole("status").innerText(), /发布待处理.*重试发布/);
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "价格与发布", exact: false }).click();
    const retry = page.getByRole("button", { name: "重试发布", exact: true });
    await retry.click();
    const failedApply = await applyStarted.promise;
    assert.equal(await retry.isDisabled(), true);
    await retry.dispatchEvent("click");
    assert.equal(applyCalls, 1);
    Object.assign(publication.channels, { desired_revision: 4, last_error: "fixture-apply-failed" });
    failedApply.writeHead(503, { "Content-Type": "application/json" });
    failedApply.end(JSON.stringify({ data: publication }));
    await page.getByText("Desired v4 / Applied v0", { exact: true }).waitFor();
    await page.getByText("fixture-apply-failed", { exact: true }).waitFor();
    await retry.click();
    await page.getByText("Desired v4 / Applied v4", { exact: true }).waitFor();
    assert.equal(channelSaves, 1, "publication retried a saved mutation");
    savePublication = "applied";
    await openNavigation(page);
    await page.locator("nav").getByRole("link", { name: "渠道管理", exact: false }).click();
    await page.getByRole("button", { name: "+ 创建渠道", exact: true }).click();
    await page.getByLabel("名称", { exact: true }).fill("applied channel");
    await page.getByLabel("上游地址", { exact: true }).fill("https://fixture.invalid");
    await page.getByLabel("模型目录", { exact: true }).fill("fixture-model");
    await page.getByLabel("首个上游 API Key", { exact: true }).fill("fixture-applied-secret");
    await page.getByRole("button", { name: "保存更改", exact: true }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.getByRole("heading", { name: "applied channel", exact: true }).waitFor();
    assert.equal(channelSaves, 2);
    assert.equal(await page.locator('input[type="password"]').count(), 0);
    const appliedNotice = await page.getByRole("status").innerText();
    assert.match(appliedNotice, /已保存并发布.*回读或管理缓存刷新失败/);
    assert.doesNotMatch(appliedNotice, /发布失败|重试发布|发布待处理/);
    await page.getByText("Desired v5 / Applied v5", { exact: true }).waitFor();
    assert.equal(applyCalls, 2, "saved/applied failure retried publication");
    console.log("PASS: navigation cancellation, log ordering/page retry, current role/403, saved mutations and publication retries");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
