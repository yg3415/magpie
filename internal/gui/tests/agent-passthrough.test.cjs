// Run with Node's test runner and Playwright on the module path; see README.md.
// Subscription passthrough (docs/claude-code-passthrough.md): Claude Code's
// row menu offers "Use subscription passthrough", which asks first in the
// app's own dialog and posts agents/passthrough-on; the row then says
// "Subscription passthrough" on its name's line, its menu offers to stop it
// (asked first), and picking one of magpie's models from its picker asks
// before leaving passthrough — Cancel posts nothing. An agent that can't
// pass through (Codex) offers none of it. In English and Chinese; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "opus", label: "Opus" }, { value: "magpie/relay/m1", label: "Relay m1", ref: "relay/m1" }];
const fresh = () => ({
  agents: [
    { id: "claude", name: "Claude Code", icon: "generic", path: "/fixture/claude", canPassthrough: true, fields: [{ key: "model", label: "model", value: "opus", options }] },
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", fields: [{ key: "model", label: "model", value: "opus", options }] },
  ],
  profiles: [],
});

function server(lang, posts) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const st = () => json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return st();
    const m = url.pathname.match(/^\/api\/agents\/passthrough-(on|off)\/(\w+)$/);
    if (m) {
      posts.push(url.pathname);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents.find((x) => x.id === m[2]);
      a.passthrough = a.wired = m[1] === "on";
      if (a.passthrough) a.launch = 'PATH="/home/me/.local/share/magpie/bin:$PATH" claude';
      else delete a.launch;
      return st();
    }
    if (url.pathname === "/api/set") { posts.push(url.pathname); return st(); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { use: "Use subscription passthrough", stop: "Stop subscription passthrough", ask: "Use subscription passthrough for Claude Code?", leave: "Stop subscription passthrough for Claude Code?", tag: "Subscription passthrough", cancel: "Cancel", go: "Continue" },
  zh: { use: "使用订阅直通", stop: "停止订阅直通", ask: "为 Claude Code 使用订阅直通？", leave: "停止 Claude Code 的订阅直通？", tag: "订阅直通", cancel: "取消", go: "继续" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: subscription passthrough on Claude Code's row`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(row("claude")).waitFor();
      const item = (name) => page.locator(".pop.row-menu .rm-item", { hasText: name });
      const openMenu = async (id) => {
        await page.locator(`${row(id)} .ag-handle`).click();
        await page.locator(".pop.row-menu").waitFor();
      };
      const closeMenu = async () => { await page.keyboard.press("Escape"); await page.locator(".pop.row-menu").waitFor({ state: "detached" }); };

      // Codex can't pass through
      await openMenu("codex");
      assert.equal(await item(w.use).count(), 0);
      await closeMenu();

      // asked first; Cancel posts nothing
      await openMenu("claude");
      assert.equal(await item(w.use).count(), 1);
      await item(w.use).click();
      const ask = page.locator(".editor.disconnect-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask);
      await ask.locator(".bar button", { hasText: w.cancel }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, []);

      // turned on: the row says so, and its launcher is a click away
      await openMenu("claude");
      await item(w.use).click();
      await ask.waitFor();
      await ask.locator(".bar button.primary").click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, ["/api/agents/passthrough-on/claude"]);
      await page.locator(`${row("claude")} .ag-mode`, { hasText: w.tag }).waitFor();
      assert.equal(await page.locator(`${row("claude")} .launch`).count(), 1);

      // one of magpie's models asks before leaving passthrough; Cancel keeps it
      await page.locator(`${row("claude")} .field[data-key="model"]`).click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.locator("#list li", { hasText: "Relay m1" }).click();
      const leave = page.locator(".editor.disconnect-ask");
      await leave.waitFor();
      assert.equal((await leave.locator(".ehead b").textContent()).trim(), w.leave);
      await leave.locator(".bar button", { hasText: w.cancel }).click();
      await leave.waitFor({ state: "detached" });
      assert.deepEqual(posts, ["/api/agents/passthrough-on/claude"]);
      // continued, the pick goes through
      await page.locator(`${row("claude")} .field[data-key="model"]`).click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      await page.locator("#list li", { hasText: "Relay m1" }).click();
      await leave.waitFor();
      await leave.locator(".bar button", { hasText: w.go }).click();
      await page.waitForFunction(() => true);
      await page.waitForTimeout(200);
      assert.deepEqual(posts, ["/api/agents/passthrough-on/claude", "/api/set"]);

      // stopping it from the menu asks first too
      await openMenu("claude");
      await item(w.stop).click();
      await leave.waitFor();
      await leave.locator(".bar button", { hasText: w.go }).click();
      await page.locator(`${row("claude")} .ag-mode`).waitFor({ state: "detached" });
      assert.deepEqual(posts.at(-1), "/api/agents/passthrough-off/claude");

      assert.deepEqual(dialogs, [], "no native dialog");
      assert.deepEqual(errors, []);
    });
  }
}
