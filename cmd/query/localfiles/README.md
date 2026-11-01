# Local file browsing

The connection service mounts `GET /api/v1/connections/files?path=/path/to/directory`. An empty path starts at the server user's home; a file path opens its parent. Responses contain `path`, `parent`, and `entries` with `name`, `path`, `directory`, and an optional `problem`. File contents are never returned. Symlinks navigate to their targets; inaccessible entries carry an error.

Both the client address and HTTP Host must be loopback. Cross-site browser requests are refused. Forwarded headers do not grant access. Local development proxies may use a loopback Origin on another port.

Query exposes file-backed connections from the global XDG `flanksource/connections` folder, its configured state directory's `connections` folder, and the working directory's `.connections` folder. Later folders override earlier folders by namespace/name. They are live and read-only, and take precedence over database connection names. Normal connection access is available remotely; directory browsing is restricted to loopback.

Run `query serve --dev --port 8088`, open `http://localhost:8088/connection`, and choose Add Connection → SQLite → Browse. Selection updates the form draft; Save persists it.

Query passes `accept={[".db", ".sqlite", ".sqlite3"]}` to the generic shared picker. SQLite URL conversion lives in `www/src/sqliteFileURL.ts`: `sqliteFileURL` escapes path delimiters and retains existing DSN options, while `sqlitePath` extracts the initial browsing path. The shared component has no SQLite-specific filtering or URL handling.
