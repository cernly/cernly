// "Prepare an upload", end to end in Chromium, Firefox and WebKit: a facts file opened
// in the built page is shown as the upload file would hold it, with the repo id and
// the evidence paths replaced by tokens, and the two files it saves are what the panel
// says. With redaction off the saved file is the input, byte for byte. No request
// leaves the page.
import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { statePath } from "./setup.mjs";

const state = JSON.parse(readFileSync(statePath, "utf8"));
const inputPath = join(state.tmp, "repo.facts.json");
const input = readFileSync(inputPath, "utf8");
const scan = JSON.parse(input);
// Evidence paths that redaction changes: not every segment is a well-known file name.
const wellKnown = new Set([".github", "workflows", "CODEOWNERS", "Dockerfile", "Caddyfile", "go.mod", "go.sum", "package.json", "catalog-info.yaml", "docker-compose.yml"]);
const paths = [...new Set(scan.facts.flatMap((f) => f.evidence.map((e) => e.path)))].filter((p) => p.length > 4 && p.split("/").some((s) => !wellKnown.has(s)));

async function saved(page, name) {
  const [download] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name }).click()]);
  return { name: download.suggestedFilename(), text: readFileSync(await download.path(), "utf8") };
}

test("the upload file and the token list, redacted and not", async ({ page }) => {
  const seen = [];
  page.on("request", (r) => { seen[seen.length] = r.url(); });
  await page.goto(pathToFileURL(state.page).href);
  await page.locator('input[accept=".json"]').setInputFiles(inputPath);
  await expect(page.getByRole("button", { name: "Overview" })).toBeVisible();

  // The build is named so its hash can be compared with the published one.
  await expect(page.getByTestId("build")).toHaveText(/^cernly \d+\.\d+\.\d+, module sha256 [0-9a-f]{12}…$/);
  expect(await page.getByTestId("build").getAttribute("title")).toMatch(/^module sha256 [0-9a-f]{64}$/);

  // Collapsed until asked for.
  await expect(page.getByTestId("upload-preview")).toHaveCount(0);
  await page.getByRole("button", { name: "Prepare an upload" }).click();
  const box = page.getByRole("checkbox", { name: "Redact paths, names, hosts and the repo id" });
  await expect(box).toBeChecked();
  await expect(page.getByText("This page cannot send anything; you upload the file yourself.")).toBeVisible();

  const preview = await page.getByTestId("upload-preview").textContent();
  expect(preview).toMatch(/\b(?:name|path)-\d+\b/);
  expect(preview).not.toContain(scan.facts[0].attrs.name);
  expect(preview).not.toContain(scan.repo);
  for (const p of paths) expect(preview).not.toContain(p);
  expect(preview.split("\n").length).toBeLessThanOrEqual(41);
  await expect(page.getByTestId("upload-counts")).toHaveText(/^Redacted: \d+ paths, \d+ names, \d+ hosts, 1 repo id\.$/);
  await expect(page.getByText("Keep the token list: it turns the tokens back into your names.")).toBeVisible();

  const upload = await saved(page, "Save upload file");
  expect(upload.name).toBe("repo.upload.json");
  const redacted = JSON.parse(upload.text);
  expect(upload.text.endsWith("}\n")).toBe(true);
  expect(redacted.repo).toBe("repo-1");
  expect(redacted.commit).toEqual(scan.commit);
  expect(redacted.facts.map((f) => f.kind)).toEqual(scan.facts.map((f) => f.kind));
  expect(upload.text).not.toContain(scan.repo);
  for (const p of paths) expect(upload.text).not.toContain(`"${p}"`);

  const tokens = await saved(page, "Save token list");
  expect(tokens.name).toBe("repo.tokens.json");
  const list = JSON.parse(tokens.text);
  expect(list.schema).toBe("cernly.redaction/v1");
  expect(list.tokens).toContainEqual({ token: "repo-1", value: scan.repo });
  const value = new Map(list.tokens.map((t) => [t.token, t.value]));
  const back = (s) => s.replace(/\b(?:host|name|path|repo)-\d+\b/g, (t) => value.get(t) ?? t);
  scan.facts.forEach((f, i) => expect(back(redacted.facts[i].key)).toBe(f.key));

  // Off: the file is the scan as it is, and there is no token list.
  await box.uncheck();
  await expect(page.getByRole("button", { name: "Save token list" })).toHaveCount(0);
  await expect(page.getByTestId("upload-counts")).toHaveText("Nothing is redacted: the file is the scan as it is.");
  expect(await page.getByTestId("upload-preview").textContent()).toContain(scan.facts[0].attrs.name);
  const plain = await saved(page, "Save upload file");
  expect(plain.name).toBe("repo.upload.json");
  expect(plain.text).toBe(input);

  expect(seen.filter((u) => !/^(file:|data:|blob:)/.test(u))).toEqual([]);
});
