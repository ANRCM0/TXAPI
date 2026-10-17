const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require("playwright");

(async () => {
  const id = "0123456789abcdef0123456789abcdef";
  const now = new Date().toISOString();
  const reservation = {
    id, user_id: 2, token_id: 3, origin_model: "fixture-model", price_version: "a".repeat(64),
    reserved_units: "100000001", charge_units: "0", state: "pending", reason: "execution_or_usage_unknown:billing_fee_limit",
    provider_budget_units: "100000000", provider_held_units: "5760000",
    lease_until: Date.now() - 1000, can_resolve: true, created_at: now, updated_at: now,
  };
  let resolution = null;
  let role = "admin";
  let paginated = false;
  let failNextPage = true;
  const posted = [];
  const requests = [];
  const json = (res, value) => {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(value));
  };
  const server = http.createServer(async (req, res) => {
    requests.push(req.url);
    const url = new URL(req.url, "http://127.0.0.1");
    const assets = {
      "/": ["index.html", "text/html"],
      "/ui/app.mjs": ["app.mjs", "text/javascript"],
      "/ui/api.mjs": ["api.mjs", "text/javascript"],
      "/ui/app.css": ["app.css", "text/css"],
    };
    if (assets[url.pathname]) {
      const [name, type] = assets[url.pathname];
      res.writeHead(200, { "Content-Type": type });
      res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", name)));
    } else if (url.pathname === "/api/auth/login") {
      json(res, { session_token: "fixture-session-only", user: {
        id: 1, username: "fixture", role, model_mode: "all", models: [],
      } });
    } else if (url.pathname === "/api/auth/logout") {
      json(res, {});
    } else if (url.pathname === "/api/me") {
      json(res, { data: { id: 1, username: "fixture", role, model_mode: "all", models: [] } });
    } else if (url.pathname === "/api/me/usage") {
      json(res, { data: { quota: 1, used_quota: 0, usage: {} } });
    } else if (url.pathname === "/api/me/keys") {
      json(res, { data: [] });
    } else if (url.pathname === "/api/admin/billing/reservations") {
      if (paginated) {
        const offset = Number(url.searchParams.get("offset"));
        if (offset === 50 && failNextPage) {
          failNextPage = false;
          res.writeHead(500, { "Content-Type": "application/json" });
          res.end('{"error":{"message":"fixture-ledger-page-failure"}}');
        } else json(res, { data: Array.from({ length: 50 }, (_, index) => ({ ...reservation,
          id: `ledger-page-${offset + index}`, reason: offset ? "second page" : "first page" })), unit_scale: "1000000000" });
        return;
      }
      const state = url.searchParams.get("state");
      json(res, { data: !state || state === reservation.state ? [reservation] : [], unit_scale: "1000000000" });
    } else if (url.pathname === `/api/admin/billing/reservations/${id}`) {
      json(res, { data: {
        reservation, resolution, price_snapshot: '{"version":"fixture-history"}',
        attempts: [{ number: 1, channel_id: 4, credential_id: 5, actual_model: "fixture-model",
          group: "default", status: 0, termination: "", prompt_tokens: 0, output_tokens: 0,
          cost_units: "0", cost_known: false, state: "active", created_at: now,
          provider_bound_units: "5760000", provider_contract_version: "b".repeat(64),
          provider_contract_snapshot: '{"format":2,"selling_bound_units":"96000000"}' }],
      }, unit_scale: "1000000000" });
    } else if (url.pathname === `/api/admin/billing/reservations/${id}/resolve` && req.method === "POST") {
      let body = "";
      for await (const chunk of req) body += chunk;
      const input = JSON.parse(body);
      posted.push(input);
      resolution = { ...input, actor_id: 1, prompt_tokens: input.usage.prompt_tokens,
        output_tokens: input.usage.completion_tokens, cost_known: true,
        calculated_units: "1650000", charge_units: "1650000", created_at: now };
      Object.assign(reservation, { state: "settled", charge_units: resolution.charge_units,
        reason: "admin_settle_usage", can_resolve: false });
      json(res, { data: resolution, unit_scale: "1000000000" });
    } else {
      res.writeHead(404); res.end();
    }
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}),
    });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    const login = async () => {
      await page.getByLabel("Username", { exact: true }).fill("fixture");
      await page.getByLabel("Password", { exact: true }).fill("fixture-password-only");
      await page.getByRole("button", { name: "Enter control room" }).click();
    };
    const noOverflow = async () => {
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true,
        "billing page overflows the viewport");
      const dialog = page.getByRole("dialog");
      if (await dialog.count()) {
        assert.equal(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth + 1), true,
          "billing dialog overflows horizontally");
      }
    };
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await login();
    await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
    await page.getByRole("link", { name: "Billing ledger", exact: false }).click();
    await page.getByRole("button", { name: "Inspect", exact: true }).waitFor();
    assert.match(await page.locator("#view").innerText(), /Hold 0\.100000001/);
    assert.match(await page.locator("#view").innerText(), /execution_or_usage_unknown:billing_fee_limit/);
    await noOverflow();
    await page.getByRole("button", { name: "Inspect", exact: true }).click();
    await page.getByText("Unknown (not free)", { exact: true }).waitFor();
    await page.getByText("Provider attempt envelopes: 0.00576 / 0.1.", { exact: false }).waitFor();
    await page.getByText(`Snapshot ${"b".repeat(12)}`, { exact: true }).click();
    await page.getByText('{"format":2,"selling_bound_units":"96000000"}', { exact: true }).waitFor();
    await page.getByText("Historical tariff /", { exact: false }).click();
    await page.getByText('{"version":"fixture-history"}', { exact: true }).waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    await noOverflow();
    await page.getByRole("button", { name: "Resolve held charge", exact: true }).click();
    await page.getByLabel("Verified input tokens", { exact: true }).fill("11");
    await page.getByLabel("Verified output tokens", { exact: true }).fill("0");
    await page.getByLabel("Reason", { exact: true }).fill("verified receipt");
    await page.getByLabel("Verification / approval reference", { exact: true }).fill("case-fixture");
    await page.getByLabel("Confirm full billing ID", { exact: true }).fill("wrong-id");
    await page.getByRole("button", { name: "Commit decision", exact: true }).click();
    await page.getByRole("alert").filter({ hasText: "Enter the full billing ID" }).waitFor();
    assert.equal(posted.length, 0, "confirmation failure sent a financial command");
    await noOverflow();
    await page.getByLabel("Confirm full billing ID", { exact: true }).fill(id);
    await page.getByRole("button", { name: "Commit decision", exact: true }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    assert.deepEqual(posted, [{ action: "settle_usage", reason: "verified receipt", evidence: "case-fixture",
      usage: { prompt_tokens: 11, completion_tokens: 0 } }]);
    await page.getByLabel("Billing state", { exact: true }).selectOption("");
    await page.getByRole("button", { name: "Refresh ledger", exact: true }).click();
    await page.getByRole("button", { name: "Inspect", exact: true }).click();
    await page.getByRole("heading", { name: "Final administrator decision", exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "Resolve held charge", exact: true }).count(), 0);
    assert.match(await page.getByRole("dialog").innerText(), /Calculated: 0\.00165\. Charged: 0\.00165\./);
    await noOverflow();
    if (process.env.TXAPI_BROWSER_SCREENSHOT) await page.screenshot({ path: process.env.TXAPI_BROWSER_SCREENSHOT, fullPage: true });
    await page.getByRole("button", { name: "Close", exact: true }).click();
    paginated = true;
    await page.getByRole("button", { name: "Refresh ledger", exact: true }).click();
    await page.getByText("ledger-page-0", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Next", exact: true }).click();
    await page.getByRole("status").filter({ hasText: "fixture-ledger-page-failure" }).waitFor();
    assert.equal(await page.getByRole("button", { name: "Next", exact: true }).isEnabled(), true);
    await page.getByRole("button", { name: "Next", exact: true }).click();
    await page.getByText("ledger-page-50", { exact: true }).waitFor();
    await page.getByText("Rows 51-100", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page.getByRole("heading", { name: "Welcome back." }).waitFor();
    role = "user";
    const boundary = requests.length;
    await login();
    await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
    assert.equal(await page.getByRole("link", { name: "Billing ledger", exact: false }).count(), 0);
    assert.equal(requests.slice(boundary).some(url => url.startsWith("/api/admin/billing")), false);
    assert.deepEqual(errors, []);
    console.log("PASS: exact ledger amounts, unknown-cost warning, historical tariff, confirmed audited decision, role navigation and desktop/mobile layout");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
