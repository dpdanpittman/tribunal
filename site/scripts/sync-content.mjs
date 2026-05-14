// sync-content.mjs — copies the canonical methodology docs + audit
// reports into site/_content/ so Astro's import.meta.glob can find them
// regardless of whether the build runs from the repo root or from inside
// a docker context that only sees site/.
//
// Behavior:
//   - If the parent repo's docs/ and .tribunal/reports/ are reachable (the
//     normal case when building from the repo root), refresh _content/
//     from them.
//   - If they're NOT reachable (e.g. building inside a docker container
//     whose context is only site/), preserve whatever _content/ already
//     has — deploy.sh's job to populate it before the rsync.
//
// Run via `npm run sync` or implicitly via prebuild/predev.

import { existsSync, mkdirSync, rmSync, cpSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const siteRoot = resolve(__dirname, "..");
const repoRoot = resolve(siteRoot, "..");

const sources = [
  { from: join(repoRoot, "docs"), to: join(siteRoot, "_content", "docs") },
  { from: join(repoRoot, ".tribunal", "reports"), to: join(siteRoot, "_content", "reports") },
];

const anyReachable = sources.some((s) => existsSync(s.from));
if (!anyReachable) {
  console.log(`sync-content: parent docs/reports not reachable (likely inside docker build); preserving existing _content/`);
  // Ensure the dir at least exists so Astro's glob doesn't error.
  mkdirSync(join(siteRoot, "_content"), { recursive: true });
  process.exit(0);
}

console.log(`sync-content: refreshing _content/ from repo root`);
for (const { from, to } of sources) {
  if (!existsSync(from)) {
    console.warn(`sync-content: skipping ${from} (not present)`);
    continue;
  }
  rmSync(to, { recursive: true, force: true });
  mkdirSync(dirname(to), { recursive: true });
  cpSync(from, to, { recursive: true });
  console.log(`sync-content: ${from} -> ${to}`);
}
