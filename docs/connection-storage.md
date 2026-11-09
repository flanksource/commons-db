# Connection addresses and storage

The shared connection form accepts a **Database URL** or **Host** and **Database** for PostgreSQL, MySQL, SQL Server and ClickHouse. **Port** is optional and uses the database driver's default. Username and password remain separate credential fields. An explicit URL takes precedence over host fields. SQLite continues to use a database file path.

For example, this connection uses separate address and credential fields:

```yaml
apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata:
  name: orders
spec:
  type: postgres
  username:
    value: reader
  password:
    valueFrom:
      secretKeyRef:
        name: database-credentials
        key: password
  properties:
    host: db.example.com
    port: "5432"
    database: orders
```

The connection list shows the storage layer and source path. **Storage layer** filters the list and is retained in the URL. **Save to** in the create/edit form offers the host's writable destinations:

| Layer | Location |
| --- | --- |
| Database | The host's configured connection database |
| Global files | `$XDG_CONFIG_HOME/flanksource/connections` |
| Application files | The app's connection directory; Query uses `<config-dir>/connections` |
| Project files | `.connections` in the server's working directory |

Paths refer to the server's filesystem. Changing **Save to** moves the definition, preserves its ID and credentials left blank, and removes the previous entry after the destination is saved. A conflicting destination or read-only source is rejected. Saved files retain the connection ID in `metadata.uid` so references and open detail pages remain valid when it moves.

The shared CLI exposes the destination as `--layer`, including through Gavel:

```bash
gavel connections add http --name example --url https://example.com --layer app
gavel connections edit example --layer global
```

Hosts advertise destinations at `GET /api/v1/connections/storage` and include the `layer` field in connection form schemas. Create and update requests use `layer: "db"`, `"global"`, `"app"` or `"cwd"`. The exact choices and default are supplied by the host; hosts with an encrypted database catalog can expose only that destination.

The upper-right KEK indicator shows **ready**, **missing**, **unavailable** or **not configured**. Reading status never initializes or replaces a key. A host without encrypted connection storage reports **not configured**; Query's ordinary database store does not claim KEK protection. Gavel exposes its status at `GET /api/connections/storage`, using the same response shape, and a missing local macOS key can be initialized with:

```bash
gavel connections kek init
```
