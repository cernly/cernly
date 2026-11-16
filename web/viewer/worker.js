// The scan worker: runs the WebAssembly module (cmd/cernly-viewer) off the
// page's thread. The page builds it from a blob: URL whose text is Go's
// wasm_exec.js followed by this file, so it needs no request. Messages:
//
//   page -> worker  {type: "init", wasm, plugins, trust}
//                   {type: "call", id, op: "view"|"compare"|"scan", args}
//                   {type: "fs-result", id, ok, value | error}
//                   {type: "plugin-result", id, value}
//   worker -> page  {type: "ready", maxBytes, maxFiles} | {type: "failed", error}
//                   {type: "result", id, ok, value | error}
//                   {type: "fs", id, method, args}
//                   {type: "plugin", id, module, stdin, opts}
//
// A scan reads the picked folder through the page (only the page holds the
// directory handle or the dropped entries): stat, readDir and read are
// forwarded and answered there. Each outside plugin run is handed to the page,
// which runs it in a Worker of its own from the WASI shim's text
// (web/viewer/wasi.js).
(function () {
  "use strict";
  let plugins = [];
  let trust = [];
  let fsSeq = 0;
  const fsWaiting = new Map();

  const fsCall = (method, ...args) =>
    new Promise((resolve, reject) => {
      const id = ++fsSeq;
      fsWaiting.set(id, { resolve, reject });
      postMessage({ type: "fs", id, method, args });
    });
  const source = {
    stat: (path) => fsCall("stat", path),
    readDir: (path) => fsCall("readDir", path),
    read: (path, off, len) => fsCall("read", path, off, len),
  };

  // A plugin runs in a Worker the page starts (WebKit starts no Worker from
  // inside one); the page ends it at its time limit.
  let runSeq = 0;
  const runs = new Map();
  globalThis.cernlyRunPlugin = (module, stdin, opts) =>
    new Promise((resolve) => {
      const id = ++runSeq;
      runs.set(id, resolve);
      postMessage({ type: "plugin", id, module, stdin, opts }, [module.buffer, stdin.buffer]);
    });

  const ops = {
    view: (facts) => cernly.view(facts),
    compare: (a, b) => cernly.compare(a, b),
    scan: (name) => cernly.scan(source, name, plugins, trust),
  };

  onmessage = async (e) => {
    const m = e.data;
    if (m.type === "fs-result") {
      const w = fsWaiting.get(m.id);
      fsWaiting.delete(m.id);
      if (w) m.ok ? w.resolve(m.value) : w.reject(new Error(m.error));
      return;
    }
    if (m.type === "plugin-result") {
      const r = runs.get(m.id);
      runs.delete(m.id);
      if (r) r(m.value);
      return;
    }
    if (m.type === "init") {
      plugins = m.plugins || [];
      trust = m.trust || [];
      try {
        addEventListener("cernly-ready", () => postMessage({ type: "ready", maxBytes: cernly.maxBytes, maxFiles: cernly.maxFiles }), { once: true });
        const go = new Go();
        const { instance } = await WebAssembly.instantiate(m.wasm, go.importObject);
        go.run(instance).then(() => postMessage({ type: "failed", error: "the module stopped" }));
      } catch (err) {
        postMessage({ type: "failed", error: String((err && err.message) || err) });
      }
      return;
    }
    if (m.type === "call") {
      try {
        // view and compare answer JSON text, an {error} object on failure;
        // scan is a promise of JSON text that rejects on failure.
        const text = await ops[m.op](...m.args);
        const value = JSON.parse(text);
        if (value && typeof value === "object" && typeof value.error === "string") throw new Error(value.error);
        postMessage({ type: "result", id: m.id, ok: true, value });
      } catch (err) {
        postMessage({ type: "result", id: m.id, ok: false, error: String((err && err.message) || err) });
      }
    }
  };
})();
