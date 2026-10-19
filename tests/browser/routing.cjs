const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');

(async () => {
  let enabled = true, failPage = true, preflight = false, delayDetail = false, held;
  const requests = [];
  const id = 'rt_' + 'a'.repeat(32);
  const trace = {
    id, request_id: 'same-request', user_id: 1, token_id: 9, model: '<img src=x>', operation: 'chat',
    started_at: Date.now() - 100, finished_at: Date.now(), status: 502, http_status: 200,
    reason: 'failed', phase: 'response_received', termination: 'upstream_error', committed: true,
    protocol_complete: false, interrupted: false, truncated: false,
    selections: [{outcome:'selected',picker:'weighted',candidate_channels:2,eligible_channels:1,selection_pool_channels:1,total_credentials:3,tried_credentials:1,unavailable_credentials:1,eligible_credentials:1,priority_fallback:true,duration_micros:20}],
    attempts: [{number:1,status:502,http_status:200,phase:'response_received',termination:'upstream_error',committed:true,protocol_complete:false}],
  };
  const projection = admin => ({...trace,...(preflight?{status:400,http_status:400,phase:'not_dispatched',reason:'request_rejected'}:{}),
    selections:preflight?[]:trace.selections.map(row=>({...row,...(admin?{channel_id:19,credential_id:29}:{})})),
    attempts:preflight?[]:trace.attempts.map(row=>({...row,...(admin?{channel_id:19,credential_id:29,actual_model:'mapped-private-model'}:{})})),
  });
  const json = (res, status, value) => {res.writeHead(status, {'Content-Type':'application/json'});res.end(JSON.stringify(value));};
  const server = http.createServer((req,res) => {
    const u = new URL(req.url,'http://localhost'); requests.push({method:req.method,url:req.url});
    const asset = u.pathname==='/'?'index.html':u.pathname.startsWith('/ui/')?u.pathname.slice(4):null;
    if(asset && ['index.html','app.mjs','api.mjs','app.css'].includes(asset)){
      res.setHeader('Content-Type',asset.endsWith('mjs')?'text/javascript':asset.endsWith('css')?'text/css':'text/html');
      res.end(fs.readFileSync(path.join(__dirname,'../../web/assets',asset)));return;
    }
    if(u.pathname==='/api/auth/login')return json(res,200,{session_token:'fixture-session',user:{id:1,username:'fixture',role:'admin'}});
    if(u.pathname==='/api/me')return json(res,200,{data:{id:1,username:'fixture',role:'admin'}});
    if(u.pathname==='/api/me/usage')return json(res,200,{data:{usage:{}}});
    if(u.pathname==='/api/me/keys')return json(res,200,{data:[]});
    if(u.pathname.endsWith('/routing')){
      if(u.searchParams.get('offset')==='50' && failPage){failPage=false;return json(res,503,{error:{message:'routing page fixture failed'}});}
      return json(res,200,{enabled,capacity:512,retention_seconds:900,data:enabled?[projection(u.pathname.includes('/admin/'))]:[],has_more:enabled&&u.searchParams.get('offset')==='0'});
    }
    if(u.pathname.includes('/routing/')){
      if(delayDetail){held=()=>json(res,200,{data:{...projection(true),model:'stale-route-detail'}});return;}
      return json(res,200,{data:projection(u.pathname.includes('/admin/'))});
    }
    json(res,200,{data:[]});
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  let browser;
  try{
    const base='http://127.0.0.1:'+server.address().port;
    browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_EXECUTABLE_PATH}:{})});
    for(const width of [1440,390]){
      enabled=true;failPage=true;preflight=false;
      const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage();page.setDefaultTimeout(10000);
      const errors=[];page.on('pageerror',e=>errors.push(e.message));
      await context.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort());
      await page.goto(base);await page.getByLabel('Username',{exact:true}).fill('fixture');await page.getByLabel('Password',{exact:true}).fill('fixture-password');
      await page.getByRole('button',{name:'Enter control room'}).click();await page.getByRole('heading',{name:'Your gateway, at a glance.'}).waitFor();
      await page.locator('nav a[data-page=routing]').click();await page.getByRole('heading',{name:'Routing diagnostics',exact:true}).waitFor();
      await page.getByRole('button',{name:'Inspect routing',exact:true}).click();await page.getByRole('heading',{name:'Routing facts',exact:true}).waitFor();
      assert.equal(await page.locator('img').count(),0,'model became HTML');
      assert.equal(await page.getByText('mapped-private-model',{exact:true}).count(),0,'owner target metadata exposed');
      await page.getByLabel('Exact Request ID').fill('same-request');await page.getByRole('button',{name:'Refresh routes',exact:true}).click();
      await page.getByText('Rows 1-1',{exact:true}).waitFor();
      assert.ok(requests.some(r=>r.url.includes('request_id=same-request')));
      await page.getByRole('button',{name:'Next',exact:true}).click();await page.getByRole('alert').filter({hasText:'routing page fixture failed'}).waitFor();
      await page.getByRole('button',{name:'Next',exact:true}).click();await page.getByText('Rows 51-51',{exact:true}).waitFor();
      await page.getByLabel('Routing scope').selectOption('admin');await page.getByText('Rows 1-1',{exact:true}).waitFor();
      await page.getByRole('button',{name:'Inspect routing'}).click();await page.getByText('mapped-private-model',{exact:true}).waitFor();
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true,'mobile routing overflow');
      preflight=true;await page.getByRole('button',{name:'Refresh routes',exact:true}).click();await page.getByRole('button',{name:'Inspect routing'}).click();
      await page.getByText('No upstream attempt was observed in this scope.',{exact:true}).waitFor();
      await page.getByText('No target selection was observed in this scope.',{exact:true}).waitFor();preflight=false;
      enabled=false;await page.getByRole('button',{name:'Refresh routes',exact:true}).click();await page.getByText('Recording is disabled.',{exact:false}).waitFor();
      assert.equal(await page.getByRole('heading',{name:'Routing facts',exact:true}).count(),0,'old detail survived disabled recording');
      enabled=true;await page.getByRole('button',{name:'Refresh routes',exact:true}).click();await page.getByRole('button',{name:'Inspect routing'}).waitFor();
      delayDetail=true;
      await Promise.all([page.waitForRequest(r=>r.url().includes('/routing/'+id)),page.getByRole('button',{name:'Inspect routing'}).click()]);
      await page.locator('nav a[data-page=overview]').click();await page.getByRole('heading',{name:'Your gateway, at a glance.'}).waitFor();
      assert.ok(held,'delayed detail request reached the mock server');held();held=null;await page.waitForTimeout(80);delayDetail=false;
      assert.equal(await page.getByRole('heading',{name:'Routing facts',exact:true}).count(),0,'late routing detail replaced current view');
      assert.deepEqual(errors,[]);await context.close();
    }
    assert.equal(requests.some(r=>r.url.startsWith('/v1/')),false,'diagnostics initiated inference/content calls');
    assert.equal(requests.some(r=>r.method!=='GET'&&r.url.includes('/routing')),false,'read-only diagnostics sent a mutation');
    console.log('PASS: routing owner/admin projections, bounded paging retry, disabled state, stale responses, desktop/mobile');
  }finally{
    if(browser)await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));
  }
})().catch(e=>{console.error(e);process.exitCode=1;});
