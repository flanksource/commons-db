# Completed samples

`Writer` commits an unkeyed stream's rows and completion metadata together through `BatchAppender`. A successful empty sample is a real frame. Invalid counts, time bounds, row validation failures and atomic write failures return an error and publish no completion metadata. The writer requires exclusive ownership of the primary stream; process collectors that already have an atomic batch should append a `Frame` to that same batch instead.

```go
writer := sampleframe.Writer{Backend: backend, Schema: schema, Stream: stream}
frame, err := writer.Append(ctx, sampleframe.Frame{
    Source: "worker-1/heap",
    StartedAt: started,
    CompletedAt: completed,
    Observed: len(observed),
    Limit: captureLimit,
}, retainedRows)
if err != nil {
    return err
}
```

`Source` identifies one endpoint or process/profile lifetime. `StartedAt` and `CompletedAt` describe the collection interval. `Observed` counts rows before capture truncation and `Limit` is that capture's cap, with zero meaning unlimited. `Summary` carries producer metadata such as original query columns and ordering. The writer sets `ID`, `AfterSeq`, `ToSeq` and `Retained`; readers select a frame before applying viewer filters. Frame membership is the primary stream's sequence window `(AfterSeq, ToSeq]`, including when both bounds are equal. Metadata is stored in `<stream>-frames`. Call `Close` after the final checkpoint to seal completion metadata without inventing a sample.

`BuildHierarchy` accepts root-first stacks with exclusive numeric leaf weights, including fractional process CPU. It computes cumulative totals, preserves recursion, rejects invalid weights, and gives nodes stable identities by their complete path. Optional `IDs` distinguish ownership nodes with identical labels; when supplied they must match `Frames` one for one. JSON numbers have the precision of the consuming renderer; native record values remain available separately for exact exports and filters.

From the `recordstore` module:

```sh
go test ./sampleframe ./recordresults
```

The tests use isolated SQLite stores and cover empty frames, atomic validation failure, identical timestamps, sealing and hierarchy conservation.
