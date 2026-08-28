package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/go-klose-old-issues/assets"
	"github.com/katbyte/go-klose-old-issues/lib/cout"
	"github.com/katbyte/go-klose-old-issues/lib/db"
)

type reportEvidence struct {
	Key, Value string
}

// reportPR is a linked PR in the triaged repo (foreign-repo mentions are filtered out).
type reportPR struct {
	Number    int
	URL       string
	Title     string
	State     string // open | merged | closed
	Release   string // release that shipped it per the changelog, "" if unknown
	WillClose bool   // the reference carries a closing keyword ("fixes #N")
}

// reportMention is one version claim found in the thread, deep-linked to its comment.
type reportMention struct {
	Version string
	Age     string
	Author  string
	Quote   string
	URL     string
}

type reportItem struct {
	Number       int
	URL          string
	Title        string
	Age          string
	LastActivity string
	Kind         string
	Version      string
	ThumbsUp     int
	Comments     int
	Confidence   string
	Template     string
	Evidence     []reportEvidence
	LinkedPRs    []reportPR
	OpenPRs      int
	Mentions     []reportMention
	AIRec        string // classify-pass recommendation, e.g. "close/legacy-bug"
	AIConf       string
	AIQuote      string // the thread quote the AI cited as its supporting evidence
	AIStill      string // still-open pass verdict text, "" if the pass hasn't run
	AIStillQuote string
}

type reportGroup struct {
	Reason string
	Action string
	Count  int
	Items  []reportItem
}

type reportData struct {
	Repo        string
	GeneratedAt string
	Total       int
	Groups      []reportGroup
}

// Report writes report.html (for reading) and decisions.csv (for deciding) so the
// community manager can review async — no terminal required. Decisions come back
// via `koi import`.
func (f *FlagData) Report(outDir string) error {
	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if err := f.ensureAnalysed(d); err != nil {
		return err
	}

	actions, err := d.Actions(db.ActionFilter{Status: db.StatusProposed})
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		cout.Printf("no proposed actions to report — is the db fetched? (<cyan>koi fetch</>)\n")
		return nil
	}

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	now := time.Now()
	groups := map[string]*reportGroup{}
	var csvRows [][]string

	for _, a := range actions {
		card, err := f.loadCard(d, a)
		if err != nil {
			return err
		}
		i, s := card.issue, card.signals

		item := reportItem{
			Number:       i.Number,
			URL:          i.URL,
			Title:        i.Title,
			Age:          humanAge(i.CreatedAt, now),
			LastActivity: humanAge(s.LastActivity, now),
			Kind:         s.Kind,
			ThumbsUp:     i.ThumbsUp,
			Comments:     i.CommentCount,
			Confidence:   fmt.Sprintf("%.2f", a.Confidence),
			Template:     a.Template,
		}
		if s.VersionMajor > 0 {
			item.Version = versionText(s)
		}
		for _, k := range sortedKeys(a.Evidence) {
			item.Evidence = append(item.Evidence, reportEvidence{Key: k, Value: a.Evidence[k]})
		}

		for _, r := range card.prs {
			pr := reportPR{
				Number:    r.RefNumber,
				URL:       fmt.Sprintf("https://github.com/%s/pull/%d", f.GH.Repo, r.RefNumber),
				Title:     r.Title,
				WillClose: r.WillClose,
			}
			switch {
			case r.Merged:
				pr.State, pr.Release = "merged", card.releases[r.RefNumber]
			case r.State == db.IssueOpen:
				pr.State = "open"
				item.OpenPRs++
			default:
				pr.State = "closed"
			}
			item.LinkedPRs = append(item.LinkedPRs, pr)
		}

		if len(card.mentions) > 1 {
			for _, m := range card.mentions {
				item.Mentions = append(item.Mentions, reportMention{
					Version: fmt.Sprintf("v%d.x", m.Major),
					Age:     humanAge(m.At, now),
					Author:  m.Author,
					Quote:   m.Quote,
					URL:     m.URL,
				})
			}
		}

		if v, ok := card.verdicts[passClassify]; ok {
			var fields map[string]any
			if json.Unmarshal([]byte(v.Verdict), &fields) == nil {
				item.AIRec, _ = fields["recommendation"].(string)
				item.AIQuote, _ = fields["quote"].(string)
				item.AIConf = fmt.Sprintf("%.2f", v.Confidence)
			}
		}
		if v, ok := card.verdicts[passStillOpen]; ok {
			var fields map[string]any
			if json.Unmarshal([]byte(v.Verdict), &fields) == nil {
				if claim, _ := fields["still_claim"].(bool); claim {
					item.AIStill = "found a claim it still occurs"
				} else {
					item.AIStill = "found no recent-version claims"
				}
				item.AIStillQuote, _ = fields["quote"].(string)
			}
		}

		key := a.Action + "/" + a.Reason
		g, ok := groups[key]
		if !ok {
			g = &reportGroup{Reason: a.Reason, Action: a.Action}
			groups[key] = g
		}
		g.Items = append(g.Items, item)
		g.Count++

		csvRows = append(csvRows, []string{
			strconv.Itoa(i.Number), a.Action, a.Reason, fmt.Sprintf("%.2f", a.Confidence),
			oneLine(i.Title), i.URL, "", "",
		})
	}

	data := reportData{
		Repo:        f.GH.Repo,
		GeneratedAt: now.Format("2006-01-02 15:04"),
		Total:       len(actions),
	}
	for _, k := range sortedKeys(groups) {
		data.Groups = append(data.Groups, *groups[k])
	}
	// closes first, biggest groups first within each action
	sort.SliceStable(data.Groups, func(a, b int) bool {
		if data.Groups[a].Action != data.Groups[b].Action {
			return data.Groups[a].Action < data.Groups[b].Action
		}
		return data.Groups[a].Count > data.Groups[b].Count
	})

	htmlPath := filepath.Join(outDir, "report.html")
	if err := writeReportHTML(htmlPath, &data); err != nil {
		return err
	}

	csvPath := filepath.Join(outDir, "decisions.csv")
	if err := writeDecisionsCSV(csvPath, csvRows); err != nil {
		return err
	}

	cout.Printf("wrote <cyan>%s</> (%d proposals) and <cyan>%s</>\n", htmlPath, len(actions), csvPath)
	cout.Printf("review flow: read the html, fill the <bold>decision</> column (approve/reject) in the csv, then <cyan>koi import %s --as name</>\n", csvPath)
	return nil
}

func writeReportHTML(path string, data *reportData) error {
	tmpl, err := template.New("report").Parse(assets.ReportHTML())
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

var csvHeader = []string{"number", "action", "reason", "confidence", "title", "url", "decision", "notes"}

func writeDecisionsCSV(path string, rows [][]string) error {
	out, err := os.Create(path) //nolint:gosec // G304: user-chosen output path is the point
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer func() { _ = out.Close() }()

	w := csv.NewWriter(out)
	if err := w.Write(csvHeader); err != nil {
		return fmt.Errorf("writing csv header: %w", err)
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return fmt.Errorf("writing csv row: %w", err)
		}
	}
	w.Flush()
	return w.Error()
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
