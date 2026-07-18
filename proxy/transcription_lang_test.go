package proxy

import "testing"

func TestNormalizeLangCode(t *testing.T) {
	cases := map[string]string{
		"ar":      "ar",
		"AR":      "ar",
		" en ":    "en",
		"ar-EG":   "ar",
		"en-US":   "en",
		"fr-FR":   "fr",
		"":        "",
		"english": "", // not a 2-letter code
		"e":       "",
		"a1":      "",
		"123":     "",
	}
	for in, want := range cases {
		if got := normalizeLangCode(in); got != want {
			t.Errorf("normalizeLangCode(%q) = %q, want %q", in, got, want)
		}
	}
}
