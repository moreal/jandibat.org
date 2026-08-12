// Package render produces deterministic server-side representations of
// activity timelines.
package render

import (
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

const (
	defaultCellSize = 11
	defaultGap      = 3
	leftPadding     = 30
	topPadding      = 24
	rightPadding    = 8
	bottomPadding   = 8
	legendHeight    = 22
)

var (
	ErrInvalidRenderDate  = errors.New("activity render: invalid date")
	ErrInvalidRenderRange = errors.New("activity render: from must not be after to")
	ErrEmptyRenderRange   = errors.New("activity render: from and to are required for an empty timeline")
	ErrUnknownTheme       = errors.New("activity render: unknown theme")
	ErrInvalidCellSize    = errors.New("activity render: cell size must be positive")
	ErrInvalidGap         = errors.New("activity render: gap must not be negative")
)

type Theme struct {
	Name       string
	Background string
	Text       string
	Levels     [5]string
}

var themes = map[string]Theme{
	"light": {
		Name: "light", Background: "#ffffff", Text: "#57606a",
		Levels: [5]string{"#ebedf0", "#9be9a8", "#40c463", "#30a14e", "#216e39"},
	},
	"dark": {
		Name: "dark", Background: "#0d1117", Text: "#8b949e",
		Levels: [5]string{"#161b22", "#0e4429", "#006d32", "#26a641", "#39d353"},
	},
}

type HeatmapOptions struct {
	From       *domain.Date
	To         *domain.Date
	Theme      string
	CellSize   int
	Gap        int
	ShowLegend *bool
	WeekStart  string
}

// RenderHeatmapSVG renders a complete SVG document. Its output depends only on
// the arguments: dates are sorted, duplicate days are summed, and no timestamps
// or random identifiers are emitted.
func RenderHeatmapSVG(timeline domain.Timeline, options HeatmapOptions) ([]byte, error) {
	themeName := options.Theme
	if themeName == "" {
		themeName = "light"
	}
	theme, ok := themes[themeName]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTheme, themeName)
	}
	cellSize := options.CellSize
	if cellSize == 0 {
		cellSize = defaultCellSize
	}
	if cellSize < 0 {
		return nil, ErrInvalidCellSize
	}
	gap := options.Gap
	if gap == 0 {
		gap = defaultGap
	}
	if gap < 0 {
		return nil, ErrInvalidGap
	}
	showLegend := true
	if options.ShowLegend != nil {
		showLegend = *options.ShowLegend
	}
	if options.WeekStart == "" {
		options.WeekStart = "sunday"
	}
	if options.WeekStart != "sunday" && options.WeekStart != "monday" {
		return nil, fmt.Errorf("activity render: invalid week start %q", options.WeekStart)
	}

	from, to, err := resolveRenderRange(timeline, options.From, options.To)
	if err != nil {
		return nil, err
	}
	days, err := normalizeDays(timeline.Days)
	if err != nil {
		return nil, err
	}
	gridStart := startOfWeek(from, options.WeekStart)
	gridEnd := endOfWeek(to, options.WeekStart)
	weekCount := int(gridEnd.Sub(gridStart).Hours()/24)/7 + 1
	step := cellSize + gap
	width := leftPadding + weekCount*step - gap + rightPadding
	height := topPadding + 7*step - gap + bottomPadding
	if showLegend {
		height += legendHeight
	}

	var output strings.Builder
	output.Grow(512 + weekCount*7*160)
	output.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	output.WriteByte('\n')
	output.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" role="img" aria-labelledby="heatmap-title heatmap-desc"`)
	writeIntAttribute(&output, "width", width)
	writeIntAttribute(&output, "height", height)
	output.WriteString(` viewBox="0 0 `)
	output.WriteString(strconv.Itoa(width))
	output.WriteByte(' ')
	output.WriteString(strconv.Itoa(height))
	output.WriteString(`">`)
	output.WriteByte('\n')
	output.WriteString(`  <title id="heatmap-title">Activity heatmap for `)
	output.WriteString(escapeText(string(timeline.Subject)))
	output.WriteString(`</title>`)
	output.WriteByte('\n')
	output.WriteString(`  <desc id="heatmap-desc">Activity from `)
	output.WriteString(from.Format(time.DateOnly))
	output.WriteString(` through `)
	output.WriteString(to.Format(time.DateOnly))
	output.WriteString(`</desc>`)
	output.WriteByte('\n')
	output.WriteString(`  <rect width="100%" height="100%" fill="`)
	output.WriteString(escapeAttribute(theme.Background))
	output.WriteString(`"/>`)
	output.WriteByte('\n')
	output.WriteString(`  <g class="weekday-labels" fill="`)
	output.WriteString(escapeAttribute(theme.Text))
	output.WriteString(`" font-family="-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif" font-size="9">`)
	output.WriteByte('\n')
	weekdayOffset := 0
	if options.WeekStart == "sunday" {
		weekdayOffset = 1
	}
	writeWeekdayLabel(&output, "Mon", weekdayOffset, step)
	writeWeekdayLabel(&output, "Wed", weekdayOffset+2, step)
	writeWeekdayLabel(&output, "Fri", weekdayOffset+4, step)
	output.WriteString("  </g>\n")
	output.WriteString(`  <g class="days">`)
	output.WriteByte('\n')

	for date := from; !date.After(to); date = date.AddDate(0, 0, 1) {
		dateText := domain.Date(date.Format(time.DateOnly))
		day := days[dateText]
		week := int(date.Sub(gridStart).Hours()/24) / 7
		weekday := weekdayIndex(date, options.WeekStart)
		x := leftPadding + week*step
		y := topPadding + weekday*step
		level := day.Level
		if level > domain.LevelVeryHigh {
			level = domain.LevelVeryHigh
		}
		output.WriteString(`    <rect class="day level-`)
		output.WriteString(strconv.Itoa(int(level)))
		output.WriteString(`"`)
		writeIntAttribute(&output, "x", x)
		writeIntAttribute(&output, "y", y)
		writeIntAttribute(&output, "width", cellSize)
		writeIntAttribute(&output, "height", cellSize)
		output.WriteString(` rx="2" fill="`)
		output.WriteString(theme.Levels[level])
		output.WriteString(`" data-date="`)
		output.WriteString(string(dateText))
		output.WriteString(`" data-count="`)
		output.WriteString(strconv.Itoa(day.Count))
		output.WriteString(`"><title>`)
		output.WriteString(string(dateText))
		output.WriteString(": ")
		output.WriteString(strconv.Itoa(day.Count))
		output.WriteString(` contributions</title></rect>`)
		output.WriteByte('\n')
	}
	output.WriteString("  </g>\n")
	if showLegend {
		legendY := topPadding + 7*step + 8
		output.WriteString(`  <g class="legend" aria-label="Activity level legend">`)
		for index, color := range theme.Levels {
			output.WriteString(`<rect`)
			writeIntAttribute(&output, "data-level", index)
			writeIntAttribute(&output, "x", leftPadding+index*(cellSize+gap))
			writeIntAttribute(&output, "y", legendY)
			writeIntAttribute(&output, "width", cellSize)
			writeIntAttribute(&output, "height", cellSize)
			output.WriteString(` fill="`)
			output.WriteString(color)
			output.WriteString(`"/>`)
		}
		output.WriteString("</g>\n")
	}
	output.WriteString("</svg>\n")
	return []byte(output.String()), nil
}

func resolveRenderRange(timeline domain.Timeline, fromInput, toInput *domain.Date) (time.Time, time.Time, error) {
	var minimum, maximum domain.Date
	for _, day := range timeline.Days {
		if _, err := time.Parse(time.DateOnly, string(day.Date)); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: %q", ErrInvalidRenderDate, day.Date)
		}
		if minimum == "" || day.Date < minimum {
			minimum = day.Date
		}
		if maximum == "" || day.Date > maximum {
			maximum = day.Date
		}
	}
	if fromInput != nil {
		minimum = *fromInput
	}
	if toInput != nil {
		maximum = *toInput
	}
	if minimum == "" || maximum == "" {
		return time.Time{}, time.Time{}, ErrEmptyRenderRange
	}
	from, err := time.Parse(time.DateOnly, string(minimum))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: %q", ErrInvalidRenderDate, minimum)
	}
	to, err := time.Parse(time.DateOnly, string(maximum))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: %q", ErrInvalidRenderDate, maximum)
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, ErrInvalidRenderRange
	}
	return from, to, nil
}

func normalizeDays(input []domain.Day) (map[domain.Date]domain.Day, error) {
	ordered := append([]domain.Day(nil), input...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Date < ordered[j].Date })
	result := make(map[domain.Date]domain.Day, len(ordered))
	for _, day := range ordered {
		if _, err := time.Parse(time.DateOnly, string(day.Date)); err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRenderDate, day.Date)
		}
		existing := result[day.Date]
		existing.Date = day.Date
		count, err := domain.AddMetricValues(existing.Count, day.Count)
		if err != nil {
			return nil, err
		}
		existing.Count = count
		existing.Level = domain.LevelForCount(existing.Count)
		result[day.Date] = existing
	}
	return result, nil
}

func startOfWeek(date time.Time, weekStart string) time.Time {
	return date.AddDate(0, 0, -weekdayIndex(date, weekStart))
}

func endOfWeek(date time.Time, weekStart string) time.Time {
	return date.AddDate(0, 0, 6-weekdayIndex(date, weekStart))
}

func weekdayIndex(date time.Time, weekStart string) int {
	weekday := int(date.Weekday())
	if weekStart == "monday" {
		return (weekday + 6) % 7
	}
	return weekday
}

func writeIntAttribute(output *strings.Builder, name string, value int) {
	output.WriteByte(' ')
	output.WriteString(name)
	output.WriteString(`="`)
	output.WriteString(strconv.Itoa(value))
	output.WriteByte('"')
}

func writeWeekdayLabel(output *strings.Builder, label string, weekday, step int) {
	y := topPadding + weekday*step + 9
	output.WriteString(`    <text x="0" y="`)
	output.WriteString(strconv.Itoa(y))
	output.WriteString(`">`)
	output.WriteString(label)
	output.WriteString(`</text>`)
	output.WriteByte('\n')
}

func escapeText(value string) string      { return html.EscapeString(value) }
func escapeAttribute(value string) string { return html.EscapeString(value) }
