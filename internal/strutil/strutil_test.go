package strutil

import "testing"

func TestDeriveAuthorSort(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"Stephen King":           "King, Stephen",
		"Ursula K. Le Guin":      "Guin, Ursula K. Le",
		"Plato":                  "Plato",
		"King, Stephen":          "King, Stephen", // already sort order -> unchanged
		"  Brandon  Sanderson  ": "Sanderson, Brandon",
		"J. R. R. Tolkien":       "Tolkien, J. R. R.",
		"Madonna":                "Madonna",
		"le Guin, Ursula":        "le Guin, Ursula", // comma present -> left alone
	}
	for in, want := range cases {
		if got := DeriveAuthorSort(in); got != want {
			t.Errorf("DeriveAuthorSort(%q) = %q, want %q", in, got, want)
		}
	}
}
