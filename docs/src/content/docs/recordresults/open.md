---
title: Opening a result store
description: recordresults.Open wires a backend, a Notifier, a sqlite index and a profile registry from settings.
---

`github.com/flanksource/commons-db/cmd/query/recordresults` serves record streams through the profile engine: **one read-only `sql` profile per result type**, over a sqlite index, addressed by a `stream` param.

That design is deliberate. Every profile is a catalog entry, an OpenAPI path and a sidebar item, so a profile per stream would grow all three with every capture. A stream param grows none of them. Paging, column filters, filter-value lookups, sorting and export all come from the profile engine and run as SQL against the index.

## Open

```go
results, err := recordresults.Open(recordresults.OpenOptions{
	Prefix:         "traces", // profiles are traces/<kind>
	ConnectionName: "index",  // connection://traces/index
	Settings:       settings, // Dir, TTL, and Backend when there is no Source
	Source:         nil,      // or a recordstore.Router
	Schemas:        nil,      // or the catalog Source's backends resolve through
	Register: func(registry *recordresults.Registry) error {
		return recordresults.RegisterResultType(registry, recordresults.ResultType[QueryEvent]{
			Kind: "query_event", Title: "Query events", TimeColumn: "at",
		})
	},
})
if err != nil {
	return err
}
defer results.Close()
```

| Option | Required | Meaning |
| --- | --- | --- |
| `Prefix` | yes | one path segment (`[A-Za-z0-9][A-Za-z0-9._-]{0,62}`). Profiles are `<prefix>/<kind>`. |
| `ConnectionName` | yes | one segment. The index is the virtual, read-only connection `connection://<prefix>/<name>`. |
| `Settings.Dir` | yes | where `records.sqlite`, `index.sqlite` and `ndjson/` live. Created if missing. |
| `Settings.TTL` | yes, > 0 | how long a stream is kept |
| `Settings.Backend` | without `Source` | `sqlite` or `ndjson`. Resolve it first with `Settings.Resolve`. `kv` is refused here, because kv is routed per caller and must be passed as `Source`. |
| `Source` | no | a backend you route yourself, usually a `recordstore.Router` over kv per tenant. `Open` owns it: `Close`, or a failed `Open`, closes it. |
| `Schemas` | no | pass the catalog `Source`'s backends resolve kinds through. `nil` makes a fresh one. |
| `Register` | yes | declares the result types the registry serves |

## What gets opened

| `Source` | `Settings.Backend` | streams are written to | profiles read |
| --- | --- | --- | --- |
| `nil` | `sqlite` | `<dir>/v4/records.sqlite` | the same file |
| `nil` | `ndjson` | `<dir>/ndjson/<kind>/<stream>.ndjson` | `<dir>/v4/index.sqlite`, derived |
| a Router, or any backend | (any) | the Source | `<dir>/v4/index.sqlite`, derived |

A separate index is always **derived**. It's never a file a route could also write, so one route's streams can't be read through another. A derived index keeps no TTL of its own: the Indexer gives each indexed stream its source's exact expiry. Both sqlite files sweep expired streams every 10 minutes.

The `v4/` directory is the sqlite catalog version. See [The sqlite record file](../../sqlite/record-file/#versioned-file-paths).

## Results

```go
type Results struct {
	Backend  *recordstore.Notifier // append here: it wakes every follower
	Registry *Registry             // serves the result types
}
```

- **`results.Backend`**: every capture appends through it. `recordstore.AppendTyped(ctx, results.Backend, stream, kind, items)` is the usual call. It's a full `recordstore.Backend`, so `Seal`, `Trim`, `Expire`, `Meta`, `Scan` and `Tail` are available too.
- **`results.Registry`**: plug it into the profile service. See [Serving over HTTP](../serving/).
- **`results.Ref(ctx, stream, from, to)`**: describes a window of a stream for another process. See [Stream refs](../stream-refs/).
- **`results.DeleteStream(ctx, stream, kind)`**: removes a stream from its source and from the derived index, and refuses if the stream holds another kind. Deleting a stream that doesn't exist is not an error.
- **`results.Close()`**: closes the registry, the files and the Source, in reverse order of opening.

## Building the pieces yourself

`Open` is a convenience over public constructors. When you need a different arrangement, for example an index at a path you choose, build it directly:

```go
schemas := recordstore.NewSchemas()
index, err := sqlite.Open(sqlite.Options{
	Path: filepath.Join(dir, "index.sqlite"), Schema: schemas.Kind, Derived: true, SweepInterval: time.Minute,
})
source, err := kv.New(kv.Options{Store: store, Prefix: "records", Schema: schemas.Kind, TTL: ttl, MaxChunkBytes: 1 << 20})
notifier, err := recordstore.NewNotifier(source, recordstore.NotifierOptions{RecheckInterval: recordresults.FollowRecheckInterval})
registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
	Prefix: "traces", Schemas: schemas, Index: index, Source: notifier, ConnectionName: "index",
})
```

`NewRegistry` builds the `Indexer` itself, so preparing a read and serving it can never name different indexes. A `Source` that is a `*recordstore.Notifier` is unwrapped for indexing and registered for follows. Result types with `Follow: true` require one.
