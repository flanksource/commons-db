// Test access to opening a store without the in-process sharing, so one test
// process can hold a store twice, and to the failpoints a child crashes at.
package owner

import "context"

// SetFailpoint makes the test binary crash or pause at the named moments.
func SetFailpoint(at func(name string)) { failpoint = at }

// OpenUnshared is Open without the in-process registry.
func OpenUnshared[T Target](ctx context.Context, options Options[T]) (*Store[T], error) {
	return open(ctx, options, false)
}
