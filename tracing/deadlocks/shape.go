package deadlocks

import (
	"slices"
	"sort"
	"strings"
)

// Shape names a recurring kind of cycle. The two named shapes are the ones a
// hot table read through a nonclustered index and updated in place produces;
// everything else is Other, and its Signature says what collided.
type Shape string

const (
	// ShapeScanVsWriter: a reader locks whole pages of a clustered index as it
	// scans, and a writer needs two of those pages — which happens when an
	// update moves the row because it changed a clustered-key column.
	ShapeScanVsWriter Shape = "scan-vs-writer"
	// ShapeKeyLookupVsWriter: a reader found the row through a nonclustered
	// index and waits to read the rest of it from the clustered index, while a
	// writer holds the clustered row and waits to update the reader's
	// nonclustered entry.
	ShapeKeyLookupVsWriter Shape = "key-lookup-vs-writer"
	ShapeOther             Shape = "other"
)

// Shapes lists every shape, in the order a reader is shown them.
var Shapes = []Shape{ShapeScanVsWriter, ShapeKeyLookupVsWriter, ShapeOther}

// Label is the shape as a reader sees it.
func (s Shape) Label() string {
	switch s {
	case ShapeScanVsWriter:
		return "Scan vs writer"
	case ShapeKeyLookupVsWriter:
		return "Key lookup vs writer"
	case ShapeOther:
		return "Other"
	}
	return string(s)
}

// Analyze derives a deadlock's objects, indexes, lock types, signature and
// shape from its resources. Call it after Resolve: page locks name their
// index, and every lock its index type, only once the catalog has been read.
func Analyze(d *Deadlock) {
	d.Objects = distinct(d.Resources, func(r Resource) string { return r.Object })
	d.Indexes = distinct(d.Resources, func(r Resource) string { return r.Index })
	d.LockTypes = distinct(d.Resources, func(r Resource) string { return r.Kind })
	d.Signature = signature(d.Resources)
	d.Shape = classify(d.Resources)
}

// signature describes each resource by what it is and the modes that met on
// it — held modes on the left, wanted modes on the right — and joins them in
// a fixed order. Process ids are left out, and so are queued owners, which
// hold nothing: the same collision reads the same whether two sessions or
// eight piled into it.
func signature(resources []Resource) string {
	parts := make([]string, 0, len(resources))
	for _, r := range resources {
		target := r.Object
		if r.Index != "" {
			target += "." + r.Index
		}
		held := modes(r.Owners, true)
		wanted := modes(r.Waiters, false)
		part := strings.TrimSpace(r.Kind + " " + target)
		if held != "" || wanted != "" {
			part += " " + held + "→" + wanted
		}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return strings.Join(slices.Compact(parts), " ⇄ ")
}

func modes(requests []LockRequest, grantedOnly bool) string {
	var out []string
	for _, request := range requests {
		if request.Mode == "" || (grantedOnly && !request.Granted()) {
			continue
		}
		if !slices.Contains(out, request.Mode) {
			out = append(out, request.Mode)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "/")
}

func classify(resources []Resource) Shape {
	switch {
	case isScanVsWriter(resources):
		return ShapeScanVsWriter
	case isKeyLookupVsWriter(resources):
		return ShapeKeyLookupVsWriter
	}
	return ShapeOther
}

// exclusiveModes are the modes that conflict with a shared lock: what a
// writer holds, or intends to take, on the rows it changes.
var exclusiveModes = map[string]bool{"X": true, "IX": true, "SIX": true, "U": true, "IU": true, "UIX": true}

func isScanVsWriter(resources []Resource) bool {
	if len(resources) == 0 {
		return false
	}
	first := resources[0]
	var heldShared, heldExclusive, wantedShared, wantedExclusive bool
	for _, r := range resources {
		if r.Kind != "pagelock" || r.IndexType != IndexClustered || r.Index == "" ||
			r.Object != first.Object || r.Index != first.Index {
			return false
		}
		for _, owner := range r.Owners {
			if owner.Granted() {
				heldShared = heldShared || owner.Mode == "S"
				heldExclusive = heldExclusive || exclusiveModes[owner.Mode]
			}
		}
		for _, waiter := range r.Waiters {
			wantedShared = wantedShared || waiter.Mode == "S"
			wantedExclusive = wantedExclusive || exclusiveModes[waiter.Mode]
		}
	}
	return heldShared && heldExclusive && wantedShared && wantedExclusive
}

// isKeyLookupVsWriter looks for a writer holding an exclusive lock on a
// clustered key and waiting on a nonclustered key of the same table, and a
// different reader holding a shared lock on that nonclustered key and waiting
// for a shared lock on the clustered one.
func isKeyLookupVsWriter(resources []Resource) bool {
	for _, clustered := range resources {
		if clustered.Kind != "keylock" || clustered.IndexType != IndexClustered {
			continue
		}
		for _, nonclustered := range resources {
			if nonclustered.Kind != "keylock" || nonclustered.IndexType != IndexNonclustered ||
				nonclustered.Object != clustered.Object {
				continue
			}
			if lookupMeetsWriter(clustered, nonclustered) {
				return true
			}
		}
	}
	return false
}

func lookupMeetsWriter(clustered, nonclustered Resource) bool {
	for _, writer := range clustered.Owners {
		if !writer.Granted() || !exclusiveModes[writer.Mode] || !waitsFor(nonclustered, writer.ProcessID, exclusiveModes) {
			continue
		}
		for _, reader := range nonclustered.Owners {
			if reader.Granted() && reader.Mode == "S" && reader.ProcessID != writer.ProcessID &&
				waitsFor(clustered, reader.ProcessID, map[string]bool{"S": true}) {
				return true
			}
		}
	}
	return false
}

func waitsFor(r Resource, processID string, wanted map[string]bool) bool {
	return slices.ContainsFunc(r.Waiters, func(w LockRequest) bool {
		return w.ProcessID == processID && wanted[w.Mode]
	})
}

func distinct(resources []Resource, value func(Resource) string) []string {
	out := []string{}
	for _, r := range resources {
		if v := value(r); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
