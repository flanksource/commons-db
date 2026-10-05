// A minimal trace kind written outside this package: its params, its record
// schema, and a capture that emits until stopped, registered on a catalog.

package traces_test

import (
	"fmt"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
)

// HeartbeatParams is the form a client fills to start a heartbeat capture.
type HeartbeatParams struct {
	Every string `json:"every,omitempty" clicky:"title=Every"`
}

// Heartbeat is one record a heartbeat capture stores.
type Heartbeat struct {
	At   time.Time `json:"at" pretty:"label=At"`
	Beat int       `json:"beat"`
}

type heartbeats struct{}

func (heartbeats) Params() HeartbeatParams { return HeartbeatParams{Every: "1s"} }

func (heartbeats) Schema() recordresults.ResultType[Heartbeat] {
	return recordresults.ResultType[Heartbeat]{Title: "Heartbeats", TimeColumn: "at"}
}

func (heartbeats) Handle(ctx dbcontext.Context, params HeartbeatParams, records traces.Emitter[Heartbeat], _ traces.Records[Heartbeat]) error {
	every, err := time.ParseDuration(params.Every)
	if err != nil {
		return err
	}
	for beat := 1; ; beat++ {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
		if err := records.Emit(ctx, Heartbeat{At: time.Now().UTC(), Beat: beat}); err != nil {
			return err
		}
	}
}

func ExampleNewHandler() {
	kinds := traces.NewKinds()
	if err := kinds.RegisterKind("heartbeat", traces.NewHandler[HeartbeatParams, Heartbeat](heartbeats{}, traces.Capabilities{Live: true})); err != nil {
		panic(err)
	}
	infos, err := kinds.List()
	if err != nil {
		panic(err)
	}
	for _, info := range infos {
		fmt.Println(info.Name, info.Title, info.Params.Properties["every"].Default, len(info.Columns))
	}
	// Output: heartbeat Heartbeats 1s 2
}
