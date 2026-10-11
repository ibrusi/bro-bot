package effort

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw   string
		want  Level
		valid bool
	}{
		{"auto", Auto, true},
		{" AUTO ", Auto, true},
		{"low", Low, true},
		{"Medium", Medium, true},
		{"high", High, true},
		{"xhigh", XHigh, true},
		{"MAX", Max, true},
		{"", Auto, false},
		{"x-high", Auto, false},
		{"ultra", Auto, false},
	}
	for _, tc := range cases {
		got, ok := Parse(tc.raw)
		if got != tc.want || ok != tc.valid {
			t.Errorf("Parse(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.valid)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	all := append([]Level{Auto}, Levels()...)
	for _, level := range all {
		parsed, ok := Parse(level.String())
		if !ok || parsed != level {
			t.Errorf("Parse(%q.String()) = (%q, %v), want (%q, true)", level, parsed, ok, level)
		}
	}
}

func TestAutoIsZeroValue(t *testing.T) {
	var level Level
	if !level.IsAuto() || level.String() != "auto" {
		t.Errorf("zero Level must be auto, got %q", level.String())
	}
	if High.IsAuto() {
		t.Error("High must not be auto")
	}
}

func TestLevelsReturnsCopy(t *testing.T) {
	levels := Levels()
	levels[0] = Max
	if Levels()[0] != Low {
		t.Error("Levels must not expose the internal slice")
	}
}
