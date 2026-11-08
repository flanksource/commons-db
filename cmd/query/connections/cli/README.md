# Connection management

`query connections` manages reusable Connection files by default. New connections are saved under `$XDG_CONFIG_HOME/flanksource/connections`, or `~/.config/flanksource/connections` when XDG_CONFIG_HOME is unset. Listing reads that directory, `<config-dir>/connections`, and `./.connections`, with later layers overriding earlier definitions of the same namespace/name. Editing and deleting operate on the effective connection's original file. Removing an override can reveal a lower-priority connection.

```sh
query connections add postgres --name orders --namespace shop --host localhost:5432 --database orders --username tester --password 'secret://orders/password'
query connections add sqlite --name local --url file:records.db
query connections add http --name service -i
query connections list --type postgres --format json
query connections edit connection://shop/orders --url postgres://localhost:5432/orders
query connections edit local -i
query connections validate local
query connections delete local
query connections list --source=db --db embedded
```

`--source` accepts `file` (default) or `db`. File mode never opens the configured PostgreSQL database or reads profiles. Database mode uses `--db`, `QUERY_DB_URL`, or the default embedded Query database and does not include file connections. HTTP routes and the web app retain their existing persistence behavior.

`add` takes a connection type subcommand and `--name`; `--namespace` is optional. Provider flags follow Incident Commander's connection commands: SQL supports URL or host/database fields, cloud connections expose their credential fields, and HTTP supports basic, bearer, OAuth, and TLS credentials. `--properties key=value` adds provider-specific properties. `edit` applies supplied fields and preserves other fields and stored credentials. References accept IDs, unique names, and namespace/name or connection://namespace/name.

Use `-i` or `--interactive` for Clicky's terminal/dashboard forms. Add forms are seeded from supplied flags; edit forms load current values. Cancellation does not save. Literal passwords and certificates are omitted from responses; credential references remain stored for hydration at use time.

`validate` resolves credentials and performs a fresh read-only provider probe. It returns a nonzero exit on invalid configuration, missing references, authentication failures, unreachable targets, or unsupported checks. SQLite opens in read-only mode and must already exist. Unsupported connection types report an error; validation never sends notifications or writes artifacts. Listing never contacts connection targets.
