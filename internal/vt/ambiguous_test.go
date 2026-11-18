package vt

import "testing"

func TestIsAmbiguous(t *testing.T) {
	for _, r := range "●◆○▌…─│╭╮╰╯·—¹²←" {
		if !IsAmbiguous(r) {
			t.Errorf("%q (U+%04X) is East Asian Ambiguous", r, r)
		}
		if RuneWidth(r) != 1 {
			t.Errorf("%q: RuneWidth = %d, want 1 (ambiguous counts narrow by default)", r, RuneWidth(r))
		}
	}
	for _, r := range "a~|+中⏎" {
		if IsAmbiguous(r) {
			t.Errorf("%q (U+%04X) is not ambiguous", r, r)
		}
	}
}
