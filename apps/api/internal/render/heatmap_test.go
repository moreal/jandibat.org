package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"reflect"
	"strings"
	"testing"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func TestRenderHeatmapSVGThemeSnapshots(t *testing.T) {
	t.Parallel()

	from, to := domain.Date("2026-03-01"), domain.Date("2026-03-01")
	timeline := domain.Timeline{
		Subject: "alice",
		Days:    []domain.Day{{Date: from, Count: 10, Level: domain.LevelVeryHigh}},
	}
	tests := []struct {
		name       string
		theme      string
		background string
		text       string
		levels     [5]string
	}{
		{
			name:       "default is light",
			background: "#ffffff",
			text:       "#57606a",
			levels:     [5]string{"#ebedf0", "#9be9a8", "#40c463", "#30a14e", "#216e39"},
		},
		{
			name:       "light",
			theme:      "light",
			background: "#ffffff",
			text:       "#57606a",
			levels:     [5]string{"#ebedf0", "#9be9a8", "#40c463", "#30a14e", "#216e39"},
		},
		{
			name:       "dark",
			theme:      "dark",
			background: "#0d1117",
			text:       "#8b949e",
			levels:     [5]string{"#161b22", "#0e4429", "#006d32", "#26a641", "#39d353"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := mustRender(t, timeline, HeatmapOptions{From: &from, To: &to, Theme: test.theme})
			assertContains(t, got,
				`<rect width="100%" height="100%" fill="`+test.background+`"/>`,
				`<g class="weekday-labels" fill="`+test.text+`"`,
				`class="day level-4" x="30" y="24" width="11" height="11" rx="2" fill="`+test.levels[4]+`"`,
			)
			for level, color := range test.levels {
				assertContains(t, got, fmt.Sprintf(
					`data-level="%d" x="%d" y="130" width="11" height="11" fill="%s"`,
					level,
					leftPadding+level*(defaultCellSize+defaultGap),
					color,
				))
			}
		})
	}
}

func TestRenderHeatmapSVGRangeAggregationAndDeterminism(t *testing.T) {
	t.Parallel()

	timeline := domain.Timeline{Subject: "alice", Days: []domain.Day{
		{Date: "2026-03-03", Count: 10, Level: domain.LevelNone},
		{Date: "2026-03-01", Count: 1, Level: domain.LevelVeryHigh},
		{Date: "2026-03-01", Count: 5, Level: domain.LevelLow},
	}}
	original := append([]domain.Day(nil), timeline.Days...)

	first := mustRender(t, timeline, HeatmapOptions{})
	second := mustRender(t, timeline, HeatmapOptions{})
	if !bytes.Equal(first, second) {
		t.Fatal("identical input produced different SVG bytes")
	}
	if !reflect.DeepEqual(timeline.Days, original) {
		t.Fatalf("renderer mutated input days: got %#v, want %#v", timeline.Days, original)
	}
	assertContains(t, first,
		`Activity from 2026-03-01 through 2026-03-03`,
		`class="day level-3" x="30" y="24" width="11" height="11" rx="2" fill="#30a14e" data-date="2026-03-01" data-count="6"`,
		`class="day level-0" x="30" y="38" width="11" height="11" rx="2" fill="#ebedf0" data-date="2026-03-02" data-count="0"`,
		`class="day level-4" x="30" y="52" width="11" height="11" rx="2" fill="#216e39" data-date="2026-03-03" data-count="10"`,
	)
	if got := strings.Count(string(first), `class="day level-`); got != 3 {
		t.Fatalf("rendered day count = %d, want 3", got)
	}

	from, to := domain.Date("2026-02-28"), domain.Date("2026-03-02")
	explicit := mustRender(t, timeline, HeatmapOptions{From: &from, To: &to})
	assertContains(t, explicit,
		`Activity from 2026-02-28 through 2026-03-02`,
		`data-date="2026-02-28" data-count="0"`,
		`data-date="2026-03-01" data-count="6"`,
		`data-date="2026-03-02" data-count="0"`,
	)
	if strings.Contains(string(explicit), `data-date="2026-03-03"`) {
		t.Fatal("renderer included a timeline day outside the explicit range")
	}

	onlyFrom := domain.Date("2026-03-02")
	assertContains(t, mustRender(t, timeline, HeatmapOptions{From: &onlyFrom}), `Activity from 2026-03-02 through 2026-03-03`)
	onlyTo := domain.Date("2026-03-02")
	assertContains(t, mustRender(t, timeline, HeatmapOptions{To: &onlyTo}), `Activity from 2026-03-01 through 2026-03-02`)
}

func TestRenderHeatmapSVGEmptyTimelineWithExplicitRange(t *testing.T) {
	t.Parallel()

	from, to := domain.Date("2026-03-01"), domain.Date("2026-03-02")
	got := mustRender(t, domain.Timeline{Subject: "empty"}, HeatmapOptions{From: &from, To: &to})
	assertContains(t, got,
		`Activity from 2026-03-01 through 2026-03-02`,
		`data-date="2026-03-01" data-count="0"`,
		`data-date="2026-03-02" data-count="0"`,
	)
}

func TestRenderHeatmapSVGOptions(t *testing.T) {
	t.Parallel()

	from, to := domain.Date("2026-03-01"), domain.Date("2026-03-02")
	timeline := domain.Timeline{Subject: "alice", Days: []domain.Day{
		{Date: from, Count: 1},
		{Date: to, Count: 2},
	}}

	t.Run("cell size controls geometry", func(t *testing.T) {
		t.Parallel()
		got := mustRender(t, timeline, HeatmapOptions{From: &from, To: &from, CellSize: 20})
		assertContains(t, got,
			`width="58" height="212" viewBox="0 0 58 212"`,
			`width="20" height="20" rx="2"`,
		)
	})

	t.Run("legend can be disabled", func(t *testing.T) {
		t.Parallel()
		showLegend := false
		got := mustRender(t, timeline, HeatmapOptions{From: &from, To: &from, ShowLegend: &showLegend})
		assertContains(t, got, `width="49" height="127" viewBox="0 0 49 127"`)
		if strings.Contains(string(got), `class="legend"`) {
			t.Fatal("legend was rendered when ShowLegend was false")
		}
	})

	t.Run("sunday week start", func(t *testing.T) {
		t.Parallel()
		got := mustRender(t, timeline, HeatmapOptions{From: &from, To: &to, WeekStart: "sunday"})
		assertContains(t, got,
			`class="day level-1" x="30" y="24" width="11" height="11"`,
			`class="day level-1" x="30" y="38" width="11" height="11"`,
			`<text x="0" y="47">Mon</text>`,
		)
	})

	t.Run("monday week start", func(t *testing.T) {
		t.Parallel()
		got := mustRender(t, timeline, HeatmapOptions{From: &from, To: &to, WeekStart: "monday"})
		assertContains(t, got,
			`width="63" height="149" viewBox="0 0 63 149"`,
			`class="day level-1" x="30" y="108" width="11" height="11"`,
			`class="day level-1" x="44" y="24" width="11" height="11"`,
			`<text x="0" y="33">Mon</text>`,
		)
	})
}

func TestRenderHeatmapSVGErrors(t *testing.T) {
	t.Parallel()

	validFrom, validTo := domain.Date("2026-03-01"), domain.Date("2026-03-02")
	invalidDate := domain.Date("2026-02-30")
	reversedFrom, reversedTo := domain.Date("2026-03-03"), domain.Date("2026-03-01")
	tests := []struct {
		name     string
		timeline domain.Timeline
		options  HeatmapOptions
		want     error
	}{
		{name: "unknown theme", options: HeatmapOptions{From: &validFrom, To: &validTo, Theme: "solarized"}, want: ErrUnknownTheme},
		{name: "negative cell size", options: HeatmapOptions{From: &validFrom, To: &validTo, CellSize: -1}, want: ErrInvalidCellSize},
		{name: "negative gap", options: HeatmapOptions{From: &validFrom, To: &validTo, Gap: -1}, want: ErrInvalidGap},
		{name: "invalid week start", options: HeatmapOptions{From: &validFrom, To: &validTo, WeekStart: "tuesday"}, want: errors.New(`activity render: invalid week start "tuesday"`)},
		{name: "empty range", want: ErrEmptyRenderRange},
		{name: "only one bound for empty timeline", options: HeatmapOptions{From: &validFrom}, want: ErrEmptyRenderRange},
		{name: "invalid from", options: HeatmapOptions{From: &invalidDate, To: &validTo}, want: ErrInvalidRenderDate},
		{name: "invalid to", options: HeatmapOptions{From: &validFrom, To: &invalidDate}, want: ErrInvalidRenderDate},
		{name: "invalid timeline date", timeline: domain.Timeline{Days: []domain.Day{{Date: invalidDate, Count: 1}}}, options: HeatmapOptions{From: &validFrom, To: &validTo}, want: ErrInvalidRenderDate},
		{name: "reversed range", options: HeatmapOptions{From: &reversedFrom, To: &reversedTo}, want: ErrInvalidRenderRange},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := RenderHeatmapSVG(test.timeline, test.options)
			if test.name == "invalid week start" {
				if err == nil || err.Error() != test.want.Error() {
					t.Fatalf("RenderHeatmapSVG() error = %v, want %q", err, test.want)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("RenderHeatmapSVG() error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestRenderHeatmapSVGEscapesHostileSubjectAndIsSelfContained(t *testing.T) {
	t.Parallel()

	const hostile = `</title><script>alert("x")</script><image href="https://evil.invalid/x" onload="steal()"><style>rect{fill:url(//evil.invalid)}</style>&'`
	date := domain.Date("2026-03-01")
	got := mustRender(t, domain.Timeline{Subject: domain.SubjectID(hostile)}, HeatmapOptions{From: &date, To: &date})
	if !strings.Contains(string(got), html.EscapeString(hostile)) {
		t.Fatalf("hostile subject was not escaped in SVG: %s", got)
	}
	if strings.Contains(string(got), `</title><script`) {
		t.Fatal("hostile subject broke out of the title element")
	}

	allowedElements := map[string]bool{"svg": true, "title": true, "desc": true, "rect": true, "g": true, "text": true}
	decoder := xml.NewDecoder(bytes.NewReader(got))
	var rootSeen bool
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("rendered SVG is not well-formed XML: %v\n%s", err, got)
		}
		switch typed := token.(type) {
		case xml.Directive:
			t.Fatalf("SVG must not contain XML directives: %q", typed)
		case xml.ProcInst:
			if strings.ToLower(typed.Target) != "xml" {
				t.Fatalf("SVG must not contain processing instruction %q", typed.Target)
			}
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			rootSeen = true
			if start.Name.Local != "svg" || start.Name.Space != "http://www.w3.org/2000/svg" {
				t.Fatalf("root element = {%s}%s, want SVG namespace", start.Name.Space, start.Name.Local)
			}
		}
		if !allowedElements[start.Name.Local] {
			t.Fatalf("unsafe or unexpected SVG element <%s>", start.Name.Local)
		}
		for _, attribute := range start.Attr {
			name := strings.ToLower(attribute.Name.Local)
			value := strings.ToLower(strings.TrimSpace(attribute.Value))
			isNamespace := name == "xmlns" || attribute.Name.Space == "xmlns"
			if strings.HasPrefix(name, "on") {
				t.Fatalf("event-handler attribute %q is not self-contained", attribute.Name.Local)
			}
			if name == "href" || name == "src" {
				t.Fatalf("external-reference attribute %q is not self-contained", attribute.Name.Local)
			}
			if name == "style" && strings.Contains(value, "url(") {
				t.Fatalf("URL-bearing style attribute is not self-contained: %q", attribute.Value)
			}
			if !isNamespace && (strings.Contains(value, "://") || strings.HasPrefix(value, "//") || strings.Contains(value, "url(")) {
				t.Fatalf("attribute %q contains an external reference: %q", attribute.Name.Local, attribute.Value)
			}
		}
	}
	if !rootSeen {
		t.Fatal("rendered document did not contain an SVG root")
	}
}

func TestRenderHeatmapSVGGoldenDigest(t *testing.T) {
	t.Parallel()

	from, to := domain.Date("2026-03-01"), domain.Date("2026-03-03")
	showLegend := false
	got := mustRender(t, domain.Timeline{Subject: "snapshot", Days: []domain.Day{
		{Date: "2026-03-03", Count: 10},
		{Date: "2026-03-01", Count: 1},
	}}, HeatmapOptions{From: &from, To: &to, Theme: "dark", CellSize: 12, ShowLegend: &showLegend, WeekStart: "monday"})

	const wantSHA256 = "113f362880f1bc1eea90faf5c81ed23b9d95a116970f53ae427bdceb9fdcd432"
	if digest := fmt.Sprintf("%x", sha256.Sum256(got)); digest != wantSHA256 {
		t.Fatalf("SVG snapshot digest = %s, want %s\n%s", digest, wantSHA256, got)
	}
}

func TestRenderHeatmapSVGRejectsDuplicateDayCountOverflow(t *testing.T) {
	t.Parallel()
	date := domain.Date("2026-03-01")
	maximum := int(^uint(0) >> 1)
	_, err := RenderHeatmapSVG(domain.Timeline{Days: []domain.Day{
		{Date: date, Count: maximum},
		{Date: date, Count: 1},
	}}, HeatmapOptions{From: &date, To: &date})
	if !errors.Is(err, domain.ErrMetricAggregationOverflow) {
		t.Fatalf("RenderHeatmapSVG() overflow error = %v, want %v", err, domain.ErrMetricAggregationOverflow)
	}
}

func mustRender(t *testing.T, timeline domain.Timeline, options HeatmapOptions) []byte {
	t.Helper()
	got, err := RenderHeatmapSVG(timeline, options)
	if err != nil {
		t.Fatalf("RenderHeatmapSVG() error = %v", err)
	}
	return got
}

func assertContains(t *testing.T, got []byte, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(string(got), fragment) {
			t.Fatalf("SVG does not contain %q\n%s", fragment, got)
		}
	}
}
