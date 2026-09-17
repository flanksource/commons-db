package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// overlay derives what this process knows about each record: whether it is
// live here, whether this process writes it, whether its owner has gone quiet,
// whether its events can be read and whether it can be restarted, and which
// sessions were restarted from it — only those allow lets the caller read.
func (h *sessionHandler) overlay(ctx context.Context, records []query.SessionRecord, allow func(string) bool) ([]query.SessionInfo, error) {
	ids := make([]string, len(records))
	for i, rec := range records {
		ids[i] = rec.ID
	}
	lineage, err := h.lineage(ctx, ids)
	if err != nil {
		return nil, err
	}
	if lineage, err = h.readableLineage(ctx, lineage, records, allow); err != nil {
		return nil, err
	}
	now, boot, staleAfter := time.Now(), h.registry.Owner().Boot, h.registry.StaleAfter()
	infos := make([]query.SessionInfo, len(records))
	for i, rec := range records {
		_, live := h.registry.Get(rec.ID)
		terminal := rec.State.Terminal()
		info := query.SessionInfo{
			SessionRecord: rec,
			Controllable:  live && !terminal,
			LocalWriter:   rec.Owner.Boot == boot,
			Unresponsive:  !live && !terminal && now.Sub(rec.HeartbeatAt) > staleAfter,
			Restartable:   terminal && h.registry.Restartable(rec),
			RestartedAs:   lineage[rec.ID],
		}
		if info.EventsAvailable, err = h.eventsAvailable(ctx, rec, live); err != nil {
			return nil, err
		}
		infos[i] = info
	}
	return infos, nil
}

// eventsAvailable reports whether this process can serve rec's events: the
// generation of its record stream its ref names still exists in the record
// store, or — for a stream session that recorded none — its ring is live here
// or the event log holds events for it.
func (h *sessionHandler) eventsAvailable(ctx context.Context, rec query.SessionRecord, live bool) (bool, error) {
	switch {
	case rec.Events == nil:
		return h.streamEventsAvailable(ctx, rec, live)
	case h.records == nil:
		return false, nil
	}
	meta, err := h.records.Meta(ctx, rec.Events.Stream)
	switch {
	case errors.Is(err, recordstore.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("session %s: read record stream %q: %w", rec.ID, rec.Events.Stream, err)
	}
	return meta.Generation == rec.Events.Generation, nil
}

// streamEventsAvailable is eventsAvailable for a record that names no record
// stream: a capture's are nowhere, a live stream session's are in its ring, and
// an ended one's are wherever the event log says.
func (h *sessionHandler) streamEventsAvailable(ctx context.Context, rec query.SessionRecord, live bool) (bool, error) {
	switch {
	case rec.Kind == query.KindCapture:
		return false, nil
	case live:
		return true, nil
	case h.eventLog == nil:
		return false, nil
	}
	held, err := h.eventLog.HasEvents(ctx, rec.ID)
	if err != nil {
		return false, fmt.Errorf("session %s: read event log: %w", rec.ID, err)
	}
	return held, nil
}

// lineage maps ids to the sessions restarted from them: from the store when
// there is one, which holds every capture, otherwise from the registry.
func (h *sessionHandler) lineage(ctx context.Context, ids []string) (map[string][]string, error) {
	if h.sessions != nil {
		lineage, err := h.sessions.Lineage(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("session lineage: %w", err)
		}
		return lineage, nil
	}
	return lineageOf(h.liveRecords(), ids), nil
}

// readableLineage drops the restarted sessions allow refuses, so restartedAs
// never names a session the caller could not list. A nil allow allows all.
func (h *sessionHandler) readableLineage(ctx context.Context, lineage map[string][]string, known []query.SessionRecord, allow func(string) bool) (map[string][]string, error) {
	if allow == nil {
		return lineage, nil
	}
	profiles := map[string]string{}
	for _, rec := range append(h.liveRecords(), known...) {
		profiles[rec.ID] = rec.Profile
	}
	readable := make(map[string][]string, len(lineage))
	for parent, children := range lineage {
		for _, child := range children {
			profile, ok := profiles[child]
			if !ok && h.sessions == nil {
				return nil, fmt.Errorf("session lineage: restarted session %s is not in the registry that named it", child)
			}
			if !ok {
				rec, found, err := h.sessions.Get(ctx, child)
				if err != nil {
					return nil, fmt.Errorf("session lineage: read restarted session %s: %w", child, err)
				}
				if !found {
					continue // gone since the lineage was read
				}
				profile = rec.Profile
			}
			if allow(profile) {
				readable[parent] = append(readable[parent], child)
			}
		}
	}
	return readable, nil
}

// liveRecords is every session the registry holds, oldest first.
func (h *sessionHandler) liveRecords() []query.SessionRecord {
	infos := h.registry.List()
	records := make([]query.SessionRecord, len(infos))
	for i, info := range infos {
		records[i] = info.SessionRecord
	}
	return records
}
