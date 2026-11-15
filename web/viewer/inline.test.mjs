import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { runInNewContext } from "node:vm";
import { assertOffline, buildScript, csp, inlinePage, scriptBodies, sha256 } from "./inline.mjs";

const html = `<!doctype html><html><head><meta charset="utf-8"><title>t</title>
<script type="module" crossorigin src="./assets/index-abc.js"></script>
<link rel="stylesheet" crossorigin href="./assets/index-abc.css">
</head><body><div id="root"></div></body></html>`;
const files = {
  "assets/index-abc.js": 'console.log("hi </script> there");',
  "assets/index-abc.css": '@font-face{src:url(data:font/woff2;base64,AAAA)}body{margin:0}',
};
const b = { b64: "AwA=", hash: "ab", wasmExec: "globalThis.Go = class {};" };
const build = () => inlinePage(html, { read: (p) => files[p.replace(/^\.\//, "")], build: b });

test("one policy, the first child of head", () => {
  const out = build();
  assert.equal(out.match(/http-equiv="Content-Security-Policy"/g).length, 1);
  assert.match(out, /<head>\s*<meta http-equiv="Content-Security-Policy"/);
});

test("every inline script is listed by hash", () => {
  const out = build();
  const policy = /content="([^"]+)"/.exec(out)[1];
  const bodies = scriptBodies(out);
  assert.equal(bodies.length, 3);
  for (const s of bodies) assert.ok(policy.includes(`'${sha256(s)}'`), "missing hash for a script");
  assert.match(policy, /'wasm-unsafe-eval'/);
  assert.match(policy, /worker-src blob:/);
  assert.equal(policy, csp(out));
});

// The page with each script's text left out, its tags kept: a script's text
// may say "src =" without referencing a file.
function withoutScriptText(s) {
  const lower = s.toLowerCase();
  let out = "";
  let at = 0;
  for (;;) {
    const open = lower.indexOf("<script", at);
    if (open < 0) return out + s.slice(at);
    const body = lower.indexOf(">", open) + 1;
    const close = lower.indexOf("</script", body);
    out += s.slice(at, body);
    at = close < 0 ? s.length : close;
  }
}

test("no request is allowed and nothing references a file", () => {
  const out = build();
  assert.ok(!/connect-src/.test(out));
  assert.match(out, /default-src 'none'/);
  const tags = withoutScriptText(out.replace(/<meta[^>]*>/, ""));
  assert.ok(!/(?:src|href)\s*=\s*["']?(?!data:)/i.test(tags));
  assertOffline(out);
});

test("</script inside a script does not end it", () => {
  const out = build();
  assert.ok(scriptBodies(out).some((s) => s.includes("<\\/script>")));
});

test("replacement patterns in a script stay text", () => {
  const out = inlinePage(html, { read: (p) => (p.endsWith("js") ? "x='$&$`$\\'';" : files["assets/index-abc.css"]), build: { ...b, wasmExec: "" } });
  assert.ok(out.includes("x='$&$`$\\'';"));
});

test("a page that still references a file is refused", () => {
  assert.throws(() => assertOffline('<img src="a.png">'), /still references/);
  assert.throws(() => assertOffline('<style>a{background:url(x.png)}</style>'), /still references/);
});

// Runs boot.js with a fake Worker and returns what the page would see; the
// worker answers as behave says.
async function boot(behave) {
  const events = [];
  const sent = [];
  let made = 0;
  class FakeWorker {
    // The first Worker is the scan worker; later ones are plugin runs, which never answer here.
    constructor() { if (made++ === 0) setTimeout(() => behave(this), 0); }
    postMessage(m) { sent[sent.length] = m; if (this.onsend) this.onsend(m); }
    terminate() { this.ended = true; }
  }
  const ctx = {
    atob, Uint8Array, ArrayBuffer, JSON, Promise, Error, setTimeout, clearTimeout, Blob, Response, DecompressionStream,
    URL: { createObjectURL: () => "blob:x" },
    Worker: FakeWorker,
    Event: class { constructor(type) { this.type = type; } },
    dispatchEvent: (e) => { events[events.length] = e.type; },
  };
  ctx.globalThis = ctx;
  runInNewContext(buildScript(b), ctx);
  runInNewContext(readFileSync(new URL("./boot.js", import.meta.url), "utf8"), ctx);
  await new Promise((r) => setTimeout(r, 20));
  return { ctx, events, sent };
}

test("a module that does not start still answers, with the error", async () => {
  const { ctx, events } = await boot((w) => w.onmessage({ data: { type: "failed", error: "CompileError: truncated" } }));
  assert.deepEqual(events, ["cernly-ready"]);
  assert.match(ctx.cernly.failed, /did not start: CompileError/);
  await assert.rejects(ctx.cernly.view("{}"), /did not start/);
  await assert.rejects(ctx.cernly.scan({}, "x"), /did not start/);
});

test("a worker that cannot start is an error, not a wait", async () => {
  const { ctx, events } = await boot((w) => w.onerror({ message: "refused", preventDefault() {} }));
  assert.deepEqual(events, ["cernly-ready"]);
  assert.match(ctx.cernly.failed, /refused/);
});

test("calls go to the worker and folder reads are answered by the page", async () => {
  const { ctx, events, sent } = await boot((w) => {
    w.onsend = (m) => {
      if (m.type === "call" && m.op === "view") w.onmessage({ data: { type: "result", id: m.id, ok: true, value: { map: 1 } } });
      if (m.type === "call" && m.op === "scan") w.onmessage({ data: { type: "fs", id: 7, method: "stat", args: [".git"] } });
      if (m.type === "fs-result") w.onmessage({ data: { type: "result", id: 2, ok: true, value: { seen: m.value } } });
    };
    w.onmessage({ data: { type: "ready", maxBytes: 5, maxFiles: 6 } });
  });
  assert.deepEqual(events, ["cernly-ready"]);
  assert.equal(ctx.cernly.maxFiles, 6);
  assert.equal(ctx.cernly.hash, "ab");
  assert.deepEqual(await ctx.cernly.view("{}"), { map: 1 });
  const folder = { stat: async (p) => ({ dir: p === ".git", size: 0, mtime: 0 }) };
  assert.deepEqual(await ctx.cernly.scan(folder, "acme"), { seen: { dir: true, size: 0, mtime: 0 } });
  const init = sent.find((m) => m.type === "init");
  assert.ok(init && init.wasm);
});

test("a plugin run asked by the worker runs in a Worker the page starts", async () => {
  const { sent } = await boot((w) => {
    w.onmessage({ data: { type: "ready" } });
    w.onmessage({ data: { type: "plugin", id: 3, module: new Uint8Array(1), stdin: new Uint8Array(1), opts: { timeoutMs: 5 } } });
  });
  await new Promise((r) => setTimeout(r, 30));
  const res = sent.find((m) => m.type === "plugin-result");
  assert.deepEqual(JSON.parse(JSON.stringify(res)), { type: "plugin-result", id: 3, value: { timedOut: true } });
});
