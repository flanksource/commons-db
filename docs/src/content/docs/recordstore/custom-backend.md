---
title: Writing a backend
description: Implement recordstore.Backend and prove it with the shared Ginkgo conformance suite.
---

Before adding a backend, check whether an existing one fits. A new store is usually better served by a new `clicky/cache.Store` behind the `kv` backend. If you do need one, it must implement `recordstore.Backend` (see [Concepts](../concepts/#the-backend-interface)) and pass the conformance suite every in-tree backend runs.

## What a backend must do

- **Validate first.** Call `recordstore.ValidateAppend(stream, kind)` (or `ValidateStream` and `ValidateTTL`) before writing anything.
- **Resolve the kind** through the `SchemaResolver` it was given (`recordstore.ResolveKind`) and refuse a kind nobody declared.
- **Serialize writers per stream** with `recordstore.StreamLocks`.
- **Make appends all-or-nothing.** A refused append (`ErrCapacity`, duplicate key in one batch, missing key) writes none of its rows.
- **Deduplicate keyed kinds atomically** with the append: `KindSchema.RowKeys` + `recordstore.Unstored`.
- **Apply `RetainRows`** in the same write: trim rows older than the TTL and slide the stream's expiry (`KindSchema.RetentionTTL`).
- **Have a commit point.** Write rows first and metadata last, and treat anything outside the committed low/high seq as invisible and removable.
- **Start a new generation** (`recordstore.NewStreamMeta`) when a stream id is reused after expiry, restarting at seq 1.
- **Refuse appends to sealed streams** with `recordstore.RefuseSealed(meta)`.
- **Wrap the sentinel errors** (`ErrNotFound`, `ErrCapacity`, `ErrSealed`) so `errors.Is` matches.

## Running the conformance suite

`recordstore/recordstoretest` registers Ginkgo specs covering appends, refusals, concurrency, expiry, keys, trimming, retention, tailing, sealing and deletion. Call `Conformance` inside a `Describe` and return a fresh harness per spec:

```go
package mystore_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"

	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"example.com/acme/mystore"
)

var _ = Describe("mystore backend", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		clock := &fakeClock{now: time.Now()}
		backend, err := mystore.New(mystore.Options{
			Schema: recordstoretest.Schema, // resolves the suite's kinds, refuses others
			TTL:    recordstoretest.TTL,
			Now:    clock.Now,
		})
		Expect(err).ToNot(HaveOccurred())
		return recordstoretest.Harness{
			Backend: backend,
			Elapse:  func(d time.Duration) { clock.Advance(d); backend.Sweep() }, // move the clock and reap
			Advance: clock.Advance,                                             // move the clock, reap nothing
			Now:     clock.Now,
		}
	})
})
```

The suite declares three kinds over the same `recordstoretest.Columns` (`name`, `count`, `ok`, `detail`):

| Constant | Kind | Options |
| --- | --- | --- |
| `Kind` | `sample` | unkeyed, `RetainStream` |
| `KeyedKind` | `keyed` | key `name` |
| `RollingKind` | `rolling` | key `name`, `RetainRows` |

The helpers `SampleRow`, `SampleRows`, `Normalize` and `Scanned` are exported for backend-specific specs next to the suite. For an example, see `recordstore/kv/kv_test.go`, which runs the suite over both `cache.NewMemory()` and miniredis.

To serve a new backend's streams through `recordresults`, pass it as `OpenOptions.Source`. It's mirrored into a derived `index.sqlite` like kv and ndjson. `Results.Ref` reports a `StoreLocation` only for the three in-tree backends, and returns an error for any other.
