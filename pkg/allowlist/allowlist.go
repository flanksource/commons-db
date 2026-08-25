// Package allowlist copies caller-supplied text out of a fixed alphabet.
//
// It exists for the places where a validated string is about to become part of
// something that executes — a SQL identifier, an argv entry. Checking the input
// and then using the input leaves the executed bytes owned by whoever supplied
// them; copying through an alphabet makes them bytes the program spelled out,
// and turns "I validated this" into a property of the value rather than of the
// control flow that produced it.
package allowlist

import "strings"

// Copy rebuilds s one byte at a time out of alphabet, and reports false the
// moment s holds a byte alphabet does not. Nothing is substituted or dropped,
// so a successful result is byte-identical to s.
func Copy(s, alphabet string) (string, bool) {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		at := strings.IndexByte(alphabet, s[i])
		if at < 0 {
			return "", false
		}
		out.WriteByte(alphabet[at])
	}
	return out.String(), true
}
