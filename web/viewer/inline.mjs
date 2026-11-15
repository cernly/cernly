// Folds the Vite build, the WebAssembly module (deflated by
// internal/cmd/deflate, so its bytes depend on the pinned Go only), the scan
// worker, the WASI shim and the bundled plugins into one offline HTML file,
// and writes its Content-Security-Policy. Node built-ins only.
//
//   node inline.mjs --dist <vite dist dir> --wasm <cernly-viewer.wasm.deflate> --hash <sha256 of the module>
//     --wasm-exec <wasm_exec.js> --out <out.html> [--plugin <bundle.json>=<module.wasm.deflate>]... [--trust <key.pub>]...
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { parseArgs } from "node:util";

const SCRIPT = /<script\b([^>]*)>([\s\S]*?)<\/script>/gi;

// The bodies of the inline scripts, in document order.
export function scriptBodies(html) {
  return [...html.matchAll(SCRIPT)].filter((m) => !/\ssrc\s*=/i.test(m[1])).map((m) => m[2]);
}

export const sha256 = (text) => "sha256-" + createHash("sha256").update(text, "utf8").digest("base64");

// The policy lists each inline script by hash and has no connect-src, so
// default-src 'none' forbids every request the page could make. 'wasm-unsafe-eval'
// is what WebAssembly.instantiate needs.
export function csp(html) {
  const hashes = scriptBodies(html).map((b) => `'${sha256(b)}'`);
  return [
    "default-src 'none'",
    `script-src ${[...new Set(hashes)].join(" ")} 'wasm-unsafe-eval'`,
    "style-src 'unsafe-inline'",
    "img-src data:",
    // The module runs in a Worker made from a blob: URL; so does each plugin.
    "worker-src blob:",
    "font-src data:",
    "base-uri 'none'",
    "form-action 'none'",
  ].join("; ");
}

// Throws if the page would load anything by URL or file name.
export function assertOffline(html) {
  const refs = [...html.matchAll(/<[a-z][^>]*\s(?:src|href|action|poster|srcset)\s*=\s*["']?([^"'\s>]*)/gi)].map((m) => m[1]).filter((u) => !/^data:/i.test(u));
  if (refs.length) throw new Error(`the page still references ${refs.join(", ")}`);
  const styles = html.replace(SCRIPT, "");
  const urls = [...styles.matchAll(/url\(\s*(?!["']?(?:data:|#))["']?([^)"']*)/gi)].map((m) => m[1]);
  if (urls.length) throw new Error(`a style still references ${urls.join(", ")}`);
  if (/@import/i.test(styles)) throw new Error("a style still imports another file");
  if (/connect-src/i.test(csp(html))) throw new Error("connect-src in the policy");
}

function safeScript(body) {
  // "</script" ends a script early: write it so the string is unchanged.
  const out = body.replaceAll(/<\/script/gi, "<\\/script");
  if (/<!--[\s\S]*<script/i.test(out)) throw new Error("a script contains <!-- before <script, which changes how the parser reads it");
  return out;
}

const here = dirname(fileURLToPath(import.meta.url));
const own = (name) => readFileSync(join(here, name), "utf8");

// The script that sets globalThis.cernlyBuild for boot.js. b64 is the
// deflated module in base64; plugins are {bundle (JSON text), module (deflated
// module, base64)}; trust lists extra PEM public keys (test builds only).
export function buildScript({ b64, hash, wasmExec, plugins = [], trust = [] }) {
  const build = { wasm: b64, hash, worker: wasmExec + "\n" + own("worker.js"), wasi: own("wasi.js"), plugins, trust };
  return `globalThis.cernlyBuild=${JSON.stringify(build)};`;
}

// html is Vite's index.html; read(path) returns a file of the build as text.
// build is what buildScript takes.
export function inlinePage(html, { read, build }) {
  let page = html;
  let app = "";
  page = page.replace(/<script\b([^>]*)\ssrc\s*=\s*["']([^"']+)["']([^>]*)>\s*<\/script>/gi, (_, _a, src) => {
    app += safeScript(read(src)) + "\n";
    return "";
  });
  page = page.replace(/<link\b[^>]*\brel\s*=\s*["']stylesheet["'][^>]*>/gi, (tag) => {
    const css = read(/href\s*=\s*["']([^"']+)["']/i.exec(tag)[1]);
    if (/<\/style/i.test(css)) throw new Error("a stylesheet contains </style");
    return `<style>${css}</style>`;
  });
  page = page.replace(/<link\b[^>]*\brel\s*=\s*["'](?:modulepreload|icon|preload)["'][^>]*>/gi, "");
  if (!app) throw new Error("no application script in the page");
  const classic = `<script>${safeScript(buildScript(build))}</script>\n<script>${safeScript(own("boot.js"))}</script>`;
  page = page.replace(/<\/head>/i, () => `${classic}\n</head>`);
  page = page.replace(/<\/body>/i, () => `<script type="module">${app}</script>\n</body>`);
  const policy = csp(page);
  page = page.replace(/<head[^>]*>/i, (open) => `${open}\n<meta http-equiv="Content-Security-Policy" content="${policy}">`);
  assertOffline(page);
  return page;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const { values: v } = parseArgs({
    options: {
      dist: { type: "string" }, wasm: { type: "string" }, hash: { type: "string" }, "wasm-exec": { type: "string" }, out: { type: "string" },
      plugin: { type: "string", multiple: true, default: [] }, trust: { type: "string", multiple: true, default: [] },
    },
  });
  if (!v.dist || !v.wasm || !v.hash || !v["wasm-exec"] || !v.out) {
    console.error("usage: node inline.mjs --dist <dir> --wasm <module.deflate> --hash <sha256> --wasm-exec <wasm_exec.js> --out <out.html> [--plugin <bundle.json>=<module.deflate>]... [--trust <key.pub>]...");
    process.exit(2);
  }
  const read = (p) => readFileSync(join(v.dist, p.replace(/^\.\//, "")), "utf8");
  const b64 = (f) => readFileSync(f).toString("base64");
  const plugins = v.plugin.map((pair) => {
    const [bundle, module] = pair.split("=");
    return { bundle: readFileSync(bundle, "utf8").trim(), module: b64(module) };
  });
  const build = { b64: b64(v.wasm), hash: v.hash, wasmExec: readFileSync(v["wasm-exec"], "utf8"), plugins, trust: v.trust.map((f) => readFileSync(f, "utf8")) };
  writeFileSync(v.out, inlinePage(read("index.html"), { read, build }));
}
