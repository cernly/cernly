// Builds what the browser tests compare against: a fixture git repository
// (the CLI's compose fixture plus the java plugin's shop, packed by git gc),
// the java plugin signed with the public test key, and `cernly scan` of both
// the repository and a copy without .git. The page itself is built by
// `CERNLY_VIEWER_TEST_PLUGINS=1 web/viewer/build.sh` before the tests run.
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const core = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
export const statePath = join(tmpdir(), "cernly-viewer-e2e.json");

const run = (cmd, args, opts = {}) => execFileSync(cmd, args, { cwd: core, stdio: ["ignore", "pipe", "inherit"], ...opts, env: { ...process.env, GOTOOLCHAIN: "go1.26.10", ...opts.env } });

export default function setup() {
  const tmp = mkdtempSync(join(tmpdir(), "cernly-e2e-"));
  const repo = join(tmp, "shop");
  cpSync(join(core, "cli", "testdata", "fixture"), repo, { recursive: true });
  cpSync(join(core, "wasmplugins", "java", "testdata", "shop"), join(repo, "java"), { recursive: true });
  const env = {
    GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1",
    GIT_AUTHOR_NAME: "Acme", GIT_AUTHOR_EMAIL: "dev@example.com", GIT_AUTHOR_DATE: "2026-11-01T10:00:00+01:00",
    GIT_COMMITTER_NAME: "Acme", GIT_COMMITTER_EMAIL: "dev@example.com", GIT_COMMITTER_DATE: "2026-11-01T10:00:00+01:00",
  };
  const git = (...args) => run("git", ["-C", repo, ...args], { env });
  git("init", "-q", "-b", "main");
  git("remote", "add", "origin", "https://example.com/acme/shop.git");
  git("add", "-A");
  git("commit", "-q", "-m", "fixture");
  git("gc", "-q");
  // Untracked: never read in git mode, read without .git.
  writeFileSync(join(repo, "untracked.env"), "SECRET_TOKEN=x\n");
  const plain = join(tmp, "plain");
  cpSync(repo, plain, { recursive: true, filter: (p) => !p.split("/").includes(".git") });

  const plugins = join(tmp, "plugins");
  mkdirSync(plugins);
  run("go", ["build", "-trimpath", "-buildvcs=false", "-o", join(plugins, "java.wasm"), "./wasmplugins/java"], { env: { GOOS: "wasip1", GOARCH: "wasm", CGO_ENABLED: "0" } });
  run("go", ["run", "./cmd/cernly", "plugin", "keygen", "--test", "--out", join(tmp, "test")]);
  run("go", ["run", "./cmd/cernly", "plugin", "sign", "--key", join(tmp, "test.key"), "--manifest", "wasmplugins/java/manifest.json", join(plugins, "java.wasm")]);
  const scan = (dir, ...extra) => run("go", ["run", "./cmd/cernly", "scan", "--plugins", plugins, "--trust-key", join(tmp, "test.pub"), ...extra, dir]);
  writeFileSync(join(tmp, "repo.facts.json"), scan(repo));
  writeFileSync(join(tmp, "plain.facts.json"), scan(plain, "--repo", "local/plain", "--commit", "0".repeat(40), "--committed-at", "1970-01-01T00:00:00Z"));
  const page = process.env.CERNLY_VIEWER_PAGE || join(core, "dist", "viewer", "cernly-viewer.html");
  writeFileSync(statePath, JSON.stringify({ tmp, repo, plain, page }));
}
