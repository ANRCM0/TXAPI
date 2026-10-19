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
  const originalReservation = { ...reservation };
  let resolution = null;
  let attemptCost = { cost_units: "0", cost_known: false };
  let role = "admin";
  let paginated = false;
  let failNextPage = true;
  let failResolution = false;
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
          ...attemptCost, state: "active", created_at: now,
          provider_bound_units: "5760000", provider_contract_version: "b".repeat(64),
          provider_contract_snapshot: '{"format":2,"selling_bound_units":"96000000"}' }],
      }, unit_scale: "1000000000" });
    } else if (url.pathname === `/api/admin/billing/reservations/${id}/resolve` && req.method === "POST") {
      let body = "";
      for await (const chunk of req) body += chunk;
      const input = JSON.parse(body);
      posted.push(input);
      if (failResolution) {
        res.writeHead(503, { "Content-Type": "application/json" });
        res.end('{"error":{"message":"fixture-resolution-outcome-unknown"}}');
        return;
      }
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
    const assertListAmount = async (label, value) => {
      const table = page.locator("#view table").first();
      const headers = await table.locator("th").allTextContents();
      const index = headers.findIndex(header => header.trim() === label);
      assert.notEqual(index, -1, `missing ledger column: ${label}`);
      await page.waitForFunction(({ index, value }) =>
        document.querySelector("#view table tbody tr")?.children[index]?.innerText.trim() === value, { index, value });
      assert.equal((await table.locator("tbody tr").first().locator("td").nth(index).innerText()).trim(), value, label);
    };
    const assertDetailAmount = async (label, value) => {
      const escape = value => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      assert.match(await page.getByRole("dialog").innerText(),
        new RegExp(`${escape(label)}\\s*:?\\s*${escape(value)}(?:\\s|[.;]|$)`), label);
    };
    const refreshLedger = async () => {
      await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname === "/api/admin/billing/reservations"),
        page.getByRole("button", { name: "Refresh ledger", exact: true }).click(),
      ]);
      await page.getByRole("button", { name: "Inspect", exact: true }).first().waitFor();
    };
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await login();
    await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
    await page.getByRole("link", { name: "Billing ledger", exact: false }).click();
    await page.getByRole("button", { name: "Inspect", exact: true }).waitFor();
    await assertListAmount("Final user charge", "Not finalized");
    await assertListAmount("User budget retained", "0.100000001");
    assert.match(await page.locator("#view").innerText(),
      /User charges and holds use the account pricing unit\. Provider envelopes use their recorded contract currency; no currency conversion or comparison is implied\./);
    assert.doesNotMatch(await page.locator("#view").innerText(), /Amounts use the configured currency/);
    assert.match(await page.locator("#view").innerText(), /execution_or_usage_unknown:billing_fee_limit/);
    await noOverflow();
    await page.getByRole("button", { name: "Inspect", exact: true }).click();
    await page.getByText("Unknown (not free)", { exact: true }).waitFor();
    assert.match(await page.getByRole("dialog").innerText(), /Amounts use the account pricing unit\./);
    assert.match(await page.getByRole("dialog").innerText(),
      /Cumulative upper bounds in the recorded contract currency, without conversion to the account pricing unit\./);
    await assertDetailAmount("Original user reservation", "0.100000001");
    await assertDetailAmount("Current retained user budget", "0.100000001");
    await assertDetailAmount("Final user charge", "Not finalized");
    await assertDetailAmount("Cumulative provider envelope", "0.00576");
    await assertDetailAmount("Provider request budget", "0.1");
    await assertDetailAmount("Actual provider cost", "Not recorded");
    await page.getByRole("columnheader", { name: "Usage at selling tariff", exact: true }).waitFor();
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
    assert.match(await page.getByRole("dialog").innerText(), /Tariff-priced usage: 0\.00165\. Final user charge: 0\.00165\./);
    await assertListAmount("User budget retained", "0");
    await assertListAmount("Final user charge", "0.00165");
    await assertDetailAmount("Original user reservation", "0.100000001");
    await assertDetailAmount("Current retained user budget", "0");
    await assertDetailAmount("Cumulative provider envelope", "0.00576");
    await assertDetailAmount("Provider request budget", "0.1");
    await noOverflow();
    if (process.env.TXAPI_BROWSER_SCREENSHOT) await page.screenshot({ path: process.env.TXAPI_BROWSER_SCREENSHOT, fullPage: true });
    await page.getByRole("button", { name: "Close", exact: true }).click();
    const amountCases = [
      { name: "reserved", state: "reserved", retained: "0.100000001", final: "Not finalized", cost: "0.00165" },
      { name: "active known zero usage", state: "active", retained: "0.100000001", final: "Not finalized", cost: "0" },
      { name: "pending unknown usage", state: "pending", retained: "0.100000001", final: "Not finalized" },
      { name: "waived final zero", state: "settled", retained: "0", final: "0", waiver: true },
      { name: "unknown state", state: "future-state", retained: "Unknown", final: "Unknown" },
      { name: "old incomplete record", state: "settled", retained: "0", final: "Unknown", original: "Unknown",
        envelope: "Unknown", budget: "Unknown", fields: { reserved_units: null, charge_units: 0,
          provider_held_units: undefined, provider_budget_units: "invalid" } },
      { name: "maximum integer", state: "pending", retained: "9223372036.854775807", final: "Not finalized",
        original: "9223372036.854775807", envelope: "9223372036.854775807", budget: "9223372036.854775807",
        fields: { reserved_units: "9223372036854775807", provider_held_units: "9223372036854775807",
          provider_budget_units: "9223372036854775807", origin_model: '<img src=x onerror="alert(1)">' } },
    ];
    await page.getByLabel("Billing Key ID", { exact: true }).fill("3");
    await refreshLedger();
    assert.ok(requests.some(request => new URL(request, "http://fixture").searchParams.get("token_id") === "3"));
    await page.getByLabel("Billing Key ID", { exact: true }).fill("");
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 900 });
      for (const item of amountCases) {
        Object.assign(reservation, originalReservation, { state: item.state, can_resolve: false }, item.fields);
        resolution = item.waiver ? { action: "waive", actor_id: 1, reason: "verified waiver", evidence: "waiver-fixture",
          cost_known: false, calculated_units: "0", charge_units: "0", created_at: now } : null;
        attemptCost = item.cost === undefined ? { cost_known: false, cost_units: "0" } :
          { cost_known: true, cost_units: item.cost === "0" ? "0" : "1650000" };
        await refreshLedger();
        await assertListAmount("Final user charge", item.final);
        await assertListAmount("User budget retained", item.retained);
        await noOverflow();
        if (process.env.TXAPI_BILLING_SCREENSHOT_DIR && item.name === "maximum integer") {
          await page.screenshot({ path: path.join(process.env.TXAPI_BILLING_SCREENSHOT_DIR, `billing-max-list-${width}.png`), fullPage: true });
        }
        await page.getByRole("button", { name: "Inspect", exact: true }).click();
        await page.getByRole("dialog").waitFor();
        await assertDetailAmount("Original user reservation", item.original || "0.100000001");
        await assertDetailAmount("Current retained user budget", item.retained);
        await assertDetailAmount("Final user charge", item.final);
        await assertDetailAmount("Cumulative provider envelope", item.envelope || "0.00576");
        await assertDetailAmount("Provider request budget", item.budget || "0.1");
        await assertDetailAmount("Actual provider cost", "Not recorded");
        const usageTable = page.getByRole("dialog").locator("table");
        const usageHeaders = await usageTable.locator("th").allTextContents();
        const usageIndex = usageHeaders.indexOf("Usage at selling tariff");
        assert.notEqual(usageIndex, -1);
        assert.equal((await usageTable.locator("tbody tr").first().locator("td").nth(usageIndex).innerText()).trim(),
          item.cost === undefined ? "Unknown (not free)" : item.cost, item.name);
        assert.equal(await page.getByRole("button", { name: "Resolve held charge", exact: true }).count(), 0);
        assert.equal(await page.locator("#view img, dialog img").count(), 0, "ledger model became HTML");
        await noOverflow();
        if (process.env.TXAPI_BILLING_SCREENSHOT_DIR && item.name === "waived final zero") {
          await page.screenshot({ path: path.join(process.env.TXAPI_BILLING_SCREENSHOT_DIR, `billing-waived-${width}.png`), fullPage: true });
          await page.getByRole("heading", { name: "Final administrator decision", exact: true }).scrollIntoViewIfNeeded();
          await page.screenshot({ path: path.join(process.env.TXAPI_BILLING_SCREENSHOT_DIR, `billing-waived-decision-${width}.png`), fullPage: true });
        }
        await page.getByRole("button", { name: "Close", exact: true }).click();
      }
    }
    assert.equal(posted.length, 1, "amount inspection issued a financial command");
    Object.assign(reservation, originalReservation);
    resolution = null;
    failResolution = true;
    await refreshLedger();
    await assertListAmount("Final user charge", "Not finalized");
    await page.getByRole("button", { name: "Inspect", exact: true }).click();
    await page.getByRole("button", { name: "Resolve held charge", exact: true }).click();
    await page.getByLabel("Verified input tokens", { exact: true }).fill("11");
    await page.getByLabel("Verified output tokens", { exact: true }).fill("0");
    await page.getByLabel("Reason", { exact: true }).fill("verified receipt");
    await page.getByLabel("Verification / approval reference", { exact: true }).fill("case-fixture");
    await page.getByLabel("Confirm full billing ID", { exact: true }).fill(id);
    await page.getByRole("button", { name: "Commit decision", exact: true }).click();
    await page.getByRole("alert").filter({ hasText: "fixture-resolution-outcome-unknown" }).waitFor();
    await page.waitForTimeout(100);
    assert.equal(posted.length, 2, "failed financial command was automatically retried");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    Object.assign(reservation, originalReservation, { state: "settled", can_resolve: false, charge_units: "1650000" });
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
    console.log("PASS: ledger state/retention/final-charge semantics, waiver/unknown/legacy/max-int64 values, selling tariff, Key filter, historical snapshots, confirmed audited decision, pagination retry, role navigation and desktop/mobile layout");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
