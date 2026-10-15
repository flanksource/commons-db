# Build, lint and test the tracing module: trace plugins whose captures run as
# managed sessions and commit their records to a record store, their kinds,
# and the SQL Server capture packages under sqlserver/.
.PHONY: build lint test jfr-record jfr-fixture jfr-compression

build:
	go build ./...

lint:
	go vet ./...

test:
	go test ./...

# A JDK Flight Recording of jfr/testdata/gen/Workload.java. It goes to .tmp by
# default: the decode specs expect the counts of the committed fixture, so a
# new recording replaces jdk-profile.jfr.gz only along with those expectations.
JFR_OUT ?= $(abspath ../.tmp/jfr/jdk-profile.jfr)
JFR_SETTINGS ?= profile
JFR_MILLIS ?= 2000
JFR_CLASSES := $(abspath ../.tmp/jfr/classes)

.DELETE_ON_ERROR:

# Compiled first: since JDK 22 the source launcher refuses gen/Workload.java,
# whose path does not match its package.
$(JFR_OUT): jfr/testdata/gen/Workload.java
	mkdir -p $(dir $(JFR_OUT))
	javac -d $(JFR_CLASSES) jfr/testdata/gen/Workload.java
	java -XX:StartFlightRecording=settings=$(JFR_SETTINGS),filename=$(JFR_OUT) -cp $(JFR_CLASSES) jfrfixture.Workload $(JFR_MILLIS)
	ls -l $(JFR_OUT)

# Records again even when JFR_OUT exists.
jfr-record:
	rm -f $(JFR_OUT)
	$(MAKE) $(JFR_OUT)

# Writes JFR_OUT to the jdk-profile.jfr.gz fixture, scrubbed down to the events
# the decoder reads: the GC, flag and environment events a profile recording is
# mostly made of would only bloat the repository. The kept events, and so the
# decode specs' expectations, are unchanged by the scrub.
JFR_DECODED_EVENTS := jdk.ExecutionSample,jdk.ObjectAllocationSample,jdk.ObjectAllocationInNewTLAB,jdk.ObjectAllocationOutsideTLAB,jdk.JavaMonitorEnter,jdk.ThreadPark,jdk.ActiveSetting
JFR_FIXTURE := jfr/testdata/jdk-profile.jfr.gz
JFR_SCRUBBED := $(basename $(JFR_OUT)).scrubbed.jfr

jfr-fixture: $(JFR_OUT)
	rm -f $(JFR_SCRUBBED)
	jfr scrub --include-events $(JFR_DECODED_EVENTS) $(JFR_OUT) $(JFR_SCRUBBED)
	gzip -9 -n -c $(JFR_SCRUBBED) > $(JFR_FIXTURE)
	ls -l $(JFR_FIXTURE)

# Compares codecs on JFR_FILE (the recording above unless set; .jfr or .jfr.gz)
# by size, ratio and throughput. JFR_FILE= benchmarks the testdata fixtures.
JFR_FILE ?= $(JFR_OUT)
JFR_BENCHTIME ?= 5x

jfr-compression: $(if $(filter $(JFR_OUT),$(JFR_FILE)),$(JFR_OUT))
	JFR_FILE=$(if $(JFR_FILE),$(abspath $(JFR_FILE))) go test ./jfr -run '^$$' -bench BenchmarkCompression -benchtime $(JFR_BENCHTIME)
