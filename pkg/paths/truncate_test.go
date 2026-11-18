package paths

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsRunesWhole(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short enough", in: "abc", max: 8, want: "abc"},
		{name: "ascii", in: "abcdef", max: 3, want: "abc"},
		{name: "rune fits exactly", in: "é" + "bc", max: 2, want: "é"},
		{name: "rune does not fit", in: "aéb", max: 2, want: "a"},
		{name: "four byte rune does not fit", in: "ab\U0001F600", max: 3, want: "ab"},
		{name: "breaks before a partial rune", in: strings.Repeat("a", 199) + "é" + "bbbbb", max: 200, want: strings.Repeat("a", 199)},
		{name: "keeps the rune that lands on the budget", in: strings.Repeat("a", 198) + "éb", max: 200, want: strings.Repeat("a", 198) + "é"},
	}

	for _, test := range tests {
		got := truncateBytes(test.in, test.max)
		if got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: result is not valid UTF-8: %q", test.name, got)
		}
		if len(got) > test.max {
			t.Errorf("%s: result is %d bytes, over the %d byte budget", test.name, len(got), test.max)
		}
	}
}

func TestSanitizeFilenameTruncatesLongUnicodeNames(t *testing.T) {
	name := strings.Repeat("é", 200)

	out := SanitizeFilename(name)
	if !utf8.ValidString(out) {
		t.Fatalf("result is not valid UTF-8: %q", out)
	}
	if len(out) > maxFilenameLen {
		t.Fatalf("result is %d bytes, want at most %d", len(out), maxFilenameLen)
	}
	if want := strings.Repeat("é", maxFilenameLen/2); out != want {
		t.Errorf("got %d runes, want %d", utf8.RuneCountInString(out), utf8.RuneCountInString(want))
	}
}
