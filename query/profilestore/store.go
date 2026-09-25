// Package profilestore holds the contracts a profile store and a profile read
// hook satisfy, shared by the profile service and the data sources it serves.
package profilestore

import (
	"context"

	"github.com/flanksource/commons-db/query"
)

type Store interface {
	List(context.Context) ([]query.Profile, error)
	Get(context.Context, string) (query.Profile, error)
	Save(context.Context, query.Profile) error
	Update(context.Context, string, query.Profile, UpdateOptions) error
	Delete(context.Context, string) error
}

type VirtualStore interface {
	Store
	Peek(context.Context, string) (query.Profile, error)
	IsVirtual(string) bool
}

type UpdateOptions struct {
	ReplaceExisting bool
}
