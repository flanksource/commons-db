/**
 * The path the server mounted the app at, "" at the root. The server serves
 * index.html with its <base> at that path (www.Handler), so a host such as
 * gavel can mount query under a prefix of its own; every API call and route
 * the app makes is resolved against it. Code run outside a page (the unit
 * tests' node environment) has no <base>, and so no mount.
 */
export const mountPath = typeof document === "undefined" ? "" : mountPathOf(document.baseURI);

export function mountPathOf(baseURI: string): string {
  return new URL(baseURI).pathname.replace(/\/+$/, "");
}

/** A route the app links to, under the mount; absolute URLs pass through. */
export function mountedRoute(to: string, mount: string = mountPath): string {
  if (!mount || !to.startsWith("/") || to.startsWith("//")) return to;
  if (to === mount || to.startsWith(`${mount}/`) || to.startsWith(`${mount}?`)) return to;
  return `${mount}${to}`;
}

/** The app's route for a location pathname under the mount. */
export function unmountedPath(pathname: string, mount: string = mountPath): string {
  if (!mount || (pathname !== mount && !pathname.startsWith(`${mount}/`))) return pathname;
  return pathname.slice(mount.length) || "/";
}
