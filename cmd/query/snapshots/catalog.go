package snapshots

import (
	"context"
	"fmt"
	"sort"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

func (m *Manager) List(context.Context) ([]query.Profile, error) {
	m.prune()
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]query.Profile, 0, len(m.profiles))
	for name, id := range m.profiles {
		if item := m.items[id]; item != nil {
			profile := item.profiles[name].profile
			profile.ExpiresAt = ptrTime(item.lastAccessed.Add(item.age))
			items = append(items, profile)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (m *Manager) Get(_ context.Context, name string) (query.Profile, error) {
	return m.profile(name, true)
}

func (m *Manager) Peek(_ context.Context, name string) (query.Profile, error) {
	return m.profile(name, false)
}

func (m *Manager) profile(name string, touch bool) (query.Profile, error) {
	m.mu.RLock()
	id := m.profiles[name]
	_, expired := m.expired[name]
	m.mu.RUnlock()
	if id == "" && expired {
		return query.Profile{}, ErrExpired
	}
	item, err := m.snapshot(id, touch)
	if err != nil {
		return query.Profile{}, err
	}
	profile, found := item.profiles[name]
	if !found {
		return query.Profile{}, fmt.Errorf("profile %q not found", name)
	}
	resolved := profile.profile
	resolved.ExpiresAt = ptrTime(item.lastAccessed.Add(item.age))
	return resolved, nil
}

func (m *Manager) Save(context.Context, query.Profile) error {
	return fmt.Errorf("virtual profiles are read-only")
}

func (m *Manager) Update(context.Context, string, query.Profile, profiles.UpdateOptions) error {
	return fmt.Errorf("virtual profiles are read-only")
}

func (m *Manager) Delete(context.Context, string) error {
	return fmt.Errorf("virtual profiles are read-only")
}

func (m *Manager) IsVirtual(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, found := m.profiles[name]
	_, wasVirtual := m.expired[name]
	return found || wasVirtual
}

func (m *Manager) ListConnections() []*models.Connection {
	m.prune()
	m.mu.RLock()
	defer m.mu.RUnlock()
	connections := make([]*models.Connection, 0, len(m.items))
	for _, item := range m.items {
		connection := item.connection
		connection.ExpiresAt = ptrTime(item.lastAccessed.Add(item.age))
		connections = append(connections, &connection)
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].Name < connections[j].Name })
	return connections
}

func (m *Manager) ResolveConnection(reference string) (*models.Connection, error) {
	m.mu.RLock()
	_, expired := m.expired[reference]
	var id string
	for snapshotID, item := range m.items {
		if reference == item.connection.ID.String() || reference == item.connection.Name ||
			reference == "connection://"+item.connection.Name ||
			reference == "connection://"+item.connection.Namespace+"/"+item.connection.Name {
			id = snapshotID
			break
		}
	}
	m.mu.RUnlock()
	if id == "" {
		if expired {
			return nil, ErrExpired
		}
		return nil, nil
	}
	item, err := m.snapshot(id, true)
	if err != nil {
		return nil, err
	}
	connection := item.connection
	connection.ExpiresAt = ptrTime(item.lastAccessed.Add(item.age))
	return &connection, nil
}

func (m *Manager) AcquireConnection(reference string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.connectionLocked(reference)
	if item == nil {
		if _, found := m.expired[reference]; found {
			return nil, ErrExpired
		}
		return nil, nil
	}
	return m.acquireLocked(item)
}
