package allowlist

import "testing"

const hexDigits = "0123456789abcdef"

func TestCopyReturnsTheInputWhenEveryByteIsAllowed(t *testing.T) {
	for _, input := range []string{"", "0", "deadbeef", hexDigits} {
		got, ok := Copy(input, hexDigits)
		if !ok {
			t.Errorf("Copy(%q) rejected an input drawn from the alphabet", input)
			continue
		}
		if got != input {
			t.Errorf("Copy(%q) = %q, want the input unchanged", input, got)
		}
	}
}

func TestCopyRejectsAnyByteOutsideTheAlphabet(t *testing.T) {
	for _, input := range []string{"DEADBEEF", "dead beef", "dead\x00beef", "deadbeef;", "café"} {
		if got, ok := Copy(input, hexDigits); ok {
			t.Errorf("Copy(%q) = %q, want rejection", input, got)
		}
	}
}

func TestCopyRejectsEveryByteAgainstAnEmptyAlphabet(t *testing.T) {
	if _, ok := Copy("a", ""); ok {
		t.Error(`Copy("a", "") was accepted, want rejection`)
	}
	if got, ok := Copy("", ""); !ok || got != "" {
		t.Errorf(`Copy("", "") = %q, %v; want "", true`, got, ok)
	}
}
