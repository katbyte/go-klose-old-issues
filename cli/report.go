// The shared HTML-report scaffolding: sections of items with linked evidence
// spans, rendered through the embedded report template. The close report and
// the label report both ride it.

package cli

import (
	"fmt"
	"html/template"
	"os"
	"slices"
	"time"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

// Span colour kinds, matching css classes in the report template.
const (
	KindOK    = "ok"
	KindMid   = "mid"
	KindWarn  = "warn"
	KindBad   = "bad"
	KindVer   = "ver"
	KindDim   = "dim"
	KindQuote = "quote"
)

// ReportSpan is one fragment of an evidence line: a link when URL is set,
// coloured by Kind (a css class in the template).
type ReportSpan struct {
	Text string
	URL  string
	Kind string // "" or one of the Kind* consts
}

// ReportItem is one candidate: the issue, its evidence lines, and the AI's
// verdict when the report was scored.
type ReportItem struct {
	Number   int
	URL      string
	Title    string
	Meta     string
	Evidence [][]ReportSpan
	AIScore  string // "" when not judged
	AIKind   string
	AIReason string
}

// ReportClass is one evidence class tally shown as a pill.
type ReportClass struct {
	Name  string
	Count int
	Kind  string
}

// ReportSection is one command's slice of the report: what it asks, what it
// found, and how to act on it.
type ReportSection struct {
	Slug        string // anchor id
	Name        string // display name when it differs from the slug ("label version")
	Question    string
	Description string
	Note        string // extra context line, e.g. what the rules protected
	Command     string // the CLI commands that act on this section
	Total       int    // every candidate the command found
	Classes     []ReportClass
	Items       []ReportItem
	Truncated   bool // --limit cut the item list short
}

// ReportData is one rendered report page.
type ReportData struct {
	Repo        string
	Noun        string // what the header calls the total ("close candidates")
	GeneratedAt string
	WithAI      bool
	Total       int
	Sections    []ReportSection
}

// Span builds a coloured text span; LinkSpan a linked one.
func Span(t, kind string) ReportSpan    { return ReportSpan{Text: t, Kind: kind} }
func LinkSpan(t, url string) ReportSpan { return ReportSpan{Text: t, URL: url} }

// ReportAIKind buckets a confidence for colouring, matching ScoreTag's bands.
func ReportAIKind(c float64) string {
	switch {
	case c >= JudgeThreshold:
		return KindOK
	case c >= 0.4:
		return KindMid
	default:
		return KindBad
	}
}

// LimitFindings caps a command's candidates for cheap test runs — applied
// BEFORE any AI judging so --limit 10 costs ten verdicts, not the full set.
func LimitFindings[T any](findings []T, limit int) ([]T, bool) {
	if limit > 0 && len(findings) > limit {
		return findings[:limit], true
	}
	return findings, false
}

// SortByVerdict orders findings surest-first once verdicts exist; unanswered
// candidates sink to the bottom.
func SortByVerdict[T any](findings []T, number func(*T) int, verdicts map[int]*issue.Verdict) {
	slices.SortStableFunc(findings, func(a, b T) int {
		av, bv := -1.0, -1.0
		if v := verdicts[number(&a)]; v != nil {
			av = v.Confidence
		}
		if v := verdicts[number(&b)]; v != nil {
			bv = v.Confidence
		}
		switch {
		case av > bv:
			return -1
		case av < bv:
			return 1
		default:
			return 0
		}
	})
}

// AttachVerdict fills the AI fields on one item.
func AttachVerdict(item *ReportItem, v *issue.Verdict) {
	if v == nil {
		return
	}
	item.AIScore = fmt.Sprintf("%.2f", v.Confidence)
	item.AIKind = ReportAIKind(v.Confidence)
	item.AIReason = text.OneLine(v.Reason)
}

// ReportFileName stamps a report output with the run's date and time —
// close-20260301-1252.html — so regenerating never overwrites an earlier run.
func ReportFileName(prefix string, now time.Time) string {
	return prefix + "-" + now.Format("20060102-1504") + ".html"
}

// WriteReportHTML renders one report page to the path.
func WriteReportHTML(path string, data *ReportData) error {
	tmpl, err := template.New("report").Parse(assets.Styles() + assets.ReportHTML())
	if err != nil {
		return fmt.Errorf("parsing report template: %w", err)
	}

	out, err := os.Create(path) //nolint:gosec // G304: user-chosen output path is the point
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer func() { _ = out.Close() }()

	if err := tmpl.Execute(out, data); err != nil {
		return fmt.Errorf("rendering report: %w", err)
	}
	return nil
}
