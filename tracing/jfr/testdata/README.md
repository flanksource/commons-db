# jfr decode fixtures

| Fixture | Recorded by | Events |
|---|---|---|
| `jdk-profile.jfr.gz` | JDK 21.0.5 Flight Recorder, `settings=profile` | cpu (`jdk.ExecutionSample`), alloc (`jdk.ObjectAllocationSample`), lock (`jdk.JavaMonitorEnter`), park (`jdk.ThreadPark`) |
| `async-profiler-cpu-lock-alloc.jfr.gz` | async-profiler 2.9, `event=cpu,alloc=0,lock=0` | cpu, alloc (`jdk.ObjectAllocationInNewTLAB` + `jdk.ObjectAllocationOutsideTLAB`), lock, park |
| `async-profiler-wall.jfr.gz` | async-profiler 4.0, `event=wall` | wall (`profiler.WallClockSample`) |

## Attribution

`async-profiler-cpu-lock-alloc.jfr.gz` and `async-profiler-wall.jfr.gz` are copied unchanged from [grafana/jfr-parser](https://github.com/grafana/jfr-parser) v0.19.0 (`parser/testdata/cortex-dev-01__kafka-0__cpu_lock0_alloc0__0.jfr.gz` and `parser/testdata/wall.jfr.gz`), licensed under the Apache License 2.0, a copy of which is in `LICENSE.grafana-jfr-parser`.

## Recording `jdk-profile.jfr.gz`

`gen/Workload.java` runs one thread burning CPU (`burner`), one allocating (`allocator`), two contending on a monitor (`contender-1`, `contender-2`) and one parking (`parker`) for two seconds. From this directory:

```sh
java -XX:StartFlightRecording=settings=profile,filename=jdk-profile.jfr gen/Workload.java 2000
gzip -9 jdk-profile.jfr
```

## Expectations

The counts, weights and instants the decode specs expect were read from each recording with the JDK's own `jfr` tool, not with the parser under test: `jfr summary <file>` for the event counts, and `jfr print --json --events <types> <file>` piped through jq for the summed weights (`weight`, `tlabSize` for in-TLAB and `allocationSize` for outside-TLAB allocations, `samples` for wall-clock samples), the summed durations, each event's end instant (`startTime` + `duration`), and the events ending after a chosen instant.
