async function openNavigation(page) {
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible() && await menu.getAttribute("aria-expanded") !== "true") await menu.click();
}

const assert = require('node:assert/strict');
const { chromium } = require('playwright');

(async () => {
  const base=new URL(process.env.TXAPI_BASE_URL),id=process.env.TXAPI_ROUTING_TRACE_ID,requestID=process.env.TXAPI_ROUTING_REQUEST_ID;
  assert.equal(base.protocol,'http:');assert.ok(['127.0.0.1','localhost','[::1]'].includes(base.hostname));assert.match(id,/^rt_[a-f0-9]{32}$/);
  const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_EXECUTABLE_PATH}:{})});
  try{
    for(const admin of [false,true]){
        const context=await browser.newContext({viewport:{width:1440,height:900}}),page=await context.newPage();page.setDefaultTimeout(10000);
        const errors=[],mutations=[];page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{if(r.method()!=='GET'&&new URL(r.url()).pathname.includes('/routing'))mutations.push(r.method());});
        await context.route('**/*',route=>new URL(route.request().url()).origin===base.origin?route.continue():route.abort());
        await page.goto(base.href);
        await page.getByLabel('用户名',{exact:true}).fill(admin?'smoke-admin':'console-user');
        await page.getByLabel('密码',{exact:true}).fill(admin?'smoke-admin-password-only':'console-user-test-password');
        const [login]=await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname==='/api/auth/login'),page.getByRole('button',{name:'登录工作空间'}).click()]);
        assert.equal(login.status(),200,'routing live login failed');await page.getByRole('heading',{name:'工作空间总览'}).waitFor();
      // Reuse each real session across viewports to respect the login rate gate.
      for(const width of [1440,390]){
        await page.setViewportSize({width,height:900});
        await openNavigation(page);
        await page.locator('nav a[data-page=routing]').click();await page.getByRole('heading',{name:'路由诊断',exact:true}).waitFor();
        if(admin)await page.getByLabel('路由查看范围').selectOption('admin');
        else assert.equal(await page.getByLabel('路由查看范围').locator('option[value=admin]').count(),0);
        await page.getByLabel('精确 Request ID').fill(requestID);await page.getByRole('button',{name:'刷新诊断',exact:true}).click();
        await page.getByRole('button',{name:'查看路由'}).click();await page.getByRole('heading',{name:'路由事实',exact:true}).waitFor();
        await page.getByText(id,{exact:true}).waitFor();assert.equal(await page.getByRole('heading',{name:'选择过程',exact:true}).count(),1);
        assert.equal(await page.getByRole('heading',{name:'上游尝试',exact:true}).count(),1);
        if(admin)await page.getByText('mapped-model',{exact:true}).waitFor();
        else assert.equal(await page.getByText('mapped-model',{exact:true}).count(),0);
        assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true,'live mobile routing overflow');
        if(!admin&&process.env.TXAPI_ROUTING_SCREENSHOT_DIR)await page.screenshot({path:process.env.TXAPI_ROUTING_SCREENSHOT_DIR+'/routing-live-'+width+'.png',fullPage:true});
        assert.deepEqual(errors,[]);assert.deepEqual(mutations,[]);
        await openNavigation(page);
        await page.locator('nav a[data-page=overview]').click();await page.getByRole('heading',{name:'工作空间总览'}).waitFor();
      }
        await context.close();
    }
  }finally{await browser.close();}
  console.log('PASS: actual main routing console owner/admin views and desktop/mobile');
})().catch(e=>{console.error('FAIL: live routing browser '+e.stack);process.exitCode=1;});
