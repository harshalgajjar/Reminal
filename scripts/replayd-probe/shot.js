// Playwright Chrome taking CDP screenshots in a loop for argv[2] seconds.
const { chromium } = require('playwright');
(async () => {
  const b = await chromium.launch({ channel: 'chrome', headless: false,
    args: ['--enable-features=CDPScreenshotNewSurface'] });
  const p = await b.newPage();
  await p.setContent('<div id=t style="font:40px sans-serif"></div><script>setInterval(()=>t.textContent=Date.now(),50)</script>');
  const end = Date.now() + Number(process.argv[2]) * 1000;
  let n = 0;
  while (Date.now() < end) { await p.screenshot(); n++; }
  console.log(`shots=${n} browser_pid=${b.process ? '' : ''}`);
  await b.close();
})();
