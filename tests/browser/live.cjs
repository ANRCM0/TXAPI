async function openNavigation(page) {
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible() && await menu.getAttribute("aria-expanded") !== "true") await menu.click();
}

// Run against an already-started disposable fixture; this script never starts a server.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
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
        await page.getByLabel("用户名", { exact: true }).fill(credentials[0]);
        await page.getByLabel("密码", { exact: true }).fill(credentials[1]);
        await page.getByRole("button", { name: "登录工作空间" }).click();
        await page.getByRole("heading", { name: "工作空间总览" }).waitFor();
      };
      const noOverflow = async () => assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true,
        `Page overflows ${viewport.width}px viewport`);
      await login(admin);
      stage = `${viewport.width}/channel-save`;
      await openNavigation(page);
      await page.locator('nav a[data-page="channels"]').click();
      await page.getByRole("button", { name: "+ 创建渠道", exact: true }).click();
      const channelDialog = page.getByRole("dialog", { name: "创建渠道", exact: true });
      await channelDialog.getByLabel("名称", { exact: true }).fill(channelName);
      await channelDialog.getByLabel("上游地址", { exact: true }).fill(upstream.href);
      await channelDialog.getByLabel("模型目录", { exact: true }).fill(model);
      await channelDialog.getByLabel("首个上游 API Key", { exact: true }).fill(`${upstreamKey}-first`);
      await channelDialog.getByRole("button", { name: "保存更改", exact: true }).click();
      await channelDialog.waitFor({ state: "detached" });
      const channelPanel = page.locator("section.panel").filter({ has: page.getByRole("heading", { name: channelName, exact: true }) });
      stage = `${viewport.width}/credential-save`;
      await channelPanel.getByRole("button", { name: "+ 添加凭据", exact: true }).click();
      const credentialDialog = page.getByRole("dialog", { name: "添加凭据", exact: true });
      await credentialDialog.getByLabel("名称", { exact: true }).fill(`backup-${viewport.width}`);
      await credentialDialog.getByLabel("上游密钥", { exact: true }).fill(upstreamKey);
      await credentialDialog.getByRole("button", { name: "保存更改", exact: true }).click();
      await credentialDialog.waitFor({ state: "detached" });
      await channelPanel.getByText(`backup-${viewport.width}`, { exact: true }).waitFor();
      await noOverflow();
      stage = `${viewport.width}/publication`;
        await openNavigation(page);
      const [publication] = await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname === "/api/admin/config-status" && response.request().method() === "GET"),
        page.locator('nav a[data-page="settings"]').click(),
      ]);
      await page.getByText("渠道与凭据", { exact: true }).waitFor();
      const status = await publication.json();
      assert.ok(status.data.epoch, "Configuration epoch missing from live API");
      for (const scope of ["channels", "prices"]) {
        assert.equal(status.data[scope].desired_revision, status.data[scope].applied_revision, `${scope} publication is pending`);
        assert.equal(status.data[scope].last_error || "", "", `${scope} publication failed`);
      }
      await page.getByText("渠道与凭据", { exact: true }).waitFor();
      await noOverflow();
      await page.getByRole("button", { name: "退出登录", exact: true }).click();
      await page.getByRole("heading", { name: "欢迎回来" }).waitFor();
      const boundary = requests.length;
      stage = `${viewport.width}/user-login`;
      await login(user);
      for (const name of ["用户与权限", "渠道管理", "价格与发布", "财务核对"]) {
        assert.equal(await page.locator("nav").getByRole("link", { name, exact: false }).count(), 0, `User sees ${name}`);
      }
      stage = `${viewport.width}/models`;
        await openNavigation(page);
      const [models] = await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname === "/api/me/models"),
        page.locator('nav a[data-page="models"]').click(),
      ]);
      const modelData = await models.json();
      assert.ok(modelData.data.includes(model), "Fixture model absent from owner API");
      await page.getByRole("heading", { name: model, exact: true }).waitFor();
      await noOverflow();
      stage = `${viewport.width}/key-create`;
      await openNavigation(page);
      await page.locator('nav a[data-page="keys"]').click();
      await page.getByRole("button", { name: "+ 创建 Key", exact: true }).click();
      const keyDialog = page.getByRole("dialog", { name: "创建 API Key", exact: true });
      await keyDialog.getByLabel("Key 名称", { exact: true }).fill(`live-key-${viewport.width}`);
      await keyDialog.getByLabel("允许的模型", { exact: true }).fill(model);
      await keyDialog.getByRole("button", { name: "保存更改", exact: true }).click();
      const secret = page.getByRole("dialog", { name: "保存你的 API Key", exact: true });
      const callKey = await secret.getByLabel("新 API Key", { exact: true }).inputValue();
      redactions.push(callKey);
      assert.ok(callKey, "New key was not shown once");
      await secret.getByRole("button", { name: "我已保存", exact: true }).click();
      stage = `${viewport.width}/playground`;
      await openNavigation(page);
      await page.locator('nav a[data-page="playground"]').click();
      await page.getByLabel("模型", { exact: true }).fill(model);
      await page.getByLabel("调用 API Key", { exact: true }).fill(callKey);
      await page.getByLabel("消息", { exact: true }).fill(`temporary browser request ${viewport.width}`);
      await page.getByRole("button", { name: "发送请求", exact: true }).click();
      await page.locator("pre.response").filter({ hasText: "s6-live-browser-ok" }).waitFor();
      assert.equal(await page.getByLabel("调用 API Key", { exact: true }).inputValue(), "", "Playground retained the API key");
      await noOverflow();
      stage = `${viewport.width}/logs`;
      await openNavigation(page);
      await page.locator('nav a[data-page="logs"]').click();
      await page.getByRole("heading", { name: "请求日志", exact: true }).waitFor();
      for (let attempt = 0; attempt < 20; attempt++) {
        if ((await page.locator("#view").innerText()).includes(model)) break;
        await page.getByRole("button", { name: "刷新", exact: true }).click();
        await page.waitForTimeout(100);
      }
      await page.getByRole("cell", { name: model, exact: true }).waitFor();
      await noOverflow();
      stage = `${viewport.width}/native-usage`;
      await openNavigation(page);
      const [nativeList] = await Promise.all([
        page.waitForResponse(r => new URL(r.url()).pathname === "/api/me/usage-records"),
        page.locator('nav a[data-page="usage"]').click(),
      ]);
      const native = await nativeList.json();
      assert.equal(native.enabled, true);
      assert.ok(native.data.some(row => row.model === model && row.attempt_count === 1));
      const usageRow = page.locator("tbody tr").filter({ hasText: model }).first();
      const [nativeDetail] = await Promise.all([
        page.waitForResponse(r => /\/api\/me\/usage-records\//.test(new URL(r.url()).pathname)),
        usageRow.getByRole("button", { name: "查看原生用量" }).click(),
      ]);
      const detail = (await nativeDetail.json()).data;
      assert.equal(detail.attempts[0].usage.input_tokens, "2");
      assert.equal(detail.attempts[0].usage.output_tokens, "1");
      assert.equal(detail.attempts[0].usage.total_tokens, "3");
      await page.getByRole("heading", { name: "调用观察", exact: true }).waitFor();
      await noOverflow();
      stage = `${viewport.width}/owner-finance`;
      await openNavigation(page);
      const [summary] = await Promise.all([
        page.waitForResponse(r => new URL(r.url()).pathname === "/api/me/billing/summary"),
        page.locator('nav a[data-page="finance"]').click(),
      ]);
      const financial = (await summary.json()).data;
      await page.getByRole("heading", { name: "个人账本", exact: true }).waitFor();
      if (!financial.balance_known) assert.ok((await page.locator(".stats").innerText()).includes("未知"));
      assert.equal(await page.getByRole("button", { name: "确认核对" }).count(), 0);
      await noOverflow();
      await openNavigation(page);
      await page.locator('nav a[data-page="overview"]').click();
      await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
      if (process.env.TXAPI_UI_SCREENSHOTS) {
        const dismiss = page.getByRole("button", { name: "关闭提示", exact: true });
        if (await dismiss.isVisible()) await dismiss.click();
        fs.mkdirSync(process.env.TXAPI_UI_SCREENSHOTS, { recursive: true });
        await page.screenshot({ path: path.join(process.env.TXAPI_UI_SCREENSHOTS, `txapi-console-${viewport.width}.png`), fullPage: true });
      }
      assert.equal(requests.slice(boundary).some(path => path.startsWith("/api/admin/")), false, "User navigation called an administrator API");
      assert.deepEqual(errors, [], "Browser runtime errors");
      await page.getByRole("button", { name: "退出登录", exact: true }).click();
      await page.getByRole("heading", { name: "欢迎回来" }).waitFor();
      await context.close();
    }
    console.log("PASS: real UI channel/credential saves, publication, restricted models/key, playground call, logs, native usage/detail, owner finance, role isolation and desktop/mobile layout");
  } finally {
    if (browser) await browser.close();
  }
})().catch(error => {
  let detail = `${error.name}: ${String(error.message).split(/\r?\n/, 1)[0]}`;
  for (const value of redactions.filter(Boolean).sort((a, b) => b.length - a.length)) detail = detail.split(value).join("[redacted]");
  console.error(`FAIL: live temporary API/browser acceptance [${stage}] ${detail}`);
  process.exitCode = 1;
});
