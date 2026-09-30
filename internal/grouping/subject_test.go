package grouping

import "testing"

func TestNormalizeSubject(t *testing.T) {
	tests := map[string]string{
		"Re: Fwd:  Project   Update ":    "project update",
		"=?UTF-8?Q?Re=3A_Pr=C3=BCfung?=": "prüfung",
		"[Team] Re: Launch":              "[team] re: launch",
	}
	for input, want := range tests {
		if got := NormalizeSubject(input); got != want {
			t.Errorf("NormalizeSubject(%q) = %q, want %q", input, got, want)
		}
	}
}
