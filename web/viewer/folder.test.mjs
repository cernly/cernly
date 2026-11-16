// The folder sources the scan reads through (src/folder.ts). Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { EntryFolder, ListFolder, maxRead } from "./src/folder.ts";

const file = (path, text, mtime = 1000) => {
  const f = new File([text], path.split("/").at(-1), { lastModified: mtime });
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
};

// A fake FileSystemDirectoryEntry tree from {name: text | {…}}; readEntries
// answers in batches of two, as real ones answer in batches.
function entries(name, tree) {
  const dir = {
    name, isDirectory: true, isFile: false,
    createReader() {
      const all = Object.entries(tree).map(([n, v]) => (typeof v === "string" ? { name: n, isDirectory: false, isFile: true, file: (ok) => ok(new File([v], n, { lastModified: 7 })) } : entries(n, v)));
      let at = 0;
      return { readEntries: (ok) => setTimeout(() => { ok(all.slice(at, at + 2)); at += 2; }, 0) };
    },
  };
  return dir;
}

const text = async (f, path) => new TextDecoder().decode(await f.read(path, 0, 1 << 20));

test("a webkitdirectory list answers stat, readDir and read below the picked folder", async () => {
  const f = new ListFolder([file("shop/.git/HEAD", "ref: refs/heads/main\n"), file("shop/src/a.ts", "export {}\n", 5), file("shop/README.md", "hi\n")]);
  assert.equal(f.name, "shop");
  assert.equal(f.count, 3);
  assert.ok(f.hidden);
  assert.deepEqual(await f.stat("."), { dir: true, size: 0, mtime: 0 });
  assert.deepEqual(await f.stat("src/a.ts"), { dir: false, size: 10, mtime: 5 });
  assert.equal(await f.stat("nope"), null);
  assert.deepEqual((await f.readDir(".")).sort((a, b) => a.name.localeCompare(b.name)), [{ name: ".git", dir: true }, { name: "README.md", dir: false }, { name: "src", dir: true }]);
  assert.equal(await text(f, ".git/HEAD"), "ref: refs/heads/main\n");
  assert.equal(new TextDecoder().decode(await f.read("src/a.ts", 7, 100)), "{}\n");
  assert.equal(f.bytesRead, 21 + 3);
  await assert.rejects(f.readDir("README.md"), /not a folder/);
});

test("a list without hidden files says so", () => {
  assert.equal(new ListFolder([file("shop/src/a.ts", "x")]).hidden, false);
});

test("reads stop past the total the page reads", async () => {
  const f = new ListFolder([file("shop/a", "abc")]);
  f.bytesRead = maxRead - 1;
  await assert.rejects(f.read("a", 0, 3), /GB read/);
});

test("a dropped folder reads lazily through the entries API, every batch", async () => {
  const f = new EntryFolder(entries("shop", { ".git": { HEAD: "ref: x\n" }, a: "1", b: "2", c: { d: "deep" } }));
  assert.equal(f.name, "shop");
  assert.deepEqual((await f.readDir(".")).map((e) => e.name).sort(), [".git", "a", "b", "c"]);
  assert.deepEqual(await f.stat(".git"), { dir: true, size: 0, mtime: 0 });
  assert.deepEqual(await f.stat("c/d"), { dir: false, size: 4, mtime: 7 });
  assert.equal(await f.stat("c/e"), null);
  assert.equal(await text(f, ".git/HEAD"), "ref: x\n");
  await assert.rejects(f.read("missing", 0, 1), /not found/);
});
