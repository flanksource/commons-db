// Checks every internal href (and #fragment) in a built Starlight dist directory.
import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, dirname, resolve, relative } from "node:path";

const dist = resolve(process.argv[2] ?? "dist");
const pages = [];
const walk = (dir) => {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) walk(path);
    else if (path.endsWith(".html")) pages.push(path);
  }
};
walk(dist);

const idsOf = new Map();
const ids = (file) => {
  if (!idsOf.has(file)) {
    idsOf.set(file, new Set([...readFileSync(file, "utf8").matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
  }
  return idsOf.get(file);
};

const target = (from, href) => {
  const [path, fragment] = href.split("#");
  let file = path === "" ? from : path.startsWith("/") ? join(dist, path) : resolve(dirname(from), path);
  if (path !== "" && (path.endsWith("/") || !path.includes("."))) file = join(file, "index.html");
  return { file, fragment };
};

let broken = 0;
for (const page of pages) {
  const html = readFileSync(page, "utf8");
  for (const [, href] of html.matchAll(/href="([^"]+)"/g)) {
    if (/^(https?:|mailto:|data:)/.test(href) || href.startsWith("/_astro/") || href.endsWith(".css") || href.endsWith(".svg")) continue;
    const { file, fragment } = target(page, href);
    const problem = !existsSync(file) ? "missing page" : fragment && !ids(file).has(decodeURIComponent(fragment)) ? "missing anchor" : null;
    if (problem) {
      broken++;
      console.log(`${relative(dist, page)}: ${href} (${problem})`);
    }
  }
}
console.log(`${pages.length} pages checked, ${broken} broken links`);
process.exit(broken ? 1 : 0);
