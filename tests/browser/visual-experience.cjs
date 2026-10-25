const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require("playwright");

const textContrast = (page, selectors) => page.evaluate(selectors => {
  const luminance = color => {
    const rgb = color.match(/[\d.]+/g).slice(0, 3).map(v => Number(v) / 255)
      .map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4);
    return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722;
  };
  return selectors.map(selector => {
    const node = [...document.querySelectorAll(selector)].find(n => n.getClientRects().length && !n.closest("[inert]"));
    if (!node) return { selector, ratio: null };
    let parent = node;
    while (parent && getComputedStyle(parent).backgroundColor === "rgba(0, 0, 0, 0)") parent = parent.parentElement;
    const foreground = getComputedStyle(node)[node.namespaceURI === "http://www.w3.org/2000/svg" ? "fill" : "color"];
    const background = parent ? getComputedStyle(parent).backgroundColor : "rgb(255, 255, 255)";
    const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
    return { selector, ratio: (values[0] + .05) / (values[1] + .05) };
  });
}, selectors);

(async () => {
  const now = Date.now();
  const user = { id: 2, username: "visual-fixture", role: "user", model_mode: "all", models: [] };
  const model = "long-model-" + "context-".repeat(12);
  const records = [0, 1, 2].map(i => ({ created_at: new Date(now - i * 3600000).toISOString(),
    request_id: "visual-request-" + i, origin_model: model, status: 200, total_tokens: 4, latency_ms: 10 }));
  const errors = [];
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    const asset = url.pathname === "/" ? "index.html" : url.pathname.startsWith("/ui/") ? url.pathname.slice(4) : "";
    if (["index.html", "app.mjs", "api.mjs", "workspace.mjs", "app.css"].includes(asset)) {
      res.writeHead(200, { "Content-Type": asset.endsWith(".mjs") ? "text/javascript" : asset.endsWith(".css") ? "text/css" : "text/html",
        "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'" });
      return res.end(fs.readFileSync(path.join(__dirname, "../../web/assets", asset)));
    }
    for await (const unused of req) { /* Drain only synthetic fixture requests. */ }
    let value;
    if (url.pathname === "/api/auth/login") value = { session_token: "visual-fixture-session", user };
    else if (url.pathname === "/api/me") value = { data: user };
    else if (url.pathname === "/api/me/usage") value = { data: { usage: { total_requests: 3, total_tokens: 12, total_cost: 0 } } };
    else if (url.pathname === "/api/me/keys") value = { data: [{ id: 1, name: "Synthetic application", key_prefix: "sk-masked",
      status: "active", model_mode: "all", models: [] }] };
    else if (url.pathname === "/api/me/models") value = { data: [model], policy: { mode: "all", models: [] } };
    else if (url.pathname === "/api/me/logs") value = { data: records };
    else if (url.pathname === "/api/me/billing/summary") value = { data: { balance_known: true,
      posted_used_units: "0", held_units: "0", pending_count: "0" } };
    if (!value) { res.writeHead(404, { "Content-Type": "application/json" }); return res.end(JSON.stringify({ error: { message: "visual-fixture-not-found" } })); }
    res.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" });
    res.end(JSON.stringify(value));
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const origin = "http://127.0.0.1:" + server.address().port;
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}) });
    for (const width of [320, 390, 768, 1440]) {
      const context = await browser.newContext({ viewport: { width, height: 900 },
        hasTouch: width <= 768, reducedMotion: "reduce", acceptDownloads: true, timezoneId: "Asia/Shanghai" });
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      page.on("pageerror", error => errors.push(error.message));
      await context.route("**/*", route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
      await page.goto(origin);
      const loginContrast = await textContrast(page, [".login-story > .eyebrow", ...(width > 740 ? [".route-art strong"] : [])]);
      for (const value of loginContrast) assert.ok(value.ratio >= 4.5, width + "px " + value.selector + " contrast " + value.ratio);
      await page.getByLabel("用户名", { exact: true }).fill("visual-fixture");
      await page.getByLabel("密码", { exact: true }).fill("synthetic-password");
      await page.getByRole("button", { name: "登录工作空间", exact: true }).click();
      await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
      await page.locator(".trend-chart").waitFor();
      const overflow = () => page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
      assert.equal(await overflow(), false, width + "px overview overflowed");
      const recent = page.locator(".overview-secondary .table-wrap").first();
      assert.ok(await recent.locator("tbody tr").first().evaluate(node => node.getBoundingClientRect().height < 180),
        "long model metadata collapsed into an unreadable vertical column");
      if (width <= 390) assert.ok(await recent.evaluate(node => node.scrollWidth > node.clientWidth), "dense table must scroll inside its region");
      if (width === 768) {
        assert.ok(await recent.evaluate(node => node.scrollWidth > node.clientWidth), "tablet fixture must retain the overflowing table case");
        assert.equal(await page.locator(".table-scroll-hint").first().isVisible(), true,
          "tablet table overflow needs a visible scroll hint");
      }
      if (width === 1440) assert.equal(await page.locator(".table-scroll-hint").first().isVisible(), false,
        "desktop tables should not imply that visible columns are hidden");
      assert.equal(await page.locator(".content > div").evaluate(node => getComputedStyle(node).animationName), "none");
      const contrast = await textContrast(page, [".muted", ".fine", ".chart-label", ".workspace-footer", ".badge",
        ...(width > 740 ? [".avatar"] : [])]);
      for (const value of contrast) assert.ok(value.ratio >= 4.5, width + "px " + value.selector + " contrast " + value.ratio);
      if (width <= 768) {
        const targets = await page.locator(".button, .icon-button, .search-trigger").evaluateAll(nodes => nodes
          .filter(n => n.getClientRects().length && getComputedStyle(n).visibility !== "hidden")
          .map(n => ({ label: n.textContent || n.getAttribute("aria-label"), height: n.getBoundingClientRect().height,
            width: n.getBoundingClientRect().width })));
        assert.ok(targets.every(t => t.height >= 44 && t.width >= 44), JSON.stringify(targets));
      }
      if (process.env.TXAPI_UI_SCREENSHOTS) {
        fs.mkdirSync(process.env.TXAPI_UI_SCREENSHOTS, { recursive: true });
        await page.screenshot({ path: path.join(process.env.TXAPI_UI_SCREENSHOTS, "ux-visual-" + width + ".png"), fullPage: true });
      }
      const chart = await page.locator(".trend-chart").getAttribute("aria-label");
      assert.match(chart, /3.*24.*不代表全部请求/);
      const crossDay = await page.evaluate(async () => {
        const { sampleChart } = await import("/ui/workspace.mjs");
        const chart = sampleChart({ count: 3, buckets: [
          { time: Date.UTC(2026, 9, 24, 15, 30), requests: 1 },
          { time: Date.UTC(2026, 9, 24, 16, 30), requests: 2 },
        ] }, 220);
        document.body.append(chart);
        const result = { dates: [...chart.querySelectorAll(".chart-date")].map(n => n.textContent),
          summary: chart.getAttribute("aria-label"), description: chart.querySelector("desc").textContent };
        chart.remove();
        return result;
      });
      assert.deepEqual(crossDay.dates, ["10/24", "10/25"]);
      assert.match(crossDay.summary, /3.*2.*不代表全部请求/);
      assert.match(crossDay.description, /本地时区/);
      await page.evaluate(() => { location.hash = "keys"; });
      await page.getByRole("heading", { name: "我的 API Key", exact: true }).waitFor();
      await page.getByRole("button", { name: "+ 创建 Key", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "创建 API Key", exact: true });
      await dialog.waitFor();
      if (width <= 768) await page.setViewportSize({ width: 640, height: 360 });
      assert.equal(await overflow(), false, "dialog caused document overflow");
      await dialog.getByRole("button", { name: "保存更改", exact: true }).scrollIntoViewIfNeeded();
      const button = await dialog.getByRole("button", { name: "保存更改", exact: true }).boundingBox();
      const viewport = page.viewportSize();
      assert.ok(button.y >= 0 && button.y + button.height <= viewport.height, "dialog action is not reachable in short viewport");
      const lastField = dialog.locator("input").last();
      await lastField.focus();
      const fieldBox = await lastField.boundingBox();
      const footerBox = await dialog.locator("form > .actions").boundingBox();
      assert.ok(fieldBox.y >= 0 && fieldBox.y + fieldBox.height <= footerBox.y,
        "sticky dialog actions cover the focused final input");
      if (process.env.TXAPI_UI_SCREENSHOTS && width === 390) {
        await page.screenshot({ path: path.join(process.env.TXAPI_UI_SCREENSHOTS, "ux-dialog-short-640x360.png") });
      }
      await page.keyboard.press("Escape");
      await dialog.waitFor({ state: "detached" });
      await page.evaluate(() => { location.hash = "overview"; });
      await page.getByRole("heading", { name: "工作空间总览", exact: true }).waitFor();
      const downloaded = page.waitForEvent("download");
      await page.evaluate(async () => {
        const { downloadCSV } = await import("/ui/workspace.mjs");
        downloadCSV("visual-sample.csv", ["模型", "数量"], [["=SUM(1)", "0"], [" @cmd", "未知"]]);
      });
      const download = await downloaded;
      assert.equal(download.suggestedFilename(), "visual-sample.csv");
      const stream = await download.createReadStream();
      const parts = [];
      for await (const part of stream) parts.push(part);
      const csv = Buffer.concat(parts).toString("utf8");
      assert.ok(csv.startsWith("\uFEFF"));
      assert.ok(csv.includes('"\'=SUM(1)"') && csv.includes('"\' @cmd"') && csv.includes('"未知"'));
      assert.equal(await page.locator('a[download="visual-sample.csv"]').count(), 0);
      if (width === 1440) {
        await page.setViewportSize({ width: 720, height: 450 });
        assert.equal(await overflow(), false, "200% equivalent reflow exceeded the 720px layout viewport");
        await page.emulateMedia({ forcedColors: "active" });
        await page.getByRole("button", { name: "刷新", exact: true }).focus();
        const focus = await page.getByRole("button", { name: "刷新", exact: true }).evaluate(node => ({
          outline: getComputedStyle(node).outlineStyle, width: parseFloat(getComputedStyle(node).outlineWidth) }));
        assert.notEqual(focus.outline, "none"); assert.ok(focus.width >= 2);
        assert.equal(await overflow(), false);
      }
      await context.close();
    }
    assert.deepEqual(errors, []);
    console.log("PASS: visual contrast, 320/390/768/1440 reflow, touch targets, short-dialog reachability, reduced motion, forced colors, chart scope and real CSV download");
  } finally {
    if (browser) await browser.close();
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
