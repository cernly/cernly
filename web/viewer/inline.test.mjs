import assert from "node:assert/strict";
import { test } from "node:test";
import { runInNewContext } from "node:vm";
import { assertOffline, csp, inlinePage, loader, scriptBodies, sha256 } from "./inline.mjs";

const html = `<!doctype html><html><head><meta charset="utf-8"><title>t</title>
<script type="module" crossorigin src="./assets/index-abc.js"></script>
<link rel="stylesheet" crossorigin href="./assets/index-abc.css">
</head><body><div id="root"></div></body></html>`;
const files = {
  "assets/index-abc.js": 'console.log("hi </script> there");',
  "assets/index-abc.css": '@font-face{src:url(data:font/woff2;base64,AAAA)}body{margin:0}',
};
const build = () => inlinePage(html, {
  read: (p) => files[p.replace(/^\.\//, "")],
  wasm: Uint8Array.from([0, 97, 115, 109]),
  wasmExec: "globalThis.Go = class {};",
});

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
  for (const b of bodies) assert.ok(policy.includes(`'${sha256(b)}'`), "missing hash for a script");
  assert.match(policy, /'wasm-unsafe-eval'/);
  assert.equal(policy, csp(out));
});

test("no request is allowed and nothing references a file", () => {
  const out = build();
  assert.ok(!/connect-src/.test(out));
  assert.match(out, /default-src 'none'/);
  assert.ok(!/(?:src|href)\s*=\s*["']?(?!data:)/i.test(out.replace(/<meta[^>]*>/, "")));
  assertOffline(out);
});

test("</script inside a script does not end it", () => {
  const out = build();
  assert.ok(scriptBodies(out).some((b) => b.includes("<\\/script>")));
});

test("replacement patterns in a script stay text", () => {
  const out = inlinePage(html, { read: (p) => (p.endsWith("js") ? "x='$&$`$\\'';" : files["assets/index-abc.css"]), wasm: Uint8Array.from([0]), wasmExec: "" });
  assert.ok(out.length < 3000);
});

test("a page that still references a file is refused", () => {
  assert.throws(() => assertOffline('<img src="a.png">'), /still references/);
  assert.throws(() => assertOffline('<style>a{background:url(x.png)}</style>'), /still references/);
});

// Runs the loader with a WebAssembly that refuses the module, or bytes atob
// cannot read, and returns what the page would see.
async function start(b64, instantiate) {
  const events = [];
  const ctx = {
    atob, Uint8Array, JSON,
    Go: class { importObject = {}; run() {} },
    WebAssembly: { instantiate },
    Event: class { constructor(type) { this.type = type; } },
    dispatchEvent: (e) => events.push(e.type),
  };
  runInNewContext(loader(b64), ctx);
  await new Promise((r) => setTimeout(r, 0));
  return { cernly: ctx.cernly, events };
}

test("a module that does not start still answers, with the error", async () => {
  for (const [b64, instantiate] of [
    ["AGFzbQ==", () => Promise.reject(new Error("CompileError: truncated"))],
    ["%%%", () => Promise.resolve({})],
  ]) {
    const { cernly, events } = await start(b64, instantiate);
    assert.deepEqual(events, ["cernly-ready"]);
    assert.match(cernly.failed, /did not start/);
    assert.match(JSON.parse(cernly.view("{}")).error, /did not start/);
    assert.match(JSON.parse(cernly.compare("{}", "{}")).error, /did not start/);
  }
});
