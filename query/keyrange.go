package query

import "fmt"

// KeyRange narrows a reconcile to the keys in [From, To). An empty end is open,
// so an empty range is everything.
//
// It is half-open on purpose: consecutive ranges that share a boundary cover
// every key exactly once, which is what lets a large reconcile be split into
// pieces — by hand or across workers — without a key falling into both or
// neither.
type KeyRange struct {
	From string `json:"from,omitempty" yaml:"from,omitempty"`
	To   string `json:"to,omitempty" yaml:"to,omitempty"`
}

// Clone returns a copy, so a merged config never aliases a stored range.
func (r *KeyRange) Clone() *KeyRange {
	if r == nil {
		return nil
	}
	cloned := *r
	return &cloned
}

// Validate rejects a range that can hold no key.
func (r *KeyRange) Validate() error {
	if r == nil {
		return nil
	}
	if r.From != "" && r.To != "" && r.From >= r.To {
		return fmt.Errorf("reconcile range from %q is not before to %q, so it covers no keys", r.From, r.To)
	}
	return nil
}

// Contains reports whether key falls in the range.
func (r *KeyRange) Contains(key string) bool {
	if r == nil {
		return true
	}
	if r.From != "" && key < r.From {
		return false
	}
	if r.To != "" && key >= r.To {
		return false
	}
	return true
}

// Before reports whether key sorts before the range, which on an ordered walk
// means it can be skipped without reading further.
func (r *KeyRange) Before(key string) bool {
	return r != nil && r.From != "" && key < r.From
}

// After reports whether key sorts at or past the end of the range. On an
// ordered walk it means the rest of that side cannot contribute, so the walk
// stops rather than reading to the end of the dataset.
func (r *KeyRange) After(key string) bool {
	return r != nil && r.To != "" && key >= r.To
}

// String renders the range for a message.
func (r *KeyRange) String() string {
	if r == nil || (r.From == "" && r.To == "") {
		return "all keys"
	}
	from, to := r.From, r.To
	if from == "" {
		from = "the first key"
	}
	if to == "" {
		to = "the last key"
	}
	return fmt.Sprintf("keys from %s up to %s", from, to)
}
