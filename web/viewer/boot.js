// Starts the local viewer's module. An earlier inline script (inline.mjs)
// sets globalThis.cernlyBuild: the module deflated and in base64, the scan
// worker's and the WASI shim's text (plugins run in Workers the page
// starts, since WebKit starts none from inside a Worker), the bundled plugins, the extra trusted
// keys (test builds only) and the module's SHA-256. The module runs in a
// Worker from a blob: URL; nothing is fetched. globalThis.cernly then offers
// view(text), compare(a, b), redact(text) and scan(source, name), each a promise, and
// "cernly-ready" fires. If the module cannot start, cernly.failed says why and
// every call rejects with it.
(function () {
  "use strict";
  const b = globalThis.cernlyBuild;
  const bytes = (b64) => {
    const s = atob(b64);
    const out = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out;
  };
  const inflate = (b64) => new Response(new Blob([bytes(b64)]).stream().pipeThrough(new DecompressionStream("deflate-raw"))).arrayBuffer();
  let seq = 0;
  const waiting = new Map();
  let worker;
  let src;
  const ready = () => dispatchEvent(new Event("cernly-ready"));
  const fail = (e) => {
    const m = "the viewer's module did not start: " + ((e && e.message) || e);
    const no = () => Promise.reject(new Error(m));
    globalThis.cernly = { failed: m, view: no, compare: no, redact: no, scan: no, hash: b && b.hash };
    for (const w of waiting.values()) w.reject(new Error(m));
    waiting.clear();
    ready();
  };
  const call = (op, args) =>
    new Promise((resolve, reject) => {
      const id = ++seq;
      waiting.set(id, { resolve, reject });
      worker.postMessage({ type: "call", id, op, args });
    });
  // The folder a scan reads: the worker asks, the source the page picked
  // answers. One scan at a time.
  const answer = async (m) => {
    try {
      if (!src) throw new Error("no folder");
      const value = await src[m.method](...m.args);
      worker.postMessage({ type: "fs-result", id: m.id, ok: true, value }, value instanceof Uint8Array ? [value.buffer] : []);
    } catch (e) {
      worker.postMessage({ type: "fs-result", id: m.id, ok: false, error: String((e && e.message) || e) });
    }
  };
  // One plugin run in a Worker of its own, ended at its time limit.
  let wasiURL;
  const runPlugin = (m) => {
    wasiURL ??= URL.createObjectURL(new Blob([b.wasi], { type: "text/javascript" }));
    const w = new Worker(wasiURL);
    const done = (value) => {
      clearTimeout(timer);
      w.terminate();
      const moved = value && value.stdout ? [value.stdout.buffer, value.stderr.buffer] : [];
      worker.postMessage({ type: "plugin-result", id: m.id, value }, moved);
    };
    const timer = setTimeout(() => done({ timedOut: true }), m.opts.timeoutMs);
    w.onmessage = (e) => done(e.data);
    w.onerror = (e) => {
      e.preventDefault();
      done({ error: e.message || "the plugin worker failed" });
    };
    w.postMessage({ module: m.module, stdin: m.stdin, opts: m.opts }, [m.module.buffer, m.stdin.buffer]);
  };
  try {
    if (!b) throw new Error("the page has no module");
    const url = URL.createObjectURL(new Blob([b.worker], { type: "text/javascript" }));
    worker = new Worker(url);
    worker.onerror = (e) => {
      e.preventDefault();
      fail(e.message || "the worker failed");
    };
    worker.onmessage = (e) => {
      const m = e.data;
      if (m.type === "fs") return void answer(m);
      if (m.type === "plugin") return runPlugin(m);
      if (m.type === "ready") {
        globalThis.cernly = {
          view: (text) => call("view", [text]),
          compare: (a, c) => call("compare", [a, c]),
          redact: (text) => call("redact", [text]),
          scan: (source, name) => {
            src = source;
            return call("scan", [name]).finally(() => { src = undefined; });
          },
          maxBytes: m.maxBytes,
          maxFiles: m.maxFiles,
          version: m.version,
          hash: b.hash,
          plugins: b.plugins.map((p) => JSON.parse(p.bundle).manifest.name),
        };
        return ready();
      }
      if (m.type === "failed") return fail(m.error);
      if (m.type === "result") {
        const w = waiting.get(m.id);
        waiting.delete(m.id);
        if (w) m.ok ? w.resolve(m.value) : w.reject(new Error(m.error));
      }
    };
    Promise.all([inflate(b.wasm), ...b.plugins.map((p) => inflate(p.module))])
      .then(([wasm, ...modules]) => {
        const plugins = b.plugins.map((p, i) => ({ bundle: p.bundle, module: new Uint8Array(modules[i]) }));
        worker.postMessage({ type: "init", wasm, plugins, trust: b.trust }, [wasm, ...plugins.map((p) => p.module.buffer)]);
      })
      .catch(fail);
  } catch (e) {
    fail(e);
  }
})();
