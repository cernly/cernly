// Runs one outside plugin (a WASI preview 1 module) the way plugin/wasm runs it
// under wazero: no files, no directories, an empty environment, stdin is the
// JSON input, stdout the facts. The clocks and the random bytes match wazero's
// defaults, so a plugin gives the same bytes in the browser and in the CLI:
// the wall clock starts at 2022-01-01T00:00:00Z and the monotonic clock at 0,
// each 1 ms later per reading; sleeps return at once; random bytes come from
// the caller (Go's math/rand seeded 42). A plain script, no imports: the page
// puts its text into the plugin Worker, and the tests load it in Node.
(function (root) {
  "use strict";
  const E = { SUCCESS: 0, BADF: 8, FAULT: 21, INVAL: 28, IO: 29, NOSYS: 52, NOTSUP: 58 };
  const MS = 1000000n;
  // The most iovecs one fd_read or fd_write takes (Linux's IOV_MAX) and the
  // most subscriptions one poll_oneoff takes: a plugin cannot make the host
  // allocate past them.
  const maxIovs = 1024;
  const maxSubs = 64;
  const EPOCH = 1640995200000n * MS;

  class Exit {
    constructor(code) { this.code = code; }
  }

  function leb(bytes, at) {
    let n = 0, shift = 0, b;
    do {
      b = bytes[at++];
      if (b === undefined) throw new Error("truncated module");
      n += (b & 0x7f) * 2 ** shift;
      shift += 7;
    } while (b & 0x80);
    return [n, at];
  }
  const ulebBytes = (n) => {
    const out = [];
    do {
      let b = n & 0x7f;
      n = Math.floor(n / 128);
      if (n) b |= 0x80;
      out.push(b);
    } while (n);
    return out;
  };

  // capMemory rewrites the module's own memory to declare maxPages as its
  // maximum (lower an existing one only), so memory.grow fails past the cap as
  // under wazero's WithMemoryLimitPages. A module that imports its memory has
  // none of its own and is refused by run.
  function capMemory(bytes, maxPages) {
    if (bytes.length < 8 || bytes[0] !== 0 || bytes[1] !== 0x61 || bytes[2] !== 0x73 || bytes[3] !== 0x6d) throw new Error("not a WebAssembly module");
    let at = 8;
    while (at < bytes.length) {
      const id = bytes[at];
      const [size, body] = leb(bytes, at + 1);
      const end = body + size;
      if (id === 5) {
        let [count, p] = leb(bytes, body);
        const parts = [ulebBytes(count)];
        for (let i = 0; i < count; i++) {
          const flags = bytes[p++];
          if (flags > 1) throw new Error("shared or 64-bit memory is not supported");
          let min, max;
          [min, p] = leb(bytes, p);
          if (flags === 1) [max, p] = leb(bytes, p);
          if (min > maxPages) throw new Error(`the module asks for ${min} pages of memory, more than the ${maxPages} allowed`);
          max = max === undefined ? maxPages : Math.min(max, maxPages);
          parts.push([1], ulebBytes(min), ulebBytes(max));
        }
        const content = parts.flat();
        const out = new Uint8Array(at + 1 + ulebBytes(content.length).length + content.length + (bytes.length - end));
        out.set(bytes.subarray(0, at + 1), 0);
        let o = at + 1;
        for (const b of ulebBytes(content.length)) out[o++] = b;
        out.set(content, o);
        out.set(bytes.subarray(end), o + content.length);
        return out;
      }
      at = end;
    }
    return bytes;
  }

  // run instantiates and runs the module once. opts: name (argv[0]), random
  // (Uint8Array), maxPages, maxOut (stdout bytes), maxErr (stderr bytes).
  // Returns { stdout, stderr, exit, overOut, error }.
  function run(moduleBytes, stdin, opts) {
    const mod = new WebAssembly.Module(capMemory(new Uint8Array(moduleBytes), opts.maxPages));
    for (const imp of WebAssembly.Module.imports(mod)) {
      if (imp.module !== "wasi_snapshot_preview1") throw new Error(`imports ${imp.module}.${imp.name}; only wasi_snapshot_preview1 is provided`);
      if (imp.kind !== "function") throw new Error(`imports ${imp.kind} ${imp.name}; only functions are provided`);
    }
    let memory;
    const mem = () => new DataView(memory.buffer);
    const bytes = () => new Uint8Array(memory.buffer);
    const args = [new TextEncoder().encode(opts.name + "\0")];
    let stdinAt = 0;
    let randAt = 0;
    let wall = EPOCH - MS;
    let mono = -MS;
    const out = { chunks: [], size: 0, max: opts.maxOut, over: false };
    const err = { chunks: [], size: 0, max: opts.maxErr, over: false };
    const write = (sink, data) => {
      if (sink.size + data.length > sink.max) {
        sink.over = true;
        return false;
      }
      sink.chunks.push(data.slice());
      sink.size += data.length;
      return true;
    };
    // fits says whether n records of size bytes at ptr lie in memory; n is
    // capped before anything is allocated for them on the host.
    const fits = (ptr, n, size, cap) => n <= cap && ptr + n * size <= memory.buffer.byteLength;
    // iovs reads n iovecs at ptr, each inside memory; null when they are not.
    const iovs = (ptr, n) => {
      ptr >>>= 0;
      n >>>= 0;
      if (!fits(ptr, n, 8, maxIovs)) return null;
      const v = mem();
      const list = [];
      for (let i = 0; i < n; i++) {
        const at = v.getUint32(ptr + 8 * i, true);
        const len = v.getUint32(ptr + 8 * i + 4, true);
        if (at + len > memory.buffer.byteLength) return null;
        list.push([at, len]);
      }
      return list;
    };
    const wasi = {
      args_sizes_get(argc, bufSize) {
        mem().setUint32(argc, args.length, true);
        mem().setUint32(bufSize, args.reduce((n, a) => n + a.length, 0), true);
        return E.SUCCESS;
      },
      args_get(argv, buf) {
        for (const a of args) {
          mem().setUint32(argv, buf, true);
          bytes().set(a, buf);
          argv += 4;
          buf += a.length;
        }
        return E.SUCCESS;
      },
      environ_sizes_get(count, size) {
        mem().setUint32(count, 0, true);
        mem().setUint32(size, 0, true);
        return E.SUCCESS;
      },
      environ_get() { return E.SUCCESS; },
      clock_res_get(_id, res) {
        mem().setBigUint64(res, 1000n, true);
        return E.SUCCESS;
      },
      clock_time_get(id, _precision, result) {
        let v;
        if (id === 0) v = wall += MS;
        else if (id === 1) v = mono += MS;
        else return E.INVAL;
        mem().setBigUint64(result, v, true);
        return E.SUCCESS;
      },
      random_get(buf, len) {
        if (randAt + len > opts.random.length) return E.IO;
        bytes().set(opts.random.subarray(randAt, randAt + len), buf);
        randAt += len;
        return E.SUCCESS;
      },
      fd_read(fd, ptr, n, nread) {
        if (fd !== 0) return E.BADF;
        const list = iovs(ptr, n);
        if (!list) return E.FAULT;
        let got = 0;
        for (const [at, len] of list) {
          const chunk = stdin.subarray(stdinAt, stdinAt + len);
          bytes().set(chunk, at);
          stdinAt += chunk.length;
          got += chunk.length;
          if (chunk.length < len) break;
        }
        mem().setUint32(nread, got, true);
        return E.SUCCESS;
      },
      fd_write(fd, ptr, n, nwritten) {
        const sink = fd === 1 ? out : fd === 2 ? err : undefined;
        if (!sink) return E.BADF;
        const list = iovs(ptr, n);
        if (!list) return E.FAULT;
        let put = 0;
        for (const [at, len] of list) {
          if (!write(sink, bytes().subarray(at, at + len))) return E.IO;
          put += len;
        }
        mem().setUint32(nwritten, put, true);
        return E.SUCCESS;
      },
      fd_close(fd) { return fd <= 2 ? E.SUCCESS : E.BADF; },
      fd_fdstat_get(fd, ptr) {
        if (fd > 2) return E.BADF;
        const v = mem();
        v.setUint16(ptr, 1, true); // a device, as wazero's stdio
        v.setUint16(ptr + 2, 0, true);
        v.setUint32(ptr + 4, 0, true);
        v.setBigUint64(ptr + 8, 0x1fffn, true);
        v.setBigUint64(ptr + 16, 0n, true);
        return E.SUCCESS;
      },
      fd_fdstat_set_flags(fd, flags) {
        if (fd > 2) return E.BADF;
        return flags & 4 ? E.NOSYS : E.SUCCESS; // nonblocking stdio is not supported, as in wazero
      },
      fd_prestat_get() { return E.BADF; },
      fd_prestat_dir_name() { return E.BADF; },
      path_open() { return E.BADF; },
      path_filestat_get() { return E.BADF; },
      poll_oneoff(inPtr, outPtr, nsubs, nevents) {
        nsubs >>>= 0;
        inPtr >>>= 0;
        outPtr >>>= 0;
        if (nsubs === 0) return E.INVAL;
        if (!fits(inPtr, nsubs, 48, Infinity) || !fits(outPtr, nsubs, 32, Infinity)) return E.FAULT;
        if (nsubs > maxSubs) return E.INVAL;
        const v = mem();
        let n = 0;
        for (let i = 0; i < nsubs; i++) {
          const s = inPtr + 48 * i;
          const type = v.getUint8(s + 8);
          const o = outPtr + 32 * n;
          bytes().fill(0, o, o + 32);
          bytes().copyWithin(o, s, s + 8);
          v.setUint8(o + 10, type);
          if (type === 0) {
            // A clock: sleeps return at once.
          } else if (type === 1 || type === 2) {
            const fd = v.getUint32(s + 16, true);
            v.setUint16(o + 8, fd > 2 ? E.BADF : type === 2 ? E.NOTSUP : E.SUCCESS, true);
          } else return E.INVAL;
          n++;
        }
        v.setUint32(nevents, n, true);
        return E.SUCCESS;
      },
      sched_yield() { return E.SUCCESS; },
      proc_exit(code) { throw new Exit(code); },
    };
    const imports = {};
    for (const imp of WebAssembly.Module.imports(mod)) imports[imp.name] = wasi[imp.name] || (() => E.NOSYS);
    const inst = new WebAssembly.Instance(mod, { wasi_snapshot_preview1: imports });
    memory = inst.exports.memory;
    if (!(memory instanceof WebAssembly.Memory)) throw new Error("the module exports no memory");
    let exit = 0;
    let error;
    try {
      inst.exports._start();
    } catch (e) {
      if (e instanceof Exit) exit = e.code;
      else error = e instanceof Error ? e.message : String(e);
    }
    const cat = (sink) => {
      const all = new Uint8Array(sink.size);
      let at = 0;
      for (const c of sink.chunks) {
        all.set(c, at);
        at += c.length;
      }
      return all;
    };
    return { stdout: cat(out), stderr: cat(err), exit, overOut: out.over, error };
  }

  const api = { run, capMemory };
  root.cernlyWasi = api;

  // As a Worker: one message { module, stdin, opts } in, one result out.
  if (typeof root.postMessage === "function" && typeof root.document === "undefined" && typeof root.WorkerGlobalScope !== "undefined") {
    root.onmessage = (e) => {
      let res;
      try {
        res = run(e.data.module, e.data.stdin, e.data.opts);
      } catch (err) {
        res = { error: err instanceof Error ? err.message : String(err) };
      }
      root.postMessage(res);
    };
  }
})(globalThis);
