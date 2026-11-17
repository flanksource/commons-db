package profiles

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
)

// profileFilters fronts clicky's filter registry, which is process-global and
// refuses a name twice. Every command-tree build creates its own Service and
// registers each profile's filters again, so an identical definition rebinds
// the registered filter to the newest Service's source, while a different
// definition under the same name is a conflict.
var profileFilters = struct {
	sync.Mutex
	byName map[string]*reboundFilterSource
}{byName: map[string]*reboundFilterSource{}}

// reboundFilterSource is the Source clicky holds for a profile filter: it
// answers through whichever source the latest identical registration supplied.
type reboundFilterSource struct {
	definition entity.NamedFilter
	mu         sync.RWMutex
	current    entity.FilterSource
}

// registerProfileFilters registers a profile's filters all together or, when
// any one conflicts, none of them.
func registerProfileFilters(filters []entity.NamedFilter) error {
	profileFilters.Lock()
	defer profileFilters.Unlock()
	batch := make(map[string]bool, len(filters))
	for _, filter := range filters {
		if batch[filter.Name] {
			return fmt.Errorf("profile filter %q is declared twice", filter.Name)
		}
		batch[filter.Name] = true
		if err := profileFilterConflict(filter); err != nil {
			return err
		}
	}
	for _, filter := range filters {
		if existing, ok := profileFilters.byName[filter.Name]; ok {
			existing.rebind(filter.Source)
			continue
		}
		rebound := &reboundFilterSource{definition: filter, current: filter.Source}
		registered := filter
		registered.Source = rebound
		entity.RegisterFilter(registered)
		profileFilters.byName[filter.Name] = rebound
	}
	return nil
}

func profileFilterConflict(filter entity.NamedFilter) error {
	if existing, ok := profileFilters.byName[filter.Name]; ok {
		if !sameProfileFilter(existing.definition, filter) {
			return fmt.Errorf("profile filter %q is already registered with a different definition", filter.Name)
		}
		return nil
	}
	if _, exists := entity.GetFilter(filter.Name); exists {
		return fmt.Errorf("profile filter %q is already registered", filter.Name)
	}
	return nil
}

func sameProfileFilter(a, b entity.NamedFilter) bool {
	sourceA, sourceB := a.Source, b.Source
	a.Source, b.Source = nil, nil
	if a != b {
		return false
	}
	lookupA, isLookupA := sourceA.(profileFilterSource)
	lookupB, isLookupB := sourceB.(profileFilterSource)
	if isLookupA || isLookupB {
		return isLookupA && isLookupB && lookupA.profileName == lookupB.profileName && lookupA.key == lookupB.key
	}
	return reflect.DeepEqual(sourceA, sourceB)
}

func (r *reboundFilterSource) rebind(source entity.FilterSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = source
}

func (r *reboundFilterSource) source() entity.FilterSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

func (r *reboundFilterSource) Options(fc entity.FilterContext, search string, limit int) (map[string]api.Textable, int, error) {
	return r.source().Options(fc, search, limit) //nolint:staticcheck // SA1019: forwards the deprecated half of the interface it implements
}

func (r *reboundFilterSource) CountedOptions(fc entity.FilterContext, search string, limit int) (entity.FilterOptions, error) {
	return countedFilterOptions(r.source(), fc, search, limit)
}

func (r *reboundFilterSource) Resolve(fc entity.FilterContext, values []string) (map[string]api.Textable, error) {
	return r.source().Resolve(fc, values)
}
