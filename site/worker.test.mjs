// node --test site/: the update feed's notes follow the app's language
// (freecss on Discord). GitHub and the edge cache are stood in for.
import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "./worker.js";

const EN = "### Features\n\n- One thing (#1)\n\n### Install\n\nDownload it.";
const ZH = "### 新功能\n\n- 一件事 (#1)";
const BODIES = {
  "v0.1.3": `${EN}\n\n<!-- lang:zh -->\n\n${ZH}\n`,
  "v0.1.2": "### Fixes\n\n- Older, English alone",
  "v0.1.1": "- first",
};

const store = new Map();
globalThis.caches = {
  default: {
    match: async (req) => (store.has(req.url) ? new Response(store.get(req.url)) : undefined),
    put: async (req, res) => void store.set(req.url, await res.text()),
  },
};
globalThis.fetch = async (u) => {
  u = String(u);
  const gh = (tag) => ({ tag_name: tag, body: BODIES[tag], html_url: "https://github.com/r/" + tag, published_at: "2026-10-01T00:00:00Z", draft: false, prerelease: false, assets: [{ name: "magpie-linux-amd64", size: 1, browser_download_url: "https://dl/x" }] });
  if (u.endsWith("/releases/latest")) return Response.json(gh("v0.1.3"));
  if (u.includes("/releases?per_page=")) return Response.json(Object.keys(BODIES).map(gh));
  if (u.endsWith("/SHA256SUMS")) return new Response("abc  magpie-linux-amd64\n");
  return new Response("not found", { status: 404 });
};

const ctx = { waitUntil: (p) => p };
const get = async (path) => {
  const res = await worker.fetch(new Request("https://usemagpie.ai" + path), {}, ctx);
  assert.equal(res.status, 200, path);
  return { body: await res.json(), cc: res.headers.get("Cache-Control") };
};

test("latest: the English alone without lang, the Chinese with zh", async () => {
  for (const q of ["", "?lang=en", "?lang=fr"]) {
    const { body, cc } = await get("/api/latest" + q);
    assert.equal(body.version, "0.1.3");
    assert.equal(body.notes, EN, q);
    assert.match(cc, /max-age=/);
  }
  for (const q of ["?lang=zh", "?lang=zh-CN", "?lang=zh_Hans"]) {
    assert.equal((await get("/api/latest" + q)).body.notes, ZH, q);
  }
  // asked in Chinese first, the edge's copy still has both
  assert.equal((await get("/api/latest")).body.notes, EN);
});

test("notes: each release in the language, English where it has no Chinese", async () => {
  const zh = (await get("/api/notes?after=0.1.1&upto=0.1.3&lang=zh")).body.releases;
  assert.deepEqual(zh.map((r) => [r.version, r.notes]), [["0.1.3", ZH], ["0.1.2", BODIES["v0.1.2"]]]);
  const en = (await get("/api/notes?after=0.1.1&upto=0.1.3")).body.releases;
  assert.deepEqual(en.map((r) => [r.version, r.notes]), [["0.1.3", EN], ["0.1.2", BODIES["v0.1.2"]]]);
  const again = (await get("/api/notes?after=0.1.1&upto=0.1.3&lang=zh")).body.releases;
  assert.equal(again[0].notes, ZH);
});

test("i18n: every string the home page marks has its Chinese, and no other", async () => {
  const { readFile } = await import("node:fs/promises");
  const { LANGS } = await import("./i18n.js");
  const html = await readFile(new URL("./public/index.html", import.meta.url), "utf8");
  const keys = new Set();
  for (const [, k] of html.matchAll(/data-i18n="([^"]+)"/g)) keys.add(k);
  for (const [, pairs] of html.matchAll(/data-i18n-attr="([^"]+)"/g))
    for (const p of pairs.split(",")) keys.add(p.split(":")[1]);
  for (const [lang, { dict }] of Object.entries(LANGS)) {
    assert.deepEqual([...keys].filter((k) => !(k in dict)), [], `${lang} lacks`);
    assert.deepEqual(Object.keys(dict).filter((k) => !keys.has(k)), [], `${lang} has unused`);
  }
});

test("home: / sends a browser that prefers Chinese or Japanese to its page, until a language is picked", async () => {
  const env = { ASSETS: { fetch: async () => new Response("<html></html>", { headers: { "Content-Type": "text/plain" } }) } };
  const home = (headers) => worker.fetch(new Request("https://usemagpie.ai/", { headers }), env, ctx);
  const to = async (headers) => {
    const res = await home(headers);
    assert.match(res.headers.get("Vary") || "", /Accept-Language/);
    return res.status === 302 ? res.headers.get("Location") : null;
  };
  assert.equal(await to({}), null);
  assert.equal(await to({ "Accept-Language": "en-US,en;q=0.9,zh;q=0.8" }), null);
  assert.equal(await to({ "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "zh-TW" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "fr-FR,zh;q=0.5,en;q=0.4" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "en;q=0.2,zh;q=0.8" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "zh-CN", Cookie: "a=b; lang=en" }), null);
  assert.equal(await to({ Cookie: "lang=zh" }), "/zh/");
  assert.equal(await to({ "Accept-Language": "ja-JP,ja;q=0.9,en;q=0.8" }), "/ja/");
  assert.equal(await to({ "Accept-Language": "ja", Cookie: "lang=zh" }), "/zh/");
  assert.equal(await to({ Cookie: "lang=ja" }), "/ja/");
  const res = await worker.fetch(new Request("https://usemagpie.ai/zh"), env, ctx);
  assert.equal(res.status, 301);
  assert.equal(new URL(res.headers.get("Location")).pathname, "/zh/");
});

test("docs: the bare docs paths open each language's guide", async () => {
  const env = { ASSETS: { fetch: async () => new Response("asset") } };
  for (const [from, to] of [["/docs", "/docs/start"], ["/docs/zh/", "/docs/zh/start"], ["/docs/ja", "/docs/ja/start"]]) {
    const res = await worker.fetch(new Request("https://usemagpie.ai" + from), env, ctx);
    assert.equal(new URL(res.headers.get("Location"), "https://usemagpie.ai").pathname, to, from);
  }
});
