package scalar

import (
	"bytes"
	"strings"
	"testing"
)

func TestLongGraphQLUsesExactDecimalStringsBeyondGraphQLIntRange(t *testing.T) {
	for _, tc := range []struct {
		value Long
		want  string
	}{
		{0, `"0"`},
		{2147483648, `"2147483648"`},
		{9223372036854775807, `"9223372036854775807"`},
		{-9223372036854775808, `"-9223372036854775808"`},
	} {
		var out bytes.Buffer
		tc.value.MarshalGQL(&out)
		got := out.String()
		if got != tc.want {
			t.Fatalf("MarshalGQL(%d) = %q, want %q", tc.value, got, tc.want)
		}
		var roundtrip Long
		if err := roundtrip.UnmarshalGQL(strings.Trim(got, `"`)); err != nil || roundtrip != tc.value {
			t.Fatalf("UnmarshalGQL(%q) = (%d, %v), want %d", got, roundtrip, err, tc.value)
		}
	}
}

func TestLongRejectsNonCanonicalOrOutOfRangeInput(t *testing.T) {
	for _, input := range []any{"", "01", "-0", "+1", " 1", "1.0", "9223372036854775808", "-9223372036854775809", 123, nil} {
		var value Long
		if err := value.UnmarshalGQL(input); err == nil {
			t.Fatalf("UnmarshalGQL(%#v) accepted invalid input", input)
		}
	}
}
