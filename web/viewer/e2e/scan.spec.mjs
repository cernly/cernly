// The browser scan, end to end in Chromium, Firefox and WebKit: the built page
// opened from disk scans a git repository and a plain folder through the
// webkitdirectory input, and the facts.json it saves is byte for byte what
// `cernly scan` wrote for the same folder. Chromium also scans through a
// directory handle (the showDirectoryPicker path), served over http with the
// header policy. No request leaves the page.
import { expect, test } from "@playwright/test";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { join, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { statePath } from "./setup.mjs";

const state = JSON.parse(readFileSync(statePath, "utf8"));

// Every request the page makes must be the page itself or in-page data.
function offline(page) {
  const seen = [];
  page.on("request", (r) => { seen[seen.length] = r.url(); });
  return () => seen.filter((u) => !/^(file:|data:|blob:)/.test(u) && u !== serverURL);
}
let serverURL = "";

async function scanWithInput(page, dir) {
  // Hide the picker so the page offers <input webkitdirectory>, the path every engine has.
  await page.addInitScript(() => { delete window.showDirectoryPicker; });
  await page.goto(pathToFileURL(state.page).href);
  await page.locator('input[webkitdirectory]').setInputFiles(dir);
}

// Keeps what the page gave beside the CLI's file, to diff when they differ.
function same(got, want, name) {
  if (got !== readFileSync(join(state.tmp, want), "utf8")) writeFileSync(join(state.tmp, `got-${name}`), got);
  expect(got).toBe(readFileSync(join(state.tmp, want), "utf8"));
}

// WebKit's directory input leaves hidden files out: the page refuses to scan
// such a list and says to drop the folder instead.
async function noHidden(page) {
  await expect(page.getByRole("alert")).toContainText("left out the folder's hidden files");
  await expect(page.getByRole("button", { name: "Scan", exact: true })).toHaveCount(0);
}

async function saved(page) {
  await page.getByRole("button", { name: "Scan", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Scanned in this page" })).toBeVisible({ timeout: 90_000 });
  const [download] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Save facts.json" }).click()]);
  return readFileSync(await download.path(), "utf8");
}

test("a git repository scans to the same bytes as the CLI", async ({ page, browserName }) => {
  const external = offline(page);
  await scanWithInput(page, state.repo);
  if (browserName === "webkit") return noHidden(page);
  await expect(page.getByText("A git repository.")).toBeVisible();
  const facts = await saved(page);
  same(facts, "repo.facts.json", `${browserName}-repo.json`);
  expect(facts).not.toContain("untracked.env");
  expect(JSON.parse(facts).plugins.find((p) => p.name === "java").status).toBe("ok");
  await expect(page.getByRole("button", { name: "Overview" })).toBeVisible();
  expect(external()).toEqual([]);
});

test("a folder without .git reads every file and says so first", async ({ page, browserName }) => {
  await scanWithInput(page, state.plain);
  if (browserName === "webkit") return noHidden(page);
  await expect(page.getByText(/has no .git, so every file in it is read/)).toBeVisible();
  const facts = await saved(page);
  same(facts, "plain.facts.json", `${browserName}-plain.json`);
});

test("the page cannot make a request", async ({ page }) => {
  await page.goto(pathToFileURL(state.page).href);
  const blocked = await page.evaluate(async () => {
    const tries = [fetch("https://example.com/").then(() => "sent", () => "blocked")];
    tries.push(new Promise((r) => { const x = new XMLHttpRequest(); x.onerror = () => r("blocked"); x.onload = () => r("sent"); try { x.open("GET", "https://example.com/"); x.send(); } catch { r("blocked"); } }));
    return Promise.all(tries);
  });
  expect(blocked).toEqual(["blocked", "blocked"]);
});

// The repository's files as [path, base64], for building it inside a page.
function fixtureFiles() {
  const files = [];
  const walk = (d) => {
    for (const n of readdirSync(d)) {
      const p = join(d, n);
      if (statSync(p).isDirectory()) walk(p);
      else files[files.length] = [relative(state.repo, p), readFileSync(p).toString("base64")];
    }
  };
  walk(state.repo);
  return files;
}

// A folder dropped on the page, through the entries API (webkitGetAsEntry),
// the path Firefox and Safari take: the drop carries an entry tree built in
// the page from the repository's files.
test("a dropped folder scans to the same bytes", async ({ page, browserName }) => {
  const external = offline(page);
  await page.goto(pathToFileURL(state.page).href);
  await page.evaluate((files) => {
    const root = {};
    for (const [path, b64] of files) {
      const parts = path.split("/");
      let d = root;
      for (const p of parts.slice(0, -1)) d = d[p] ??= {};
      d[parts.at(-1)] = new File([Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))], parts.at(-1), { lastModified: 1700000000000 });
    }
    const entry = (name, node) => node instanceof File
      ? { name, isFile: true, isDirectory: false, file: (ok) => ok(node) }
      : { name, isFile: false, isDirectory: true, createReader() { let done = false; return { readEntries: (ok) => { ok(done ? [] : Object.entries(node).map(([n, v]) => entry(n, v))); done = true; } }; } };
    const item = { kind: "file", webkitGetAsEntry: () => entry("shop", root) };
    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", { value: { items: [item], files: [] } });
    document.querySelector(".drop").dispatchEvent(drop);
  }, fixtureFiles());
  await expect(page.getByText("A git repository.")).toBeVisible();
  same(await saved(page), "repo.facts.json", `${browserName}-drop.json`);
  expect(external()).toEqual([]);
});

// Served over http with the header policy the hosted page uses, through a
// directory handle: the showDirectoryPicker path, with a copy of the
// repository in the origin's private file system standing in for the picker
// (which only Chromium has). This runs the scan itself in every engine.
test("a directory handle scans to the same bytes", async ({ page, browserName }) => {
  test.skip(browserName === "webkit", "WebKit's private file system refuses writes when headless; the drop test covers its scan");
  const html = readFileSync(state.page);
  const meta = /http-equiv="Content-Security-Policy" content="([^"]+)"/.exec(html.toString())[1];
  const server = createServer((_, res) => {
    res.writeHead(200, { "content-type": "text/html; charset=utf-8", "content-security-policy": `${meta}; frame-ancestors 'none'` });
    res.end(html);
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  serverURL = `http://127.0.0.1:${server.address().port}/`;
  try {
    const external = offline(page);
    await page.addInitScript(() => { window.showDirectoryPicker = async () => window.cernlyTestRoot; });
    await page.goto(serverURL);
    const files = fixtureFiles();
    await page.evaluate(async (files) => {
      const root = await (await navigator.storage.getDirectory()).getDirectoryHandle("shop", { create: true });
      for (const [path, b64] of files) {
        const parts = path.split("/");
        let d = root;
        for (const p of parts.slice(0, -1)) d = await d.getDirectoryHandle(p, { create: true });
        const w = await (await d.getFileHandle(parts.at(-1), { create: true })).createWritable();
        await w.write(Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)));
        await w.close();
      }
      window.cernlyTestRoot = root;
    }, files);
    await page.getByRole("button", { name: "Choose a folder" }).click();
    await expect(page.getByText("A git repository.")).toBeVisible();
    same(await saved(page), "repo.facts.json", `${browserName}-handle.json`);
    expect(external()).toEqual([]);
  } finally {
    server.close();
  }
});
