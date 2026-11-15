// A folder the reader picked, as the scan module reads it (internal/jsio.FS):
// stat, readDir and read, each a promise. Three ways in, by what the browser
// offers: a directory handle (showDirectoryPicker, Chromium), a dropped folder
// (webkitGetAsEntry, every engine) and the webkitdirectory file list (every
// engine; the browser lists every file before anything can be skipped). The
// first two read lazily: a file is opened only when the scan reads it.

export type Stat = { dir: boolean; size: number; mtime: number } | null;
export type Entry = { name: string; dir: boolean };

export interface Folder {
  name: string;
  stat(path: string): Promise<Stat>;
  readDir(path: string): Promise<Entry[]>;
  read(path: string, off: number, len: number): Promise<Uint8Array>;
  // The bytes read so far; the scan stops past maxRead.
  bytesRead: number;
}

// The most bytes a browser scan reads in total. A file is also capped by the
// scanner's own max_file_bytes; this bounds the sum (and git's pack files,
// read in slices).
export const maxRead = 2 * 2 ** 30;

const parts = (path: string) => (path === "." ? [] : path.split("/"));

abstract class Base implements Folder {
  bytesRead = 0;
  name: string;
  constructor(name: string) {
    this.name = name;
  }
  abstract stat(path: string): Promise<Stat>;
  abstract readDir(path: string): Promise<Entry[]>;
  protected abstract file(path: string): Promise<File | undefined>;
  async read(path: string, off: number, len: number): Promise<Uint8Array> {
    const f = await this.file(path);
    if (!f) throw new Error(`${path}: not found`);
    if (this.bytesRead + len > maxRead) throw new Error(`more than ${maxRead / 2 ** 30} GB read; scan this repository with the cernly CLI`);
    const data = new Uint8Array(await f.slice(off, off + len).arrayBuffer());
    this.bytesRead += data.length;
    return data;
  }
}

const statOf = (f: File): Stat => ({ dir: false, size: f.size, mtime: f.lastModified });

// A directory handle from showDirectoryPicker or a drop in Chromium.
export class HandleFolder extends Base {
  private dirs = new Map<string, FileSystemDirectoryHandle>();
  private files = new Map<string, File>();
  constructor(root: FileSystemDirectoryHandle) {
    super(root.name);
    this.dirs.set(".", root);
  }
  private async dirHandle(path: string): Promise<FileSystemDirectoryHandle | undefined> {
    const known = this.dirs.get(path);
    if (known) return known;
    const p = parts(path);
    const parent = await this.dirHandle(p.slice(0, -1).join("/") || ".");
    if (!parent) return undefined;
    try {
      const d = await parent.getDirectoryHandle(p[p.length - 1]!);
      this.dirs.set(path, d);
      return d;
    } catch {
      return undefined;
    }
  }
  protected async file(path: string): Promise<File | undefined> {
    const known = this.files.get(path);
    if (known) return known;
    const p = parts(path);
    const parent = await this.dirHandle(p.slice(0, -1).join("/") || ".");
    if (!parent) return undefined;
    try {
      const f = await (await parent.getFileHandle(p[p.length - 1]!)).getFile();
      this.files.set(path, f);
      return f;
    } catch {
      return undefined;
    }
  }
  async stat(path: string): Promise<Stat> {
    if (await this.dirHandle(path)) return { dir: true, size: 0, mtime: 0 };
    const f = await this.file(path);
    return f ? statOf(f) : null;
  }
  async readDir(path: string): Promise<Entry[]> {
    const d = await this.dirHandle(path);
    if (!d) throw new Error(`${path}: not a folder`);
    const out: Entry[] = [];
    for await (const h of (d as unknown as { values(): AsyncIterable<FileSystemHandle> }).values()) out.push({ name: h.name, dir: h.kind === "directory" });
    return out;
  }
}

// A dropped folder through the entries API.
export class EntryFolder extends Base {
  private dirs = new Map<string, Map<string, FileSystemEntry>>();
  private files = new Map<string, File>();
  private root: FileSystemDirectoryEntry;
  constructor(root: FileSystemDirectoryEntry) {
    super(root.name);
    this.root = root;
  }
  private async children(path: string): Promise<Map<string, FileSystemEntry> | undefined> {
    const known = this.dirs.get(path);
    if (known) return known;
    const dir = path === "." ? this.root : await this.entry(path);
    if (!dir || !dir.isDirectory) return undefined;
    const reader = (dir as FileSystemDirectoryEntry).createReader();
    const all = new Map<string, FileSystemEntry>();
    // readEntries answers in batches until it answers none.
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
      if (!batch.length) break;
      for (const e of batch) all.set(e.name, e);
    }
    this.dirs.set(path, all);
    return all;
  }
  private async entry(path: string): Promise<FileSystemEntry | undefined> {
    const p = parts(path);
    const kids = await this.children(p.slice(0, -1).join("/") || ".");
    return kids?.get(p[p.length - 1]!);
  }
  protected async file(path: string): Promise<File | undefined> {
    const known = this.files.get(path);
    if (known) return known;
    const e = await this.entry(path);
    if (!e || !e.isFile) return undefined;
    const f = await new Promise<File>((resolve, reject) => (e as FileSystemFileEntry).file(resolve, reject));
    this.files.set(path, f);
    return f;
  }
  async stat(path: string): Promise<Stat> {
    if (path === ".") return { dir: true, size: 0, mtime: 0 };
    const e = await this.entry(path);
    if (!e) return null;
    if (e.isDirectory) return { dir: true, size: 0, mtime: 0 };
    const f = await this.file(path);
    return f ? statOf(f) : null;
  }
  async readDir(path: string): Promise<Entry[]> {
    const kids = await this.children(path);
    if (!kids) throw new Error(`${path}: not a folder`);
    return [...kids.values()].map((e) => ({ name: e.name, dir: e.isDirectory }));
  }
}

// The file list of <input webkitdirectory>: every file, with its path below
// the picked folder in webkitRelativePath ("repo/src/a.ts").
export class ListFolder extends Base {
  private files = new Map<string, File>();
  private dirs = new Map<string, Map<string, boolean>>();
  constructor(list: File[]) {
    const first = list[0]?.webkitRelativePath.split("/")[0] ?? "folder";
    super(first);
    this.dirs.set(".", new Map());
    for (const f of list) {
      const p = f.webkitRelativePath.split("/").slice(1);
      if (!p.length) continue;
      const path = p.join("/");
      this.files.set(path, f);
      for (let i = 0; i < p.length; i++) {
        const dir = p.slice(0, i).join("/") || ".";
        if (!this.dirs.has(dir)) this.dirs.set(dir, new Map());
        this.dirs.get(dir)!.set(p[i]!, i < p.length - 1);
      }
    }
  }
  get count() {
    return this.files.size;
  }
  // Some browsers (Safari) leave hidden files and folders out of the list:
  // .git, .env, .github. A list with none cannot be scanned as the CLI would.
  get hidden() {
    for (const p of this.files.keys()) if (p.split("/").some((s) => s.startsWith("."))) return true;
    return false;
  }
  protected async file(path: string) {
    return this.files.get(path);
  }
  async stat(path: string): Promise<Stat> {
    if (this.dirs.has(path)) return { dir: true, size: 0, mtime: 0 };
    const f = this.files.get(path);
    return f ? statOf(f) : null;
  }
  async readDir(path: string): Promise<Entry[]> {
    const d = this.dirs.get(path);
    if (!d) throw new Error(`${path}: not a folder`);
    return [...d].map(([name, dir]) => ({ name, dir }));
  }
}

// The folder in a drop, if the drop holds exactly one: a handle where the
// browser offers one (lazy, and the same path as the picker), else an entry.
export async function droppedFolder(items: DataTransferItemList): Promise<Folder | "files" | undefined> {
  const list = [...items].filter((i) => i.kind === "file");
  if (list.length !== 1) return list.length ? "files" : undefined;
  const item = list[0]!;
  // getAsFileSystemHandle must be called before the drop event returns; so
  // must webkitGetAsEntry.
  const entry = item.webkitGetAsEntry?.();
  const handle = (item as unknown as { getAsFileSystemHandle?: () => Promise<FileSystemHandle | null> }).getAsFileSystemHandle?.();
  if (handle) {
    const h = await handle.catch(() => null);
    if (h && h.kind === "directory") return new HandleFolder(h as FileSystemDirectoryHandle);
  }
  if (entry?.isDirectory) return new EntryFolder(entry as FileSystemDirectoryEntry);
  return "files";
}

export const canPickFolder = () => typeof (globalThis as unknown as { showDirectoryPicker?: unknown }).showDirectoryPicker === "function";

export async function pickFolder(): Promise<Folder | undefined> {
  try {
    const h = await (globalThis as unknown as { showDirectoryPicker(o: object): Promise<FileSystemDirectoryHandle> }).showDirectoryPicker({ mode: "read" });
    return new HandleFolder(h);
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") return undefined;
    throw e;
  }
}

// What the page says before it scans: a git repository (tracked files only)
// or a folder without one (every file is read).
export async function kind(f: Folder): Promise<"git" | "git-file" | "plain"> {
  const s = await f.stat(".git");
  if (!s) return "plain";
  return s.dir ? "git" : "git-file";
}
