const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require("playwright");

(async () => {
  let role = "admin", malformedModels = true, forbiddenChannels = false, expireIdentity = false;
  let unavailableChannels = false, forbiddenQuote = false;
  let createAck;
  const keyCreates = { me: 0, admin: 0 };
  const calls = new Map(), closed = new Set(), errors = [];
  const user = () => ({ id: 1, username: "resilience-fixture", role, status: "active", model_mode: "all", models: [] });
  const json = (res, value) => { res.writeHead(200, { "Content-Type": "application/json" }); res.end(JSON.stringify(value)); };
  const hold = (res, status, requestId) => {
    res.writeHead(status, { "Content-Type": "application/json", "X-Request-Id": requestId });
    res.flushHeaders();
    res.write('{"data":');
    res.on("close", () => closed.add(requestId));
  };
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    calls.set(url.pathname, (calls.get(url.pathname) || 0) + 1);
    const asset = url.pathname === "/" ? "index.html" : url.pathname.startsWith("/ui/") ? url.pathname.slice(4) : "";
    if (["index.html", "app.mjs", "api.mjs", "workspace.mjs", "app.css"].includes(asset)) {
      res.writeHead(200, { "Content-Type": asset.endsWith(".mjs") ? "text/javascript" : asset.endsWith(".css") ? "text/css" : "text/html",
        "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'" });
      return res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", asset)));
    }
    for await (const unused of req) { /* Drain synthetic bodies before simulating a transport failure. */ }
    if (url.pathname.startsWith("/__resilience/")) {
      const mode = url.pathname.slice("/__resilience/".length);
      if (mode === "timeout") return hold(res, 200, "timeout-body");
      if (mode === "forbidden-slow") return hold(res, 403, "forbidden-body");
      if (mode === "unauthorized-slow") return hold(res, 401, "unauthorized-body");
      if (mode === "key-unauthorized-slow") return hold(res, 401, "key-unauthorized-body");
      if (mode === "network") return res.destroy();
      if (mode === "stale") {
        return setTimeout(() => {
          res.writeHead(401, { "Content-Type": "text/html", "X-TXAPI-Publication-State": "pending" });
          res.end("<p>obsolete-auth-response</p>");
        }, 20);
      }
      if (mode === "pending") {
        res.writeHead(503, { "Content-Type": "text/html", "X-Request-Id": "pending-proxy-1", "X-TXAPI-Publication-State": "pending" });
        return res.end("<p>fixture-proxy-private-details</p>");
      }
      return json(res, { data: { recovered: true } });
    }
    if (url.pathname === "/api/auth/login") return json(res, { session_token: "resilience-session-fixture", user: user() });
    if (url.pathname === "/api/auth/logout") return json(res, {});
    if (url.pathname === "/api/me") return expireIdentity ? hold(res, 401, "expired-ui-body") : json(res, { data: user() });
    if (url.pathname === "/api/me/usage") return json(res, { data: { usage: { total_requests: 0, total_tokens: 0, total_cost: 0 } } });
    if (["/api/me/keys", "/api/admin/keys"].includes(url.pathname) && req.method === "POST") {
      keyCreates[url.pathname.includes("/admin/") ? "admin" : "me"]++;
      res.writeHead(201, { "Content-Type": "application/json" });
      return res.end(JSON.stringify(createAck));
    }
    if (url.pathname === "/api/me/keys" || url.pathname === "/api/me/logs") return json(res, { data: [] });
    if (url.pathname === "/api/admin/keys") return json(res, { data: [] });
    if (url.pathname === "/api/admin/users") return json(res, { data: [user()] });
    if (url.pathname === "/api/me/billing/summary") return json(res, { data: { balance_known: false, pending_count: "0" } });
    if (url.pathname === "/api/admin/channels") {
      if (forbiddenChannels) {
        forbiddenChannels = false; role = "user";
        res.writeHead(403, { "Content-Type": "text/html", "X-Request-Id": "role-revoked-1" });
        return res.end("<p>fixture-forbidden-proxy</p>");
      }
      if (unavailableChannels) {
        res.writeHead(503, { "Content-Type": "application/json" });
        return res.end('{"error":{"message":"fixture-channel-unavailable"}}');
      }
      return json(res, { data: [] });
    }
    if (url.pathname === "/api/admin/quotations/current") return json(res, { data: {
      enabled: true, version: "a".repeat(64), currency: "ACCOUNT", usage_profile: "responses_native_tokens_v1",
    } });
    if (url.pathname === "/api/admin/quotations/calculate" && forbiddenQuote) {
      role = "user";
      res.writeHead(403, { "Content-Type": "text/html", "X-Request-Id": "quote-role-revoked-1" });
      return res.end("<p>fixture-forbidden-quote</p>");
    }
    if (url.pathname === "/api/me/models") {
      if (malformedModels) {
        res.writeHead(200, { "Content-Type": "application/json", "X-Request-Id": "models-invalid-1" });
        return res.end("null");
      }
      return json(res, { data: ["recovered-model"], policy: { mode: "all", models: [] } });
    }
    res.writeHead(404, { "Content-Type": "application/json" });
    res.end('{"error":{"message":"fixture-not-found"}}');
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}) });
    const origin = "http://127.0.0.1:" + server.address().port;
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: "reduce" });
    await context.route("**/*", route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    const page = await context.newPage(); page.setDefaultTimeout(10000);
    page.on("pageerror", error => errors.push(error.message));
    await page.goto(origin);
    const probes = await page.evaluate(async () => {
      const { createClient } = await import("/ui/api.mjs");
      const failure = async promise => {
        try { await promise; return { unexpectedSuccess: true }; }
        catch (error) { return { code: error.code, name: error.name, status: error.status,
          requestId: error.requestId, operationUnknown: error.operationUnknown, publication: error.publication,
          saved: error.saved, message: error.message }; }
      };
      const client = createClient();
      const canceled = new AbortController(); canceled.abort();
      const preCanceled = await failure(client.request("/__resilience/never-sent", { method: "POST", body: {}, signal: canceled.signal }));
      const timeout = await failure(client.request("/__resilience/timeout", { method: "POST", body: {}, timeout: 750 }));
      const network = await failure(client.request("/__resilience/network", { method: "POST", body: {} }));
      const forbidden = await failure(client.request("/__resilience/forbidden-slow", { timeout: 750 }));
      let signedOut = 0;
      const auth = createClient({ onUnauthorized: () => { signedOut++; auth.setSession(""); } });
      auth.setSession("fixture-management-session");
      const expired = await failure(auth.request("/__resilience/unauthorized-slow"));
      auth.setSession("fixture-management-session");
      const callKey = await failure(auth.request("/__resilience/key-unauthorized-slow", { callKey: "fixture-invalid-call-key", timeout: 750 }));
      let pending = 0;
      const publication = createClient({ onPending: () => pending++ });
      const malformed = await failure(publication.request("/__resilience/pending", { method: "POST", body: {} }));
      let staleExpired = 0, staleWarnings = 0;
      const stale = createClient({ onUnauthorized: () => staleExpired++, onPending: () => staleWarnings++,
        fetcher: (url, options) => fetch(url, { ...options, signal: undefined }) });
      const old = [];
      for (let i = 0; i < 8; i++) {
        stale.setSession("old-fixture-session-" + i);
        const request = stale.request("/__resilience/stale");
        stale.setSession("new-fixture-session-" + i);
        old.push(await failure(request));
      }
      return { preCanceled, timeout, network, forbidden, expired, callKey, signedOut, malformed, pending,
        old, staleExpired, staleWarnings, recovered: (await client.request("/__resilience/ok")).data.recovered };
    });
    assert.equal(probes.preCanceled.code, "canceled");
    assert.equal(probes.preCanceled.name, "AbortError");
    assert.equal(calls.get("/__resilience/never-sent") || 0, 0);
    assert.equal(probes.timeout.code, "timeout");
    assert.equal(probes.timeout.operationUnknown, true);
    assert.match(probes.timeout.message, /重复提交/);
    assert.equal(probes.network.code, "network");
    assert.equal(probes.network.operationUnknown, true);
    for (const name of ["timeout", "network", "pending"]) assert.equal(calls.get("/__resilience/" + name), 1, name + " was replayed");
    assert.equal(probes.forbidden.status, 403);
    assert.equal(probes.expired.status, 401);
    assert.equal(probes.expired.requestId, "unauthorized-body");
    assert.equal(probes.callKey.status, 401);
    assert.equal(probes.signedOut, 1, "invalid call Key expired the management session");
    assert.equal(probes.malformed.publication, "pending");
    assert.equal(probes.malformed.saved, undefined);
    assert.equal(probes.malformed.operationUnknown, true);
    assert.equal(probes.pending, 1);
    assert.equal(probes.malformed.message.includes("private-details"), false);
    assert.equal(probes.old.length, 8);
    assert.ok(probes.old.every(row => row.code === "canceled"));
    assert.equal(probes.staleExpired, 0);
    assert.equal(probes.staleWarnings, 0);
    assert.equal(probes.recovered, true);

    await page.getByLabel("用户名", { exact: true }).fill("fixture");
    await page.getByLabel("密码", { exact: true }).fill("fixture-password");
    await page.getByRole("button", { name: "登录工作空间", exact: true }).click();
    await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
    unavailableChannels = true;
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    await page.getByText("渠道配置暂不可用。", { exact: true }).waitFor();
    assert.equal(await page.locator("nav a[data-page=channels]").count(), 1, "optional 503 changed an authorized role");
    await page.getByRole("heading", { name: "我的资金", exact: true }).waitFor();
    unavailableChannels = false;
    malformedModels = false;
    const malformedKeys = [{ data: null }, {}, { data: { id: 101 } },
      { data: { id: 101, key: 123 } }, { data: { id: 101, key: "" } }];
    for (const [scope, pageID, title, opener] of [
      ["me", "keys", "我的 API Key", "+ 创建 Key"],
      ["admin", "admin-keys", "全站 API Key", "+ 为用户创建 Key"],
    ]) {
      await page.locator("nav a[data-page='" + pageID + "']").click();
      await page.getByRole("heading", { name: title, exact: true }).waitFor();
      for (let index = 0; index < malformedKeys.length; index++) {
        createAck = malformedKeys[index];
        const before = keyCreates[scope];
        await page.getByRole("button", { name: opener, exact: true }).click();
        const editor = page.getByRole("dialog");
        await editor.getByLabel("Key 名称", { exact: true }).fill("malformed-ack-" + scope + "-" + index);
        await editor.getByRole("button", { name: "保存更改", exact: true }).click();
        const feedback = editor.getByRole("alert");
        await feedback.waitFor();
        const message = await feedback.innerText();
        assert.match(message, /结果.*确认/);
        assert.match(message, /刷新/);
        assert.match(message, /重复/);
        assert.doesNotMatch(message, /TypeError|Cannot read/);
        assert.equal(keyCreates[scope], before + 1, "malformed Key acknowledgement was replayed");
        assert.equal(await page.getByRole("dialog", { name: "保存你的 API Key", exact: true }).count(), 0);
        assert.equal(await editor.getByLabel("Key 名称", { exact: true }).inputValue(), "malformed-ack-" + scope + "-" + index);
        page.once("dialog", dialog => dialog.accept());
        await editor.getByRole("button", { name: "取消", exact: true }).click();
        await editor.waitFor({ state: "detached" });
      }
      const secret = "sk-resilience-synthetic-one-time-key-" + scope;
      createAck = { data: { id: 101, key: secret } };
      const before = keyCreates[scope];
      await page.getByRole("button", { name: opener, exact: true }).click();
      await page.getByLabel("Key 名称", { exact: true }).fill("valid-ack-" + scope);
      await page.getByRole("button", { name: "保存更改", exact: true }).click();
      const saved = page.getByRole("dialog", { name: "保存你的 API Key", exact: true });
      await saved.waitFor();
      assert.equal(await saved.getByLabel("新 API Key", { exact: true }).inputValue(), secret);
      assert.equal(keyCreates[scope], before + 1);
      await saved.getByRole("button", { name: "我已保存", exact: true }).click();
      await saved.waitFor({ state: "detached" });
      assert.equal(await page.locator("textarea.secret").count(), 0);
    }
    malformedModels = true;
    await page.locator("nav a[data-page=models]").click();
    await page.getByRole("heading", { name: "读取失败", exact: true }).waitFor();
    assert.match(await page.locator("#view .form-error").innerText(), /无法识别/);
    assert.match(await page.locator("#view .form-error").innerText(), /models-invalid-1/);
    malformedModels = false;
    await page.getByRole("button", { name: "重试", exact: true }).click();
    await page.getByRole("heading", { name: "recovered-model", exact: true }).waitFor();
    forbiddenChannels = true;
    await page.locator("nav a[data-page=channels]").click();
    await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
    await page.locator("nav a[data-page=channels]").waitFor({ state: "detached" });
    assert.equal(await page.locator("nav a[data-page=channels]").count(), 0, "HTML 403 left administrator navigation visible");
    role = "admin";
    await page.locator("nav a[data-page=models]").click();
    await page.getByRole("heading", { name: "recovered-model", exact: true }).waitFor();
    assert.equal(await page.locator("nav a[data-page=channels]").count(), 1);
    forbiddenChannels = true;
    await page.locator("nav a[data-page=overview]").click();
    await page.locator("nav a[data-page=channels]").waitFor({ state: "detached" });
    await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
    role = "admin";
    await page.locator("nav a[data-page=models]").click();
    await page.getByRole("heading", { name: "recovered-model", exact: true }).waitFor();
    await page.locator("nav a[data-page=quotation]").click();
    await page.getByRole("heading", { name: "报价诊断", exact: true }).waitFor();
    await page.getByLabel("精确模型名称", { exact: true }).fill("recovered-model");
    forbiddenQuote = true;
    await page.getByRole("button", { name: "计算假设报价", exact: true }).click();
    await page.locator("nav a[data-page=quotation]").waitFor({ state: "detached" });
    await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
    assert.equal(await page.getByLabel("明确价格版本", { exact: true }).count(), 0, "revoked quotation retained a private form");
    expireIdentity = true;
    await page.locator("nav a[data-page=logs]").click();
    await page.getByRole("button", { name: "登录工作空间", exact: true }).waitFor();
    assert.match(await page.locator(".form-error").innerText(), /登录已过期/);
    assert.equal(await page.locator("nav, #view, dialog[open]").count(), 0, "expired identity retained a private view");
    assert.deepEqual(await page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } })), { local: {}, session: {} });
    for (const id of ["timeout-body", "forbidden-body", "unauthorized-body", "key-unauthorized-body", "expired-ui-body"]) {
      const until = Date.now() + 2000;
      while (!closed.has(id) && Date.now() < until) await new Promise(resolve => setTimeout(resolve, 20));
      assert.ok(closed.has(id), id + " remained open after cancellation");
    }
    assert.deepEqual(errors, []);
    await context.close();
    console.log("PASS: native HTTP recovery/no replay, slow 401/403, session fence, publication facts, malformed personal/admin Key ACK recovery, one-time Key lifecycle, optional 403/503 and quotation revocation");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
