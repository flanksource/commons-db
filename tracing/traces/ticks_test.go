// A minimal trace kind the specs register: it emits numbered ticks, so every
// part of the plugin contract can be exercised without an external source.

package traces_test

import (
	"errors"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
)

type tickParams struct {
	Count int    `json:"count"`
	Label string `json:"label,omitempty"`
}

func (p tickParams) Validate() error {
	if p.Count < 0 {
		return errors.New("count must not be negative")
	}
	return nil
}

type tick struct {
	At    time.Time `json:"at" pretty:"label=At"`
	N     int       `json:"n"`
	Label string    `json:"label"`
}

type ticker struct{}

func (ticker) Params() tickParams { return tickParams{Count: 3} }

func (ticker) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at"}
}

func (ticker) Handle(dbcontext.Context, tickParams, traces.Emitter[tick], traces.Records[tick]) error {
	return nil
}

func tickPlugin() *traces.Handler[tickParams, tick] {
	return traces.NewHandler[tickParams, tick](ticker{}, traces.Capabilities{Live: true})
}
