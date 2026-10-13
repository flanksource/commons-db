# jfr decode fixtures

| Fixture | Recorded by | Events |
|---|---|---|
| `jdk-profile.jfr.gz` | JDK 21.0.5 Flight Recorder, `settings=profile`, scrubbed to the decoded events | cpu (`jdk.ExecutionSample`), alloc (`jdk.ObjectAllocationSample`), lock (`jdk.JavaMonitorEnter`), park (`jdk.ThreadPark`) |
| `async-profiler-cpu-lock-alloc.jfr.gz` | async-profiler 2.9, `event=cpu,alloc=0,lock=0` | cpu, alloc (`jdk.ObjectAllocationInNewTLAB` + `jdk.ObjectAllocationOutsideTLAB`), lock, park |
| `async-profiler-wall.jfr.gz` | async-profiler 4.0, `event=wall` | wall (`profiler.WallClockSample`) |

## Attribution

`async-profiler-cpu-lock-alloc.jfr.gz` and `async-profiler-wall.jfr.gz` are copied unchanged from [grafana/jfr-parser](https://github.com/grafana/jfr-parser) v0.19.0 (`parser/testdata/cortex-dev-01__kafka-0__cpu_lock0_alloc0__0.jfr.gz` and `parser/testdata/wall.jfr.gz`), licensed under the Apache License 2.0, a copy of which is in `LICENSE.grafana-jfr-parser`.

## Recording `jdk-profile.jfr.gz`

`gen/Workload.java` runs one thread burning CPU (`burner`), one allocating (`allocator`), two contending on a monitor (`contender-1`, `contender-2`) and one parking (`parker`) for two seconds. `make -C tracing jfr-record` (or `task tracing:jfr-record`) compiles it and records it into `.tmp/jfr/jdk-profile.jfr`; `JFR_SETTINGS`, `JFR_MILLIS` and `JFR_OUT` override the settings, duration and output.

`make -C tracing jfr-fixture` (or `task tracing:jfr-fixture`) writes that recording to the fixture, recording it first unless it exists. `jfr scrub --include-events` keeps only the events the decoder reads (the four above, the TLAB allocation events and `jdk.ActiveSetting`) with the constant pools they reference, then `gzip -9` compresses it. A `settings=profile` recording is mostly GC, flag and environment events: the fixture went from 1.2 MB to 61 KB scrubbed, with every kept event, and so every expectation below, unchanged. A new recording replaces the fixture only together with those expectations, which are this recording's counts.

## Compression

`make -C tracing jfr-compression` records the workload unless `.tmp/jfr/jdk-profile.jfr` exists, then runs `BenchmarkCompression` on it: per codec (gzip, zstd, brotli, s2, lz4, xz at several levels) the compressed size and ratio, and compress and decompress throughput. `JFR_FILE=<path>` benchmarks another recording, `.jfr` or `.jfr.gz`; `JFR_FILE=` benchmarks every fixture in this directory. `JFR_BENCHTIME` sets the iterations (default `5x`).

## Expectations

The counts, weights and instants the decode specs expect were read from each recording with the JDK's own `jfr` tool, not with the parser under test: `jfr summary <file>` for the event counts, and `jfr print --json --events <types> <file>` piped through jq for the summed weights (`weight`, `tlabSize` for in-TLAB and `allocationSize` for outside-TLAB allocations, `samples` for wall-clock samples), the summed durations, each event's end instant (`startTime` + `duration`), and the events ending after a chosen instant.
