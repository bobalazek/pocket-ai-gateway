#!/usr/bin/env node
// Capture the disposable demo only. Run scripts/demo.sh first, then set PAG_DEMO_PASSWORD
// to the printed temporary password. CHROME_BIN may point to Chrome or Chromium.

import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";

const pages = [
  { name: "overview", path: "/_/", heading: "Overview", ready: "document.querySelectorAll('.activity-list li').length > 0 && document.querySelector('.overview-metrics strong')?.textContent !== '…'" },
  { name: "usage", path: "/_/usage/", heading: "Usage and limits", ready: "document.querySelectorAll('.usage-chart-grid svg').length >= 2" },
  { name: "requests", path: "/_/requests/", heading: "Requests", ready: "document.querySelectorAll('.request-table tbody tr').length > 0" },
  { name: "providers", path: "/_/providers/", heading: "Providers", ready: "document.querySelectorAll('main .resource-list > .panel').length > 0" },
  { name: "models", path: "/_/models/", heading: "Models", ready: "document.querySelectorAll('main .resource-list > .panel').length > 0" },
  { name: "keys", path: "/_/keys/", heading: "API keys", ready: "document.querySelectorAll('main .resource-list > .resource-row').length > 0" },
  { name: "users", path: "/_/users/", heading: "Users", ready: "document.querySelectorAll('main .resource-list > .resource-row').length > 0" },
  { name: "audit", path: "/_/audit/", heading: "Audit log", ready: "document.querySelectorAll('main .resource-list > .resource-row').length > 0" },
  { name: "settings", path: "/_/settings/", heading: "Settings and recovery", ready: "document.querySelector('main h2')?.textContent === 'Backup schedule'" },
  { name: "status", path: "/_/status/", heading: "Status", ready: "document.querySelectorAll('.status-list strong[data-state=ready]').length >= 2 && document.querySelector('#operational-status-title') && !document.querySelector('.operational-status [role=status]')" },
  { name: "playground", path: "/_/playground/", heading: "Playground", ready: "document.querySelector('form #prompt') && document.querySelector('form #protocol')" },
  { name: "account", path: "/_/account/", heading: "Your profile and sessions.", ready: "document.querySelectorAll('main .resource-list .resource-row').length > 0" },
  { name: "media-jobs", path: "/_/media-jobs/", heading: "Media jobs", ready: "document.querySelector('main .resource-list')?.textContent.includes('No media jobs') || document.querySelectorAll('main .resource-list > .panel').length > 0" },
];
const mobileNames = new Set(["overview", "usage", "requests", "status"]);

function options(args) {
  if (args.includes("--help")) {
    console.log("Usage: PAG_DEMO_PASSWORD=<temporary demo password> [CHROME_BIN=/path/to/chrome] node scripts/capture-demo-screenshots.mjs [--base-url http://127.0.0.1:18084] [--output-dir docs/images]");
    process.exit(0);
  }
  let baseURL = "http://127.0.0.1:18084";
  let outputDir = "docs/images";
  for (let i = 0; i < args.length; i += 2) {
    if (!args[i + 1]) throw new Error(`Missing value for ${args[i]}`);
    if (args[i] === "--base-url") baseURL = args[i + 1];
    else if (args[i] === "--output-dir") outputDir = args[i + 1];
    else throw new Error(`Unknown option: ${args[i]}`);
  }
  const origin = new URL(baseURL);
  if (origin.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname) || origin.pathname !== "/" || origin.search || origin.hash || origin.username || origin.password) {
    throw new Error("--base-url must be a local HTTP origin, for example http://127.0.0.1:18084");
  }
  if (!process.env.PAG_DEMO_PASSWORD) throw new Error("Set PAG_DEMO_PASSWORD to the temporary password printed by scripts/demo.sh");
  return { baseURL: origin.origin, outputDir: resolve(outputDir) };
}

const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

async function waitFor(predicate, timeoutMs, label) {
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    const value = await predicate();
    if (value) return value;
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${label}`);
}

async function capture({ baseURL, outputDir }) {
  const manifest = [];
  const chromeBin = process.env.CHROME_BIN || (process.platform === "darwin" ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" : "google-chrome");
  const profile = await mkdtemp(join(tmpdir(), "pag-capture-"));
  const chrome = spawn(chromeBin, [
    "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-networking",
    "--disable-extensions", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
    "--remote-allow-origins=*", `--user-data-dir=${profile}`, "about:blank",
  ], { stdio: "ignore" });
  let launchError;
  chrome.on("error", (error) => { launchError = error; });
  let socket;
  try {
    const port = await waitFor(async () => {
      if (launchError) throw launchError;
      if (chrome.exitCode !== null) throw new Error(`Chrome exited with code ${chrome.exitCode}`);
      try { return Number((await readFile(join(profile, "DevToolsActivePort"), "utf8")).split("\n")[0]); }
      catch { return 0; }
    }, 15000, "Chrome's local debugging port");
    const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
    const target = targets.find((item) => item.type === "page");
    if (!target) throw new Error("Chrome opened without a page target");
    socket = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((done, reject) => { socket.onopen = done; socket.onerror = reject; });

    let nextID = 0;
    const pending = new Map();
    socket.onmessage = ({ data }) => {
      const message = JSON.parse(data);
      const request = pending.get(message.id);
      if (!request) return;
      pending.delete(message.id);
      clearTimeout(request.timer);
      message.error ? request.reject(new Error(message.error.message)) : request.resolve(message.result);
    };
    const send = (method, params = {}) => new Promise((done, reject) => {
      const id = ++nextID;
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`${method} timed out`)); }, 30000);
      pending.set(id, { resolve: done, reject, timer });
      socket.send(JSON.stringify({ id, method, params }));
    });
    const evaluate = async (expression) => {
      const result = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
      return result.result.value;
    };
    const setViewport = async (width, height, mobile) => send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
    const navigate = async (path, heading, ready) => {
      const result = await send("Page.navigate", { url: baseURL + path });
      if (result.errorText) throw new Error(`${path}: ${result.errorText}`);
      await waitFor(async () => {
        const state = await evaluate(`(() => ({
          path: location.pathname + location.search,
          heading: document.querySelector('main h1')?.textContent?.trim(),
          alert: document.querySelector('main [role=alert]')?.textContent?.trim(),
          broken: document.body?.textContent?.includes('Application error: a client-side exception'),
          ready: Boolean(${ready}),
        }))()`);
        if (state.alert || state.broken) throw new Error(`${path}: ${state.alert || "client-side error boundary"}`);
        return state.path === path && state.heading === heading && state.ready;
      }, 15000, `${heading} to load at ${path}`);
      await evaluate("document.fonts.ready.then(() => true)");
      // Let layout observers finish after data settles. Charts disable entrance animation.
      await sleep(450);
      const width = await evaluate("document.documentElement.clientWidth");
      const overflow = await evaluate("document.documentElement.scrollWidth > document.documentElement.clientWidth + 1");
      if (overflow) throw new Error(`${path}: page has horizontal overflow at ${width}px`);
    };
    const screenshot = async (filename, width, page) => {
      const height = await evaluate("Math.ceil(Math.max(document.documentElement.scrollHeight, document.body.scrollHeight))");
      if (height > 30000) throw new Error(`${filename}: page is ${height}px tall; capture it in smaller sections`);
      const result = await send("Page.captureScreenshot", {
        format: "png", captureBeyondViewport: true, fromSurface: true,
        clip: { x: 0, y: 0, width, height, scale: 1 },
      });
      const png = Buffer.from(result.data, "base64");
      if (png.subarray(0, 8).toString("hex") !== "89504e470d0a1a0a" || png.readUInt32BE(16) !== width || png.readUInt32BE(20) !== height) {
        throw new Error(`${filename}: Chrome returned an incomplete PNG`);
      }
      await writeFile(join(outputDir, filename), png);
      manifest.push({ file: filename, page, viewport_width: width, full_page_height: height, bytes: png.length });
      console.log(`${filename} (${width} × ${height})`);
    };

    await send("Page.enable");
    await send("Runtime.enable");
    await setViewport(1440, 900, false);
    await navigate("/_/login/", "Sign in.", "document.querySelector('form #password')");
    const login = await evaluate(`fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'demo@example.test', password: ${JSON.stringify(process.env.PAG_DEMO_PASSWORD)} }) }).then((response) => response.status)`);
    if (login !== 200) throw new Error(`Demo login failed (${login}); check the temporary password`);
    await mkdir(outputDir, { recursive: true });

    for (const page of pages) {
      await navigate(page.path, page.heading, page.ready);
      if (page.name === "models") {
        await evaluate("document.querySelector('details.grant-editor').open = true");
        await sleep(200);
      }
      await screenshot(`demo-page-${page.name}.png`, 1440, page.name);
      if (page.name === "requests") {
        const href = await evaluate("document.querySelector('main a[href*=request_id]')?.getAttribute('href')");
        if (!href) throw new Error("Populated requests page has no request detail link");
        const detailPath = new URL(href, baseURL + page.path);
        if (detailPath.origin !== baseURL) throw new Error("Request detail link left the demo origin");
        await navigate(detailPath.pathname + detailPath.search, "Requests", "document.querySelector('#request-detail-heading') && document.querySelector('[aria-label=\"Request detail\"] .facts')");
        await screenshot("demo-page-request-detail.png", 1440, "request-detail");
      }
    }

    await setViewport(390, 844, true);
    for (const page of pages.filter((item) => mobileNames.has(item.name))) {
      await navigate(page.path, page.heading, page.ready);
      await screenshot(`demo-mobile-${page.name}.png`, 390, page.name);
    }
    await writeFile(join(outputDir, "demo-screenshots.json"), `${JSON.stringify({ source: "Disposable synthetic demo", captured_at: new Date().toISOString(), screenshots: manifest }, null, 2)}\n`);
  } finally {
    socket?.close();
    chrome.kill("SIGTERM");
    await sleep(300);
    await rm(profile, { recursive: true, force: true });
  }
}

try { await capture(options(process.argv.slice(2))); }
catch (error) { console.error(error.message); process.exitCode = 1; }
