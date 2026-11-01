export function sqlitePath(value: string): string {
  if (value.startsWith("file:")) return decodeURIComponent(new URL(value).pathname);
  if (value.startsWith("sqlite://")) return decodeURIComponent(value.slice(9).split("?")[0] ?? "");
  return value.split("?")[0] ?? "";
}

export function sqliteFileURL(path: string, previous: string): string {
  const query = previous.indexOf("?");
  return `file:${path.split("/").map(encodeURIComponent).join("/")}${query < 0 ? "" : previous.slice(query)}`;
}
