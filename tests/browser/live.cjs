// Run against an already-started disposable fixture; this script never starts a server.
const assert = require("node:assert/strict");
const { chromium } = require("playwright");
let stage = "configuration";
const redactions = [];

function required(name) {
  assert.ok(process.env[name], `Set ${name} to a disposable fixture value`);
  return process.env[name];
}

(async () => {
  const base = new URL(required("TXAPI_BASE_URL"));
  assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Live acceptance requires a loopback fixture");
  assert.equal(base.protocol, "http:", "Use the disposable local HTTP fixture");
  const admin = [required("TXAPI_TEST_ADMIN_USERNAME"), required("TXAPI_TEST_ADMIN_PASSWORD")];
  const user = [required("TXAPI_TEST_USER_USERNAME"), required("TXAPI_TEST_USER_PASSWORD")];
  const expectedModel = required("TXAPI_TEST_MODEL");
  const upstream = new URL(required("TXAPI_TEST_UPSTREAM_URL"));
  assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(upstream.hostname) && upstream.protocol === "http:", "Use a local fixture upstream");
  const upstreamKey = required("TXAPI_TEST_UPSTREAM_KEY");
  redactions.push(...admin, ...user, upstreamKey);
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}),
    });
    for (const viewport of [{ width: 1440, height: 1050 }, { width: 390, height: 844 }]) {
      stage = `${viewport.width}/admin-login`;
      const model = `${expectedModel}-${viewport.width}`;
      const channelName = `live-channel-${viewport.width}`;
      const context = await browser.newContext({ viewport, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      const errors = [];
      const requests = [];
      page.on("pageerror", error => errors.push(error.message));
      page.on("request", request => requests.push(new URL(request.url()).pathname));
      // Prevent a page or misconfigured asset from contacting any real upstream.
      await context.route("**/*", route => {
        const url = new URL(route.request().url());
        return url.origin === base.origin ? route.continue() : route.abort();
      });
      const login = async credentials => {
        await page.goto(base.href);
        await page.getByLabel("Username", { exact: true }).fill(credentials[0]);
        await page.getByLabel("Password", { exact: true }).fill(credentials[1]);
        await page.getByRole("button", { name: "Enter control room" }).click();
        await page.getByRole("heading", { name: "Your gateway, at a glance." }).waitFor();
      };
      const noOverflow = async () => assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true,
        `Page overflows ${viewport.width}px viewport`);
      await login(admin);
      stage = `${viewport.width}/channel-save`;
      await page.locator('nav a[data-page="channels"]').click();
      await page.getByRole("button", { name: "+ Create channel", exact: true }).click();
      const channelDialog = page.getByRole("dialog", { name: "Create channel", exact: true });
      await channelDialog.getByLabel("Name", { exact: true }).fill(channelName);
      await channelDialog.getByLabel("Upstream base URL", { exact: true }).fill(upstream.href);
      await channelDialog.getByLabel("Models", { exact: true }).fill(model);
      await channelDialog.getByLabel("First upstream API key", { exact: true }).fill(`${upstreamKey}-first`);
      await channelDialog.getByRole("button", { name: "Save changes", exact: true }).click();
      await channelDialog.waitFor({ state: "detached" });
      const channelPanel = page.locator("section.panel").filter({ has: page.getByRole("heading", { name: channelName, exact: true }) });
      stage = `${viewport.width}/credential-save`;
      await channelPanel.getByRole("button", { name: "+ Add credential", exact: true }).click();
      const credentialDialog = page.getByRole("dialog", { name: "Add credential", exact: true });
      await credentialDialog.getByLabel("Name", { exact: true }).fill(`backup-${viewport.width}`);
      await credentialDialog.getByLabel("Upstream key", { exact: true }).fill(upstreamKey);
      await credentialDialog.getByRole("button", { name: "Save changes", exact: true }).click();
      await credentialDialog.waitFor({ state: "detached" });
      await channelPanel.getByText(`backup-${viewport.width}`, { exact: true }).waitFor();
      await noOverflow();
      stage = `${viewport.width}/publication`;
      const [publication] = await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname === "/api/admin/config-status" && response.request().method() === "GET"),
        page.locator('nav a[data-page="settings"]').click(),
      ]);
      await page.getByText("Channels & credentials", { exact: true }).waitFor();
      const status = await publication.json();
      assert.ok(status.data.epoch, "Configuration epoch missing from live API");
      for (const scope of ["channels", "prices"]) {
        assert.equal(status.data[scope].desired_revision, status.data[scope].applied_revision, `${scope} publication is pending`);
        assert.equal(status.data[scope].last_error || "", "", `${scope} publication failed`);
      }
      await page.getByText("Channels & credentials", { exact: true }).waitFor();
      await noOverflow();
      await page.getByRole("button", { name: "Sign out", exact: true }).click();
      await page.getByRole("heading", { name: "Welcome back." }).waitFor();
      const boundary = requests.length;
      stage = `${viewport.width}/user-login`;
      await login(user);
      for (const name of ["Users", "Channels", "Settings", "Billing ledger"]) {
        assert.equal(await page.getByRole("link", { name, exact: false }).count(), 0, `User sees ${name}`);
      }
      stage = `${viewport.width}/models`;
      const [models] = await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname === "/api/me/models"),
        page.locator('nav a[data-page="models"]').click(),
      ]);
      const modelData = await models.json();
      assert.ok(modelData.data.includes(model), "Fixture model absent from owner API");
      await page.getByRole("heading", { name: model, exact: true }).waitFor();
      await noOverflow();
      stage = `${viewport.width}/key-create`;
      await page.locator('nav a[data-page="keys"]').click();
      await page.getByRole("button", { name: "+ Create key", exact: true }).click();
      const keyDialog = page.getByRole("dialog", { name: "Create API key", exact: true });
      await keyDialog.getByLabel("Key name", { exact: true }).fill(`live-key-${viewport.width}`);
      await keyDialog.getByLabel("Allowed models", { exact: true }).fill(model);
      await keyDialog.getByRole("button", { name: "Save changes", exact: true }).click();
      const secret = page.getByRole("dialog", { name: "Save your API key", exact: true });
      const callKey = await secret.getByLabel("New API key", { exact: true }).inputValue();
      redactions.push(callKey);
      assert.ok(callKey, "New key was not shown once");
      await secret.getByRole("button", { name: "I saved it", exact: true }).click();
      stage = `${viewport.width}/playground`;
      await page.locator('nav a[data-page="playground"]').click();
      await page.getByLabel("Model", { exact: true }).fill(model);
      await page.getByLabel("API key", { exact: true }).fill(callKey);
      await page.getByLabel("Message", { exact: true }).fill(`temporary browser request ${viewport.width}`);
      await page.getByRole("button", { name: "Send request", exact: true }).click();
      await page.locator("pre.response").filter({ hasText: "s6-live-browser-ok" }).waitFor();
      assert.equal(await page.getByLabel("API key", { exact: true }).inputValue(), "", "Playground retained the API key");
      await noOverflow();
      stage = `${viewport.width}/logs`;
      await page.locator('nav a[data-page="logs"]').click();
      await page.getByRole("heading", { name: "Request history", exact: true }).waitFor();
      for (let attempt = 0; attempt < 20; attempt++) {
        if ((await page.locator("#view").innerText()).includes(model)) break;
        await page.getByRole("button", { name: "Refresh", exact: true }).click();
        await page.waitForTimeout(100);
      }
      await page.getByRole("cell", { name: model, exact: true }).waitFor();
      await noOverflow();
      assert.equal(requests.slice(boundary).some(path => path.startsWith("/api/admin/")), false, "User navigation called an administrator API");
      assert.deepEqual(errors, [], "Browser runtime errors");
      await page.getByRole("button", { name: "Sign out", exact: true }).click();
      await page.getByRole("heading", { name: "Welcome back." }).waitFor();
      await context.close();
    }
    console.log("PASS: real UI channel/credential saves, publication, restricted models/key, playground call, logs, role isolation and desktop/mobile layout");
  } finally {
    if (browser) await browser.close();
  }
})().catch(error => {
  let detail = `${error.name}: ${String(error.message).split(/\r?\n/, 1)[0]}`;
  for (const value of redactions.filter(Boolean).sort((a, b) => b.length - a.length)) detail = detail.split(value).join("[redacted]");
  console.error(`FAIL: live temporary API/browser acceptance [${stage}] ${detail}`);
  process.exitCode = 1;
});
