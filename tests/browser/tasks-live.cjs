async function openNavigation(page) {
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible() && await menu.getAttribute("aria-expanded") !== "true") await menu.click();
}

const assert=require('node:assert/strict');
const {chromium}=require('playwright');
(async()=>{
 const base=new URL(process.env.TXAPI_BASE_URL),id=process.env.TXAPI_TEST_TASK_ID;
 assert.equal(base.protocol,'http:');assert.ok(['127.0.0.1','localhost','[::1]'].includes(base.hostname));assert.ok(id);
 const browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_EXECUTABLE_PATH?{executablePath:process.env.PLAYWRIGHT_EXECUTABLE_PATH}:{})});
 try{for(const width of [1440,390]){
 const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>d.accept());
 await context.route('**/*',route=>new URL(route.request().url()).origin===base.origin?route.continue():route.abort());
 await page.goto(base.href);await page.getByLabel('用户名',{exact:true}).fill(process.env.TXAPI_TEST_ADMIN_USERNAME);await page.getByLabel('密码',{exact:true}).fill(process.env.TXAPI_TEST_ADMIN_PASSWORD);await page.getByRole('button',{name:'登录工作空间'}).click();await page.getByRole('heading',{name:'工作空间总览'}).waitFor();
 await openNavigation(page);
 await page.locator('nav a[data-page=tasks]').click();await page.getByRole('heading',{name:'后台任务',exact:true}).waitFor();
 const row=()=>page.getByRole('row').filter({hasText:id});
 await row().getByRole('button',{name:'查看任务'}).click();await page.getByRole('heading',{name:'任务元数据',exact:true}).waitFor();
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true);
 if(process.env.TXAPI_TASK_SCREENSHOT_DIR)await page.screenshot({path:process.env.TXAPI_TASK_SCREENSHOT_DIR+'/tasks-live-'+width+'.png',fullPage:true});
 await page.getByLabel('任务查看范围').selectOption('admin');await row().getByRole('button',{name:'查看任务'}).click();await page.getByRole('heading',{name:'任务元数据',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'请求取消'}).count(),0);
 if(width===390){
 await page.getByLabel('任务查看范围').selectOption('me');await row().getByRole('button',{name:'查看任务'}).click();
 // A live worker may advance the revision. Each conflict requires another
 // explicit click after the page refreshes; the product must not retry POST.
 let recorded=false;
 for(let attempt=0;attempt<3;attempt++){
 const [response]=await Promise.all([page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/'+id+'/cancel')&&r.request().method()==='POST'),page.getByRole('button',{name:'请求取消'}).click()]);
 if(response.status()===409){await page.getByRole('alert').filter({hasText:'Task changed'}).waitFor();continue;}
 assert.equal(response.status(),200);recorded=true;break;
 }
 assert.equal(recorded,true,'Live task kept changing across three explicit cancellation attempts');
 await page.getByRole('status').filter({hasText:'取消请求已记录'}).waitFor();
 }
 assert.deepEqual(errors,[]);await context.close();
 }}finally{await browser.close();}
 console.log('PASS: actual main task console owner/admin metadata, cancellation and desktop/mobile');
})().catch(e=>{console.error('FAIL: live task browser '+e.stack);process.exitCode=1;});
