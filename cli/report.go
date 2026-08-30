package cli

import (
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/issue"
	"github.com/katbyte/koi/lib/text"
)

// Span colour kinds, matching css classes in the report template.
const (
	kindOK    = "ok"
	kindMid   = "mid"
	kindWarn  = "warn"
	kindBad   = "bad"
	kindVer   = "ver"
	kindDim   = "dim"
	kindQuote = "quote"
)

// reportSpan is one fragment of an evidence line: a link when URL is set,
// coloured by Kind (a css class in the template).
type reportSpan struct {
	Text string
	URL  string
	Kind string // "" or one of the kind* consts
}

// reportItem is one close candidate: the issue, its evidence lines, and the
// AI's verdict when the report was scored.
type reportItem struct {
	Number   int
	URL      string
	Title    string
	Meta     string
	Evidence [][]reportSpan
	AIScore  string // "" when not judged
	AIKind   string
	AIReason string
}

// reportClass is one evidence class tally shown as a pill.
type reportClass struct {
	Name  string
	Count int
	Kind  string
}

// reportSection is one check: what it asks, what it found, and how to act on it.
type reportSection struct {
	Slug        string
	Question    string
	Description string
	Note        string // extra context line, e.g. what the rules protected
	Command     string // the CLI commands that act on this section
	Total       int    // every candidate the check found
	Classes     []reportClass
	Items       []reportItem
	Truncated   bool // --limit cut the item list short
}

type reportData struct {
	Repo        string
	GeneratedAt string
	WithAI      bool
	Total       int
	Sections    []reportSection
}

func span(t, kind string) reportSpan    { return reportSpan{Text: t, Kind: kind} }
func linkSpan(t, url string) reportSpan { return reportSpan{Text: t, URL: url} }
func (f *FlagData) prHTMLURL(n int) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", f.GH.Repo, n)
}

func (f *FlagData) issHTMLURL(n int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", f.GH.Repo, n)
}

// reportAIKind buckets a confidence for colouring, matching scoreTag's bands.
func reportAIKind(c float64) string {
	switch {
	case c >= 0.7:
		return kindOK
	case c >= 0.4:
		return kindMid
	default:
		return kindBad
	}
}

// limitFindings caps a check's candidates for cheap test runs — applied BEFORE
// any AI judging so --limit 10 costs ten verdicts, not the full set.
func limitFindings[T any](findings []T, limit int) ([]T, bool) {
	if limit > 0 && len(findings) > limit {
		return findings[:limit], true
	}
	return findings, false
}

// sortByVerdict orders findings surest-first once verdicts exist; unanswered
// candidates sink to the bottom.
func sortByVerdict[T any](findings []T, number func(*T) int, verdicts map[int]*issue.Verdict) {
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

// attachVerdict fills the AI fields on one item.
func attachVerdict(item *reportItem, v *issue.Verdict) {
	if v == nil {
		return
	}
	item.AIScore = fmt.Sprintf("%.2f", v.Confidence)
	item.AIKind = reportAIKind(v.Confidence)
	item.AIReason = text.OneLine(v.Reason)
}

// Report writes report.html: every close candidate each check sees (fixed,
// resolved, comments, exists, legacy, deprecated), with the evidence for why it
// is listed and links to
// everything cited. --with-ai scores each candidate with the check's judge
// (cached verdicts are reused) and sorts surest first; --limit N keeps test
// runs cheap. The old analyse-based report and its decisions.csv are gone —
// the checks' apply modes are the review flow now.
func (f *FlagData) Report() error {
	o := f.Cmd.Report
	if !f.NoAutoFetch {
		if err := f.Fetch(false); err != nil {
			return err
		}
	}
	if o.WithAI && !f.AI.Enabled {
		return errors.New("--with-ai needs the AI (--ai=false is set)")
	}

	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	now := time.Now()
	data := reportData{Repo: f.GH.Repo, WithAI: o.WithAI, GeneratedAt: now.Format("2006-01-02 15:04")}

	fixed, err := f.fixedReportSection(d, o, now)
	if err != nil {
		return err
	}
	resolved, err := f.resolvedReportSection(d, o, now)
	if err != nil {
		return err
	}
	legacy, err := f.legacyReportSection(d, o, now)
	if err != nil {
		return err
	}
	deprecated, err := f.deprecatedReportSection(d, o, now)
	if err != nil {
		return err
	}
	comments, err := f.commentsReportSection(d, o, now)
	if err != nil {
		return err
	}
	exists, err := f.existsReportSection(d, o, now)
	if err != nil {
		return err
	}
	duplicates, err := f.duplicatesReportSection(d, o, now)
	if err != nil {
		return err
	}
	data.Sections = []reportSection{fixed, resolved, duplicates, comments, exists, legacy, deprecated}
	for _, s := range data.Sections {
		data.Total += s.Total
	}
	if data.Total == 0 {
		cout.Printf("no close candidates in any check — is the db fetched? (<cyan>koi fetch</>)\n")
		return nil
	}

	if err := os.MkdirAll(o.Out, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", o.Out, err)
	}
	htmlPath := filepath.Join(o.Out, "report.html")
	if err := writeReportHTML(htmlPath, &data); err != nil {
		return err
	}
	cout.Printf("\nwrote <cyan>%s</> — <yellow>%d</> close candidates <gray>(fixed %d · resolved %d · duplicates %d · comments %d · exists %d · legacy %d · deprecated %d)</>\n",
		htmlPath, data.Total, fixed.Total, resolved.Total, duplicates.Total, comments.Total, exists.Total, legacy.Total, deprecated.Total)
	if !o.WithAI {
		cout.Printf("<gray>rerun with</> <cyan>--with-ai</> <gray>to score every candidate, or</> <cyan>--limit 10</> <gray>to test cheaply</>\n")
	}
	// a file:// url so the terminal makes the path clickable
	if abs, aerr := filepath.Abs(htmlPath); aerr == nil {
		cout.Printf("<gray>open:</> <cyan>file://%s</>\n", abs)
	}
	return nil
}

// fixedReportSection builds the "a merged PR touches this" check section.
func (f *FlagData) fixedReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	s := reportSection{
		Slug:     passFixed,
		Question: "a merged PR touches this open issue — did it fix it?",
		Description: "Every open issue referenced by a merged same-repository pull request: the issue looks fixed but nobody closed it. " +
			"fixed-by means the PR declared it closes the issue with a closing keyword; mentioned-by is a bare mention. " +
			"Applying closes with a comment citing the fix PR and its shipped release, closed as completed.",
		Command: "koi fixed [fixed-by|mentioned-by] --apply / --apply-with-ai / --apply-with-ai-auto",
	}
	findings, counts, prVersions, _, err := f.collectFixed(d, "")
	if err != nil {
		return s, err
	}
	s.Total = len(findings)
	s.Classes = []reportClass{
		{classFixedBy, counts[classFixedBy], kindOK},
		{classMentionedBy, counts[classMentionedBy], kindWarn},
	}

	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.fixedJudgeItems(d, findings, prVersions)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passFixed, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *fixedFinding) int { return x.issue.Number }, verdicts)
	}

	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.issue.UpdatedAt, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		for n := range fdg.prs {
			pr := &fdg.prs[n]
			row := make([]reportSpan, 0, 4)
			if pr.WillClose {
				row = append(row, span("fixed by", kindOK))
			} else {
				row = append(row, span("mentioned by", kindWarn))
			}
			row = append(row, linkSpan(fmt.Sprintf("PR #%d", pr.RefNumber), f.prHTMLURL(pr.RefNumber)))
			if vs := prVersions[pr.RefNumber]; len(vs) > 0 {
				row = append(row, span("shipped in v"+vs[0], kindVer))
			} else {
				row = append(row, span("merged, not yet in a release", kindDim))
			}
			row = append(row, span("· "+text.OneLine(pr.Title), kindDim))
			item.Evidence = append(item.Evidence, row)
		}
		if fdg.reopenedBy != 0 {
			item.Evidence = append(item.Evidence, []reportSpan{
				span(fmt.Sprintf("closed by PR #%d and then reopened — the fix may not have stuck", fdg.reopenedBy), kindBad),
			})
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

// resolvedReportSection builds the "a linked issue was dealt with" check section.
func (f *FlagData) resolvedReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	s := reportSection{
		Slug:     passResolved,
		Question: "a linked issue was dealt with — does its outcome cover this open one?",
		Description: "Every open issue that cross-references a CLOSED issue in the same repository: likely duplicates of something already dealt with. " +
			"Classes by how the linked issue was closed: completed (with the fixing PR and release when the changelog records them), duplicate, then not planned. " +
			"Applying closes as a duplicate pointing at the linked issue and its resolution.",
		Command: "koi resolved [completed|duplicate|not-planned] --apply / --apply-with-ai / --apply-with-ai-auto",
	}
	findings, counts, _, err := f.collectResolved(d, "")
	if err != nil {
		return s, err
	}
	s.Total = len(findings)
	s.Classes = []reportClass{
		{classCompleted, counts[classCompleted], kindOK},
		{classDuplicate, counts[classDuplicate], kindMid},
		{classNotPlanned, counts[classNotPlanned], kindWarn},
	}

	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.resolvedJudgeItems(d, findings)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passResolved, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *resolvedFinding) int { return x.issue.Number }, verdicts)
	}

	classKind := map[string]string{classCompleted: kindOK, classDuplicate: kindMid, classNotPlanned: kindWarn}
	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.issue.UpdatedAt, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		for n := range fdg.targets {
			t := &fdg.targets[n]
			class := resolvedClass(t.stateReason)
			row := make([]reportSpan, 0, 6)
			row = append(row,
				span("links", kindDim),
				linkSpan(fmt.Sprintf("#%d", t.ref.RefNumber), f.issHTMLURL(t.ref.RefNumber)),
				span("closed "+strings.ReplaceAll(class, "-", " "), classKind[class]))
			if t.closedAt != "" {
				row = append(row, span(dateOf(t.closedAt), kindDim))
			}
			switch {
			case t.fixPR != 0:
				row = append(row, span("by", kindDim), linkSpan(fmt.Sprintf("PR #%d", t.fixPR), f.prHTMLURL(t.fixPR)))
				if t.version != "" {
					row = append(row, span("in v"+t.version, kindVer))
				} else if t.milestone != "" {
					row = append(row, span(t.milestone, kindVer))
				}
			case class == classCompleted:
				row = append(row, span("(no fix recorded)", kindBad))
			}
			row = append(row, span("· "+text.OneLine(t.ref.Title), kindDim))
			item.Evidence = append(item.Evidence, row)
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

// legacyReportSection builds the "this bug is old and unconfirmed" check section.
func (f *FlagData) legacyReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	col, err := f.collectLegacy(d, nil)
	if err != nil {
		return reportSection{Slug: passLegacy}, err
	}
	s := reportSection{
		Slug:     passLegacy,
		Question: fmt.Sprintf("this bug is old (v1–v%d) and nobody says it is still alive — close as stale?", col.maxMajor),
		Description: fmt.Sprintf("Open bug and crash reports against legacy majors (v1–v%d) that the keep rules cleared for closing: "+
			"no credible recent-version repro claim, no open linked PR, not highly engaged. Enhancements are not touched. "+
			"Applying closes with the legacy-bug comment, closed as not planned.", col.maxMajor),
		Command: "koi legacy [--major N] --apply / --apply-with-ai / --apply-with-ai-auto",
		Total:   len(col.findings),
	}
	for m := 1; m <= col.maxMajor; m++ {
		if col.byMajor[m] > 0 {
			s.Classes = append(s.Classes, reportClass{fmt.Sprintf("v%d.x", m), col.byMajor[m], kindVer})
		}
	}
	if col.legacyBugs > 0 {
		parts := make([]string, 0, len(col.protected)+1)
		for _, r := range text.SortedKeys(col.protected) {
			parts = append(parts, fmt.Sprintf("%s %d", r, col.protected[r]))
		}
		note := fmt.Sprintf("%d legacy bugs seen in total", col.legacyBugs)
		if len(parts) > 0 {
			note += " · protected from closing: " + strings.Join(parts, " · ")
		}
		if col.diverted > 0 {
			note += fmt.Sprintf(" · %d have a merged PR and belong to koi fixed", col.diverted)
		}
		s.Note = note
	}

	findings := col.findings
	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.legacyJudgeItems(d, findings)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passLegacy, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *legacyFinding) int { return x.issue.Number }, verdicts)
	}

	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.signals.LastActivity, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		kindTag := kindWarn
		if fdg.signals.Kind == signalKindCrash {
			kindTag = kindBad
		}
		item.Evidence = append(item.Evidence, []reportSpan{
			span(fdg.signals.Kind, kindTag),
			span("on v"+text.OrDefault(fdg.signals.VersionFull, fmt.Sprintf("%d.x", fdg.signals.VersionMajor)), kindVer),
			span("("+fdg.signals.VersionSource+")", kindDim),
		})
		// a label-sourced quote is just "labelled v/3.x" — the source tag above
		// already says that; only body/template/ai quotes add anything
		if fdg.signals.VersionQuote != "" && fdg.signals.VersionSource != versionSourceLabel {
			item.Evidence = append(item.Evidence, []reportSpan{
				span("version evidence:", kindDim),
				span("“"+text.TruncateRunes(text.OneLine(fdg.signals.VersionQuote), 120)+"”", kindQuote),
			})
		}

		// the thread's version trail is what decides a staleness close: every
		// version claim with its quote and deep link, or the silence spelt out
		comments, cerr := d.CommentsFor(fdg.issue.Number)
		if cerr != nil {
			return s, cerr
		}
		mentions := issue.VersionMentions(comments)
		const maxMentions = 6
		for n, m := range mentions {
			if n == maxMentions {
				item.Evidence = append(item.Evidence, []reportSpan{
					span(fmt.Sprintf("… and %d more version mentions in the thread", len(mentions)-maxMentions), kindDim),
				})
				break
			}
			row := []reportSpan{
				span(fmt.Sprintf("v%d.x", m.Major), kindVer),
				span(fmt.Sprintf("%s ago · @%s:", text.HumanAge(m.At, now), m.Author), kindDim),
				span("“"+text.TruncateRunes(text.OneLine(m.Quote), 110)+"”", kindQuote),
			}
			if m.URL != "" {
				row = append(row, linkSpan("view comment", m.URL))
			}
			item.Evidence = append(item.Evidence, row)
		}
		if len(mentions) == 0 && len(comments) > 0 {
			item.Evidence = append(item.Evidence, []reportSpan{
				span(fmt.Sprintf("no version mentions in the thread's %d comments — nobody re-confirmed on a newer version", len(comments)), kindDim),
			})
		}
		// how the thread ended is the other half of the staleness call
		if len(comments) > 0 {
			last := &comments[len(comments)-1]
			row := []reportSpan{
				span(fmt.Sprintf("last comment %s ago · @%s:", text.HumanAge(last.CreatedAt, now), last.Author), kindDim),
				span("“"+text.TruncateRunes(text.OneLine(issue.CleanBody(last.Body)), 140)+"”", kindQuote),
			}
			if last.URL != "" {
				row = append(row, linkSpan("view comment", last.URL))
			}
			item.Evidence = append(item.Evidence, row)
		} else {
			item.Evidence = append(item.Evidence, []reportSpan{span("no comments at all", kindDim)})
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

// commentsReportSection builds the "its own thread says it is done" section.
func (f *FlagData) commentsReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	s := reportSection{
		Slug:     passComments,
		Question: "this issue's own thread says it can be closed — is the claim credible?",
		Description: "Every open issue with a comment claiming it is done: \"this can be closed\", \"fixed in vX by #PR\", " +
			"\"no longer an issue\", or a maintainer saying they will close it. Classes by who says so. " +
			"Applying closes as completed with a comment citing the claim and its author.",
		Command: "koi comments [maintainer-says|community-says] --apply / --apply-with-ai / --apply-with-ai-auto",
	}
	findings, counts, _, err := f.collectComments(d, "")
	if err != nil {
		return s, err
	}
	s.Total = len(findings)
	s.Classes = []reportClass{
		{classMaintainerSays, counts[classMaintainerSays], kindOK},
		{classCommunitySays, counts[classCommunitySays], kindWarn},
	}

	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.commentsJudgeItems(d, findings)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passComments, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *commentsFinding) int { return x.issue.Number }, verdicts)
	}

	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.issue.UpdatedAt, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		for n := range fdg.claims {
			cl := &fdg.claims[n]
			authorKind := kindWarn
			assocName := ""
			switch _, label := assocDisplay(cl.comment.AuthorAssociation); {
			case maintainerAssoc(cl.comment.AuthorAssociation):
				authorKind, assocName = kindOK, "("+label+") "
			case label != "":
				authorKind, assocName = "", "("+label+") "
			}
			row := []reportSpan{
				span("@"+cl.comment.Author, authorKind),
				span(assocName+text.HumanAge(cl.comment.CreatedAt, now)+" ago:", kindDim),
				span("“"+cl.quote+"”", kindQuote),
			}
			if cl.prNumber != 0 {
				row = append(row, linkSpan(fmt.Sprintf("PR #%d", cl.prNumber), f.prHTMLURL(cl.prNumber)),
					span("shipped in v"+cl.prVersion, kindVer))
			}
			if cl.comment.URL != "" {
				row = append(row, linkSpan("view comment", cl.comment.URL))
			}
			item.Evidence = append(item.Evidence, row)
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

// existsReportSection builds the "the ask already exists" section.
func (f *FlagData) existsReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	s := reportSection{
		Slug:     passExists,
		Question: "this enhancement request appears to already exist in the provider — did it ship?",
		Description: "Open enhancement requests whose ask already exists: the requested resource or data source is in the " +
			"provider docs today and arrived after the request, or a property the request's prose names shipped for one of " +
			"its resources in a later release. Applying closes as completed with the good news and a documentation link.",
		Command: "koi exists [resource|property] --apply / --apply-with-ai / --apply-with-ai-auto",
	}
	findings, counts, _, err := f.collectExists(d, "")
	if err != nil {
		return s, err
	}
	s.Total = len(findings)
	s.Classes = []reportClass{
		{classExistsResource, counts[classExistsResource], kindOK},
		{classExistsProperty, counts[classExistsProperty], kindMid},
	}

	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.existsJudgeItems(d, findings)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passExists, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *existsFinding) int { return x.issue.Number }, verdicts)
	}

	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.issue.UpdatedAt, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		for n := range fdg.evidence {
			e := &fdg.evidence[n]
			// evidence is dated when a changelog bullet names it and undated
			// when only the docs do — the row must not claim a release either
			// way it does not have
			var row []reportSpan
			if e.kind == db.RemovalKindProperty {
				row = []reportSpan{span(e.name, ""), span("on "+e.resource, kindDim)}
				if e.version != "" {
					row = append(row, span("shipped in v"+e.version, kindVer), linkSpan(fmt.Sprintf("PR #%d", e.pr), f.prHTMLURL(e.pr)))
				} else {
					row = append(row, span("in the docs today", kindOK),
						linkSpan("docs", registryDocURL(db.DocKindResource, e.resource)))
				}
			} else {
				row = []reportSpan{
					span(e.name, kindOK), span("("+strings.ReplaceAll(e.kind, "-", " ")+")", kindDim),
					span("now exists", kindOK),
				}
				if e.version != "" {
					row = append(row, span("arrived in v"+e.version, kindVer), linkSpan(fmt.Sprintf("PR #%d", e.pr), f.prHTMLURL(e.pr)))
				} else {
					row = append(row, span("— arrival not dated", kindDim))
				}
				row = append(row, linkSpan("docs", registryDocURL(e.kind, e.name)))
			}
			item.Evidence = append(item.Evidence, row,
				[]reportSpan{span("changelog:", kindDim), span("“"+e.bullet+"”", kindQuote)})
			if e.quote != "" {
				item.Evidence = append(item.Evidence, []reportSpan{span("the ask:", kindDim), span("“"+e.quote+"”", kindQuote)})
			}
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

// duplicatesReportSection builds the "this is another open issue" section.
func (f *FlagData) duplicatesReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	s := reportSection{
		Slug:     passDuplicates,
		Question: "this looks like an older open issue — is it the same one?",
		Description: "Open issues that duplicate another OPEN issue: this one references it, or nobody linked them and " +
			"the titles say the same thing. The issue with more engagement survives, weighted towards the older one; " +
			"applying closes this one as a duplicate pointing at it. Duplicates of already-closed issues belong to " +
			"the resolved check.",
		Command: "koi duplicates [linked|similar] --apply / --apply-with-ai / --apply-with-ai-auto",
	}
	findings, counts, _, err := f.collectDuplicates(d, "")
	if err != nil {
		return s, err
	}
	s.Total = len(findings)
	s.Classes = []reportClass{
		{classDupLinked, counts[classDupLinked], kindOK},
		{classDupSimilar, counts[classDupSimilar], kindMid},
	}

	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.duplicatesJudgeItems(d, findings)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passDuplicates, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *duplicateFinding) int { return x.issue.Number }, verdicts)
	}

	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.issue.UpdatedAt, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		for n := range fdg.targets {
			t := &fdg.targets[n]
			how, kind := "referenced from this issue", kindOK
			if t.class == classDupSimilar {
				how, kind = fmt.Sprintf("%.0f%% title match, nothing links them", t.similarity*100), kindMid
			}
			item.Evidence = append(item.Evidence, []reportSpan{
				linkSpan(fmt.Sprintf("#%d", t.issue.Number), t.issue.URL),
				span(how, kind),
				span(fmt.Sprintf("opened %s ago · 💬 %d · 👍 %d", text.HumanAge(t.issue.CreatedAt, now), t.issue.CommentCount, t.issue.ThumbsUp), kindDim),
			}, []reportSpan{span("“"+text.OneLine(t.issue.Title)+"”", kindQuote)})
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

// deprecatedReportSection builds the "the thing it leans on is gone" section.
func (f *FlagData) deprecatedReportSection(d *db.DB, o FlagsReport, now time.Time) (reportSection, error) {
	s := reportSection{
		Slug:     passDeprecated,
		Question: "this issue leans on a removed or deprecated resource/property — moot where it stands?",
		Description: "Every open issue referencing a resource, data source, or property that was removed or deprecated, " +
			"per the 4.0/5.0 upgrade guides and the changelog's DEPRECATIONS bullets. " +
			"Applying closes as not planned with a comment naming what is gone, when, and the successor to use.",
		Command: "koi deprecated [removed-resource|removed-property|...] --apply / --apply-with-ai / --apply-with-ai-auto",
	}
	findings, counts, _, _, err := f.collectDeprecated(d, "")
	if err != nil {
		return s, err
	}
	s.Total = len(findings)
	s.Classes = []reportClass{
		{classRemovedResource, counts[classRemovedResource], kindBad},
		{classRemovedProperty, counts[classRemovedProperty], kindWarn},
		{classDeprecatedResource, counts[classDeprecatedResource], kindMid},
		{classDeprecatedProperty, counts[classDeprecatedProperty], kindDim},
	}

	findings, s.Truncated = limitFindings(findings, o.Limit)
	var verdicts map[int]*issue.Verdict
	if o.WithAI && len(findings) > 0 {
		promptText, items, jerr := f.deprecatedJudgeItems(d, findings)
		if jerr != nil {
			return s, jerr
		}
		if verdicts, err = f.judgeBlocks(d, passDeprecated, promptText, items, nil, nil); err != nil {
			return s, err
		}
		sortByVerdict(findings, func(x *deprecatedFinding) int { return x.issue.Number }, verdicts)
	}

	for i := range findings {
		fdg := &findings[i]
		item := reportItem{
			Number: fdg.issue.Number, URL: fdg.issue.URL, Title: text.OneLine(fdg.issue.Title),
			Meta: fmt.Sprintf("opened %s ago · last activity %s ago · 💬 %d · 👍 %d",
				text.HumanAge(fdg.issue.CreatedAt, now), text.HumanAge(fdg.issue.UpdatedAt, now),
				fdg.issue.CommentCount, fdg.issue.ThumbsUp),
		}
		for n := range fdg.matches {
			m := &fdg.matches[n]
			r := m.removal
			actionKind := kindMid
			if r.Action == db.RemovalRemoved {
				actionKind = kindBad
			}
			row := make([]reportSpan, 0, 6)
			if r.Kind == db.RemovalKindProperty {
				row = append(row, span(r.Property, ""), span("on "+r.Resource, kindDim))
			} else {
				row = append(row, span(r.Resource, ""), span("("+strings.ReplaceAll(r.Kind, "-", " ")+")", kindDim))
			}
			if v, ok := strings.CutPrefix(r.Source, "changelog "); ok {
				row = append(row, span(r.Action, actionKind), linkSpan("in "+v, removalURL(r)))
			} else {
				row = append(row, span(r.Action, actionKind), linkSpan(fmt.Sprintf("in v%d.0 (%s)", r.Major, r.Source), removalURL(r)))
			}
			if r.Successor != "" {
				row = append(row, span("· use "+r.Successor, kindOK))
			}
			item.Evidence = append(item.Evidence, row)
			if m.quote != "" {
				item.Evidence = append(item.Evidence, []reportSpan{
					span("matched:", kindDim), span("“"+m.quote+"”", kindQuote),
				})
			}
		}
		if len(fdg.alive) > 0 {
			item.Evidence = append(item.Evidence, []reportSpan{
				span("also references (not removed or deprecated):", kindDim),
				span(strings.Join(fdg.alive, " · "), kindOK),
			})
		}
		attachVerdict(&item, verdicts[fdg.issue.Number])
		s.Items = append(s.Items, item)
	}
	return s, nil
}

func writeReportHTML(path string, data *reportData) error {
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

// Import reads a filled-in decisions.csv and records approve/reject decisions.
func (f *FlagData) Import(path string) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	file, err := os.Open(path) //nolint:gosec // G304: user-chosen input path is the point
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if len(rows) < 2 {
		return fmt.Errorf("%s has no data rows", path)
	}

	// column positions from the header, so column re-ordering in a spreadsheet is fine
	col := map[string]int{}
	for n, h := range rows[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = n
	}
	numberCol, ok := col["number"]
	if !ok {
		return fmt.Errorf("%s has no 'number' column", path)
	}
	decisionCol, ok := col["decision"]
	if !ok {
		return fmt.Errorf("%s has no 'decision' column", path)
	}

	decider := f.Decider()
	approved, rejected, blank, unknown := 0, 0, 0, 0

	for _, row := range rows[1:] {
		if len(row) <= numberCol || len(row) <= decisionCol {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(row[numberCol]))
		if err != nil {
			continue
		}

		a, err := d.GetAction(number)
		if err != nil {
			return err
		}
		if a == nil || a.Status != db.StatusProposed {
			continue // decided elsewhere, or unknown issue
		}

		switch strings.ToLower(strings.TrimSpace(row[decisionCol])) {
		case "approve", "approved", "yes", "y", "close":
			if err := d.DecideAction(a.ID, db.StatusApproved, decider); err != nil {
				return err
			}
			approved++
		case "reject", "rejected", "no", "n", "keep":
			if err := d.DecideAction(a.ID, db.StatusRejected, decider); err != nil {
				return err
			}
			rejected++
		case "":
			blank++
		default:
			unknown++
			cout.Errorf("  <yellow>#%d:</> unrecognised decision %q\n", number, row[decisionCol])
		}
	}

	cout.Printf("imported as <bold>%s</>: <green>%d approved</> · <red>%d rejected</> · %d blank · %d unrecognised\n",
		decider, approved, rejected, blank, unknown)
	if approved > 0 {
		cout.Printf("next: <cyan>koi apply</>\n")
	}
	return nil
}

// Column names shared by every csv this tool writes.
const (
	csvColNumber = "number"
	csvColTitle  = "title"
	csvColURL    = "url"
)
