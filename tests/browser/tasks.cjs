async function openNavigation(page) {
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible() && await menu.getAttribute("aria-expanded") !== "true") await menu.click();
}

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
(async () => {
  let failPage = true, revision = '9007199254740993', posts = [], delay = false, held;
  const row = { id: 'resp_' + 'a'.repeat(64), user_id: 1, token_id: 9, model: '<img src=x>', stage: 'remote_active', revision, created_at: Date.now(), deadline: Date.now()+60000, content_expires_at: Date.now()+60000, metadata_expires_at: Date.now()+120000, cancel_requested: false, content_available: true, event_count: 1, last_sequence: 0, replay_pending: false, can_cancel: true };
  const requests = [];
  const json = (res,status,value) => { res.writeHead(status,{'Content-Type':'application/json'}); res.end(JSON.stringify(value)); };
  const server = http.createServer(async (req,res) => {
    const u = new URL(req.url,'http://localhost'); requests.push(req.url);
    const asset = u.pathname==='/' ? 'index.html' : u.pathname.startsWith('/ui/') ? u.pathname.slice(4) : null;
    if (asset && ['index.html','app.mjs','api.mjs','app.css','workspace.mjs'].includes(asset)) { res.setHeader('Content-Type',asset.endsWith('mjs')?'text/javascript':asset.endsWith('css')?'text/css':'text/html');res.end(fs.readFileSync(path.join(__dirname,'../../web/assets',asset)));return; }
    if (u.pathname==='/api/auth/login') return json(res,200,{session_token:'fixture-task-session',user:{id:1,username:'fixture',role:'admin'}});
    if (u.pathname==='/api/me') return json(res,200,{data:{id:1,username:'fixture',role:'admin'}});
    if (u.pathname==='/api/me/usage') return json(res,200,{data:{usage:{}}});
    if (u.pathname==='/api/me/keys') return json(res,200,{data:[]});
    if (/\/background$/.test(u.pathname)) {
      if (u.searchParams.get('offset')==='50' && failPage) {failPage=false;return json(res,503,{error:{message:'page fixture failed'}});}
      return json(res,200,{data:[{...row,revision,can_cancel:!row.cancel_requested&&u.pathname.includes('/me/')}],has_more:u.searchParams.get('offset')==='0'});
    }
    if (u.pathname.endsWith('/cancel')) { let body='';for await(const chunk of req)body+=chunk;posts.push(JSON.parse(body));if(posts.length===1){revision='9007199254740994';return json(res,409,{error:{message:'Task changed. Refresh before requesting cancellation.'}});}row.cancel_requested=true;row.can_cancel=false;return json(res,200,{data:{...row,revision}}); }
    if (u.pathname.includes('/background/')) {if(delay){held=()=>json(res,200,{data:{...row,model:'stale-detail'}});return;}return json(res,200,{data:{...row,revision,can_cancel:!row.cancel_requested&&u.pathname.includes('/me/')}});}
    json(res,200,{});
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));let browser;
  try {
    browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_EXECUTABLE_PATH}:{})});
    for (const width of [1440,390]) {
      revision='9007199254740993';failPage=true;posts.length=0;row.cancel_requested=false;row.can_cancel=true;
      const context=await browser.newContext({viewport:{width,height:900}});const page=await context.newPage();page.setDefaultTimeout(10000);
      const errors=[];page.on('pageerror',e=>errors.push(e.message));page.on('dialog',dialog=>dialog.accept());
      await context.route('**/*',route=>new URL(route.request().url()).origin==='http://127.0.0.1:'+server.address().port?route.continue():route.abort());
      await page.goto('http://127.0.0.1:'+server.address().port);
      await page.getByLabel('用户名',{exact:true}).fill('fixture');await page.getByLabel('密码',{exact:true}).fill('fixture-password');
      await page.getByRole('button',{name:'登录工作空间'}).click();await page.getByRole('heading',{name:'工作空间总览'}).waitFor();
      await openNavigation(page);
      await page.locator('nav a[data-page="tasks"]').click();await page.getByRole('heading',{name:'后台任务',exact:true}).waitFor();
      await page.getByRole('button',{name:'下一页',exact:true}).click();await page.getByRole('alert').filter({hasText:'page fixture failed'}).waitFor();
      await page.getByRole('status').filter({hasText:'读取未完成，可重试原页'}).waitFor();
      assert.equal(await page.getByText('记录 1-1',{exact:true}).count(),0,'failed page retained the previous count');
      assert.equal(await page.getByRole('button',{name:'查看任务',exact:true}).count(),0,'failed page retained a stale task');
      assert.equal(await page.getByRole('button',{name:'下一页',exact:true}).isEnabled(),true,'same-filter retry was disabled');
      await page.getByRole('button',{name:'下一页',exact:true}).click();await page.getByText('记录 51-51',{exact:true}).waitFor();
      await page.getByRole('button',{name:'查看任务',exact:true}).click();await page.getByText(revision,{exact:true}).waitFor();
      await page.getByRole('button',{name:'请求取消'}).click();await page.getByRole('alert').filter({hasText:'Task changed'}).waitFor();
      assert.equal(posts.length,1);assert.equal(posts[0].revision,'9007199254740993');
      await page.getByRole('button',{name:'请求取消'}).click();await page.getByRole('status').filter({hasText:'取消请求已记录'}).waitFor();
      assert.equal(posts.length,2);assert.equal(posts[1].revision,'9007199254740994');
      await page.getByRole('button',{name:'查看任务'}).click();await page.getByRole('heading',{name:'任务元数据',exact:true}).waitFor();
      console.log('layout',width,await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth,nodes:[...document.querySelectorAll('#view *')].filter(e=>e.getBoundingClientRect().right>innerWidth+1).slice(0,8).map(e=>({tag:e.tagName,cls:e.className,right:e.getBoundingClientRect().right,text:e.textContent.slice(0,80)}))})));
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true,'mobile overflow');
      await page.getByLabel('任务查看范围').selectOption('admin');await page.getByText('记录 1-1',{exact:true}).waitFor();
      await page.getByRole('button',{name:'查看任务'}).first().click();await page.getByRole('heading',{name:'任务元数据',exact:true}).waitFor();
      assert.equal(await page.getByRole('button',{name:'请求取消'}).count(),0,'administrator mutation exposed');
      delay=true;await page.getByRole('button',{name:'查看任务'}).first().click();
      await openNavigation(page);
      await page.locator('nav a[data-page="overview"]').click();await page.getByRole('heading',{name:'工作空间总览'}).waitFor();
      if(held){held();held=null;}await page.waitForTimeout(100);assert.equal(await page.getByRole('heading',{name:'任务元数据',exact:true}).count(),0);delay=false;
      assert.deepEqual(errors,[]);await context.close();
    }
    assert.equal(requests.some(p=>p.startsWith('/v1/')),false,'console fetched content through inference API');
    console.log('PASS: task pagination retry, exact revisions/CAS, read-only admin, navigation cancellation, desktop/mobile');
  } finally {if(browser)await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
})().catch(e=>{console.error(e);process.exitCode=1;});
