---
title: "Tutorial: capture and serve a stream"
description: Declare a result type, append a stream to a local store, and page, filter and follow it over HTTP.
---

This tutorial builds the smallest application that captures rows and serves them back. An app called `acme` records the SQL statements a job runs and serves them as the `traces/query_event` profile.

You'll:

1. declare a row type
2. read the store settings and open a result store
3. append a stream and seal it
4. mount the profile service and read the stream over HTTP

It assumes both modules are in your `go.mod` together with the sqlite `replace` (see [Consuming commons-db](../consuming/)).

## 1. Declare the row type

A result type is a Go struct. Its JSON encoding is the row. `query.ColumnsFor` reflects the `json` and `pretty` tags into the profile's columns, the same tags clicky reads.

```go
package traces

import "time"

type QueryEvent struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at" pretty:"label=Captured"`
	Database  string    `json:"db"`
	User      string    `json:"user"`
	ElapsedMs float64   `json:"elapsed_ms" pretty:"type=duration,unit=ms"`
	Statement string    `json:"statement"`
	Tables    []string  `json:"tables"`
}
```

A `[]string` field becomes a JSON column that is filtered as an array, with no tag needed. `seq` and `stream_id` are reserved: every stream table already has them.

## 2. Open the result store

```go
package traces

import (
	"time"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/recordstore"
)

const QueryEventKind = "query_event"

func Open() (*recordresults.Results, error) {
	settings, err := recordstore.ReadSettings("trace.store", recordstore.Settings{
		Dir:               ".acme/traces",
		TTL:               7 * 24 * time.Hour,
		NDJSONMaxBytes:    256 << 20,
		NDJSONKeepStreams: 20,
	})
	if err != nil {
		return nil, err
	}
	// No kv store in this app: an unset backend resolves to the local sqlite file.
	if settings.Backend, err = settings.Resolve(false, recordstore.BackendSQLite); err != nil {
		return nil, err
	}
	return recordresults.Open(recordresults.OpenOptions{
		Prefix:         "traces",
		ConnectionName: "index",
		Settings:       settings,
		Register: func(registry *recordresults.Registry) error {
			return recordresults.RegisterResultType(registry, recordresults.ResultType[QueryEvent]{
				Kind:          QueryEventKind,
				Title:         "Query events",
				TimeColumn:    "at",
				KeyColumn:     "id",
				SearchColumns: []string{"statement"},
				Follow:        true,
			})
		},
	})
}
```

With `trace.store.backend` unset, this opens `.acme/traces/v4/records.sqlite`, a durable file that is also the index profiles read. Running with `-P trace.store.backend=ndjson` writes NDJSON files instead and keeps a derived `index.sqlite` next to them. The application code stays the same.

## 3. Append and seal a stream

`results.Backend` is a `*recordstore.Notifier`. Appending through it wakes every session that follows the stream.

```go
ctx := context.Background()
results, err := traces.Open()
if err != nil {
	return err
}
defer results.Close()

stream := "job-" + jobID // 1-200 chars of A-Z a-z 0-9 . _ : -
for batch := range capture(ctx) {
	appended, err := recordstore.AppendTyped(ctx, results.Backend, stream, traces.QueryEventKind, batch)
	if err != nil {
		return err // ErrCapacity and ErrSealed are loud: nothing from the batch was written
	}
	log.Printf("stored seqs %d-%d, skipped %d already-stored ids",
		appended.Window.From, appended.Window.To, appended.Skipped)
}
// The capture is complete: followers finish instead of waiting for more rows.
if err := results.Backend.Seal(ctx, stream); err != nil {
	return err
}
```

Because the kind declares `KeyColumn: "id"`, re-sending an overlapping batch is idempotent: rows whose `id` the stream already holds are skipped and counted in `Skipped`.

## 4. Serve the profile

The registry is a read-only virtual profile store. Layer it over your own profile store, let the query context resolve the index connection, and run `BeforeExecute` before every read so the index is caught up and the stream checked.

```go
import (
	"net/http"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
)

func Handler(results *recordresults.Results, profileDir string, next http.Handler) (http.Handler, error) {
	base, err := profiles.NewFileStore(profileDir)
	if err != nil {
		return nil, err
	}
	store, err := profiles.NewOverlayStore(base, results.Registry)
	if err != nil {
		return nil, err
	}
	queryCtx := dbcontext.New().WithConnectionResolver(results.Registry.ResolveConnection)
	service, err := profiles.New(profiles.Options{
		Store:         func() (profiles.Store, error) { return store, nil },
		Context:       func() dbcontext.Context { return queryCtx },
		DecodeBody:    profiles.DecodeRequestBody,
		BeforeExecute: results.Registry.BeforeExecute,
	})
	if err != nil {
		return nil, err
	}
	service.RegisterFamily()
	return service.Handler("/api/v1", next)
}
```

`next` handles everything the profile service doesn't. `cmd/query` passes a clicky `rpc` swagger server, which serves the profile schema (`Accept: application/json+clicky`), filter-value lookups and OpenAPI. See [Serving over HTTP](../../recordresults/serving/) for that setup and for follow sessions.

## 5. Read it

The profile's name is `traces/query_event`. In a URL it is one escaped path segment:

```bash
BASE=http://localhost:8080/api/v1/profile/traces%2Fquery_event

# first page, newest first because the type has a TimeColumn
curl -H 'Accept: application/json' "$BASE?stream=job-42"

# a time window, a column filter and a search
curl -H 'Accept: application/json' \
  "$BASE?stream=job-42&from=now-1h&filter.user=!batch&q=policy&limit=50"

# only the rows after seq 1200 (resume where a reader left off)
curl -H 'Accept: application/json' "$BASE?stream=job-42&afterSeq=1200"

# the whole stream as CSV
curl "$BASE?stream=job-42&scope=all&format=csv" -o job-42.csv
```

The totals come back in headers: `X-Total-Count`, `X-Has-More` and `X-Next-Cursor`. A stream that doesn't exist, or that holds another kind, gets a not-found error, never an empty page.

## Next steps

- Share a kv store between processes: [Settings and routing](../../recordstore/settings/)
- Add aggregate views: [Views](../../recordresults/views/)
- Hand the stream to another process as a reference instead of its rows: [Stream refs](../../recordresults/stream-refs/)
