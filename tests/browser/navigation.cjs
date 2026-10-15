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
      "/ui/app.css": ["app.css", "text/css"],
    };
    if (assets[req.url]) {
      const [name, type] = assets[req.url];
      res.writeHead(200, { "Content-Type": type });
      res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", name)));
    } else if (req.url === "/api/auth/login") {
      json(res, { session_token: "fixture-session-only", user });
    } else if (req.url === "/api/me/usage") {
      json(res, { data: { user_id: 1, quota: 0, used_quota: 0, usage: {} } });
    } else if (req.url === "/api/me/keys") {
      json(res, { data: [] });
    } else if (req.url === "/api/me/models") {
      if (++modelCalls === 1) firstModels.resolve(res);
      else json(res, modelReply);
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
    await page.getByLabel("Username", { exact: true }).fill("fixture");
    await page.getByLabel("Password", { exact: true }).fill("fixture-password-only");
    await page.getByRole("button", { name: "Enter control room" }).click();
    await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
    await page.getByRole("link", { name: "Try a request", exact: false }).click();
    const delayed = await firstModels.promise;
    await page.getByRole("link", { name: "Overview", exact: false }).click();
    await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
    await page.getByRole("link", { name: "Try a request", exact: false }).click();
    await page.getByRole("heading", { name: "A small connection test" }).waitFor();
    const oldReply = page.waitForResponse(response => response.url().endsWith("/api/me/models"));
    json(delayed, modelReply);
    await (await oldReply).finished();
    await page.waitForTimeout(100);
    await page.getByLabel("API key", { exact: true }).fill("fixture-call-key-only");
    await page.getByRole("button", { name: "Send request", exact: true }).click();
    await callStarted.promise;
    await page.getByRole("link", { name: "Overview", exact: false }).click();
    await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
    const canceled = await Promise.race([callCanceled.promise, new Promise(done => setTimeout(() => done(false), 1500))]);
    assert.equal(canceled, true, "an old model-list response replaced the active view cleanup; leaving the page did not cancel its model call");
    console.log("PASS: delayed old playground load cannot replace current cleanup; leaving current view cancels its in-flight request");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
