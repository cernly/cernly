// Folds the Vite build and the WebAssembly module into one offline HTML file
// and writes its Content-Security-Policy. Node built-ins only.
//
//   node inline.mjs <vite dist dir> <cernly-viewer.wasm> <wasm_exec.js> <out.html>
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

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

// Starts the module. If it cannot (a truncated file, a browser that refuses
// it), cernly answers every call with that error and "cernly-ready" still
// fires, so the page shows it instead of waiting.
export const loader = (b64) =>
  `const fail=e=>{const m="the viewer's module did not start: "+(e&&e.message||e),err=()=>JSON.stringify({error:m});` +
  `globalThis.cernly={failed:m,view:err,compare:err};dispatchEvent(new Event("cernly-ready"))};` +
  `try{const b=atob("${b64}"),bytes=new Uint8Array(b.length);for(let i=0;i<b.length;i++)bytes[i]=b.charCodeAt(i);` +
  `const go=new Go();WebAssembly.instantiate(bytes,go.importObject).then(r=>go.run(r.instance)).catch(fail)}catch(e){fail(e)}`;

// html is Vite's index.html; read(path) returns a file of the build as text.
export function inlinePage(html, { read, wasm, wasmExec }) {
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
  const classic = `<script>${safeScript(wasmExec)}</script>\n<script>${loader(Buffer.from(wasm).toString("base64"))}</script>`;
  page = page.replace(/<\/head>/i, () => `${classic}\n</head>`);
  page = page.replace(/<\/body>/i, () => `<script type="module">${app}</script>\n</body>`);
  const policy = csp(page);
  page = page.replace(/<head[^>]*>/i, (open) => `${open}\n<meta http-equiv="Content-Security-Policy" content="${policy}">`);
  assertOffline(page);
  return page;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [dist, wasm, wasmExec, out] = process.argv.slice(2);
  if (!out) {
    console.error("usage: node inline.mjs <dist dir> <wasm> <wasm_exec.js> <out.html>");
    process.exit(2);
  }
  const read = (p) => readFileSync(join(dist, p.replace(/^\.\//, "")), "utf8");
  const page = inlinePage(read("index.html"), { read, wasm: readFileSync(wasm), wasmExec: readFileSync(wasmExec, "utf8") });
  writeFileSync(out, page);
}
