package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/ai"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	passClassify  = "classify"
	passStillOpen = "still-open"

	// prompt template names — specific about what each pass judges; verdict
	// pass keys above stay short and stable so caches survive renames
	promptClassify  = "issue-classify"
	promptStillOpen = "issue-still-open"
	passAll         = "all"

	kindUnknown = "unknown"

	aiBatchSize     = 12
	maxConsecFails  = 3
	bodyRunesForAI  = 1200
	commentRunesFor = 400
)

// Classify runs the AI passes. classify determines kind/version/recommendation
// for rules-undetermined issues; still-open re-checks every proposed close with
// comment activity for credible recent-version claims. Verdicts are cached by
// (issue, pass, prompt-hash + updated_at) so re-runs only pay for changes.
func (f *FlagData) Classify(pass string, limit int) error {
	if !f.AI.Enabled {
		return errors.New("ai is disabled (--ai=false); the classify passes need it")
	}

	d, err := f.OpenDB()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if err := f.ensureAnalysed(d); err != nil {
		return err
	}

	a := f.NewAI()

	if pass == passClassify || pass == passAll {
		if err := f.runClassify(d, a, limit); err != nil {
			return err
		}
	}
	if pass == passStillOpen || pass == passAll {
		if err := f.runStillOpen(d, a, limit); err != nil {
			return err
		}
	}
	if pass != passClassify && pass != passStillOpen && pass != passAll {
		return fmt.Errorf("unknown pass %q: expected classify, still-open, or all", pass)
	}
	return nil
}

// target is one issue queued for an AI pass.
type target struct {
	issue    *db.Issue
	action   *db.Action
	comments []db.Comment
	hash     string
}

// collectTargets loads issues for a pass, skipping ones whose cached verdict is current.
func (f *FlagData) collectTargets(d *db.DB, filter db.ActionFilter, pass, promptText string, limit int) ([]target, int, error) {
	actions, err := d.Actions(filter)
	if err != nil {
		return nil, 0, err
	}

	var targets []target
	skipped := 0
	for _, act := range actions {
		i, err := d.GetIssue(act.IssueNumber)
		if err != nil {
			return nil, 0, err
		}
		if i == nil || i.State != db.IssueOpen {
			continue
		}

		hash := verdictHash(promptText, i.UpdatedAt)
		v, err := d.GetVerdict(i.Number, pass)
		if err != nil {
			return nil, 0, err
		}
		if v != nil && v.PromptHash == hash {
			skipped++
			continue
		}

		comments, err := d.CommentsFor(i.Number)
		if err != nil {
			return nil, 0, err
		}
		if pass == passStillOpen && len(comments) == 0 {
			skipped++ // nothing to check: no comments means no claims
			continue
		}

		targets = append(targets, target{issue: i, action: act, comments: comments, hash: hash})
		if limit > 0 && len(targets) >= limit {
			break
		}
	}
	return targets, skipped, nil
}

func verdictHash(promptText string, updatedAt time.Time) string {
	h := sha256.Sum256([]byte(promptText + "|" + updatedAt.UTC().Format(time.RFC3339)))
	return hex.EncodeToString(h[:8])
}

// preparePrompt loads a prompt template and substitutes the version placeholders.
func (f *FlagData) preparePrompt(name string) (string, error) {
	p, err := assets.Prompt(name)
	if err != nil {
		return "", err
	}
	recent := fmt.Sprintf("%d or %d", f.CurrentMajor-1, f.CurrentMajor)
	p = strings.ReplaceAll(p, "{{CURRENT_MAJOR}}", strconv.Itoa(f.CurrentMajor))
	p = strings.ReplaceAll(p, "{{LEGACY_MAX}}", strconv.Itoa(f.CurrentMajor-2))
	p = strings.ReplaceAll(p, "{{RECENT_MAJORS}}", recent)
	return p, nil
}

// ---- classify pass ----

type classifyVerdict struct {
	Number         int     `json:"number"`
	Kind           string  `json:"kind"`
	VersionMajor   int     `json:"version_major"`
	StillRelevant  bool    `json:"still_relevant"`
	Recommendation string  `json:"recommendation"`
	Confidence     float64 `json:"confidence"`
	Quote          string  `json:"quote"`
}

func (f *FlagData) runClassify(d *db.DB, a ai.AI, limit int) error {
	promptText, err := f.preparePrompt(promptClassify)
	if err != nil {
		return err
	}

	targets, skipped, err := f.collectTargets(d,
		db.ActionFilter{Status: db.StatusProposed, Action: db.ActionHuman, Reason: triage.ReasonUndetermined},
		passClassify, promptText, limit)
	if err != nil {
		return err
	}

	cout.Printf("classify: <yellow>%d</> issues to judge (<gray>%d cached</>)\n", len(targets), skipped)
	if len(targets) == 0 {
		return nil
	}

	return f.runBatches(a, targets, promptText, classifyBlock, func(raw string, batch []target) error {
		var verdicts []classifyVerdict
		if err := ai.ExtractJSON(raw, &verdicts); err != nil {
			return err
		}
		byNumber := map[int]*classifyVerdict{}
		for i := range verdicts {
			byNumber[verdicts[i].Number] = &verdicts[i]
		}

		for _, t := range batch {
			v := byNumber[t.issue.Number]
			if v == nil {
				cout.Errorf("  <yellow>#%d:</> no verdict in response\n", t.issue.Number)
				continue
			}
			if err := f.applyClassifyVerdict(d, t, v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f *FlagData) applyClassifyVerdict(d *db.DB, t target, v *classifyVerdict) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshalling verdict for #%d: %w", t.issue.Number, err)
	}
	if err := d.SaveVerdict(&db.Verdict{
		IssueNumber: t.issue.Number, Pass: passClassify, PromptHash: t.hash,
		Model: aiIdent(f.AI.Cmd, f.AI.Model), Verdict: string(raw), Confidence: v.Confidence, CreatedAt: db.Now(),
	}); err != nil {
		return err
	}

	// AI fills in signals the deterministic pass couldn't; rules stay the decider
	s, err := d.GetSignals(t.issue.Number)
	if err != nil {
		return err
	}
	if s == nil {
		s = &db.Signals{IssueNumber: t.issue.Number}
	}
	if s.Kind == "" && v.Kind != "" && v.Kind != kindUnknown {
		s.Kind = v.Kind
	}
	if s.VersionMajor == 0 && v.VersionMajor > 0 {
		s.VersionMajor, s.VersionSource, s.VersionQuote = v.VersionMajor, "ai", v.Quote
	}
	if err := d.SaveSignals(s); err != nil {
		return err
	}

	proposal := triage.Propose(t.issue, s, f.RuleConfig())
	if proposal == nil {
		return nil
	}
	proposal.Source = "ai"
	if v.Quote != "" {
		proposal.Evidence["ai"] = v.Quote
	}

	// the AI recommendation gates the rules outcome: it can veto a close it
	// disagrees with, and its confidence caps a close it produced the version for
	switch {
	case proposal.Action == db.ActionClose && (v.StillRelevant || v.Recommendation == "keep"):
		proposal.Action, proposal.Reason, proposal.Confidence = db.ActionKeep, triage.ReasonAIKeep, v.Confidence
		proposal.StateReason, proposal.Template = "", ""
	case proposal.Action == db.ActionClose && v.Recommendation == "unknown":
		cout.Printf("  <cyan>#%-6d</> AI says <bold>unknown</> — stays human/undetermined <darkGray>%s</>\n", t.issue.Number, t.issue.URL)
		return nil // not confident enough to act on; stays human/undetermined
	case proposal.Action == db.ActionClose:
		if v.Confidence < proposal.Confidence {
			proposal.Confidence = v.Confidence
		}
	}

	line := fmt.Sprintf("<lightBlue>%s</>/%s", proposal.Action, proposal.Reason)
	if proposal.Action == db.ActionKeep && proposal.Reason == triage.ReasonAIKeep {
		line = "<fg=208>keep/ai-keep — AI vetoed the close</>"
	}
	quote := ""
	if v.Quote != "" {
		quote = fmt.Sprintf(" — <gray>%q</>", text.TruncateRunes(v.Quote, 60))
	}
	cout.Printf("  <cyan>#%-6d</> AI says <bold>%s</> %s → %s%s <darkGray>%s</>\n",
		t.issue.Number, v.Recommendation, confidenceColoured(v.Confidence), line, quote, t.issue.URL)

	_, err = d.ProposeAction(proposal)
	return err
}

// classifyBlock renders one issue for the classify prompt: body + informative comments.
func classifyBlock(t target) string {
	var b strings.Builder
	i := t.issue
	fmt.Fprintf(&b, "### Issue #%d: %s\n", i.Number, i.Title)
	fmt.Fprintf(&b, "labels: %s | opened: %s | comments: %d\n", strings.Join(i.Labels, ", "), i.CreatedAt.Format("2006-01-02"), i.CommentCount)
	fmt.Fprintf(&b, "BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(i.Body), bodyRunesForAI))

	picked := digestComments(t.comments, 5)
	if len(picked) > 0 {
		fmt.Fprintf(&b, "COMMENTS (%d of %d):\n", len(picked), len(t.comments))
		for _, c := range picked {
			fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
				text.TruncateRunes(text.OneLine(triage.CleanBody(c.Body)), commentRunesFor))
		}
	}
	return b.String()
}

// ---- still-open pass ----

type stillOpenVerdict struct {
	Number       int     `json:"number"`
	StillClaim   bool    `json:"still_claim"`
	ClaimedMajor int     `json:"claimed_major"`
	Confidence   float64 `json:"confidence"`
	Quote        string  `json:"quote"`
}

func (f *FlagData) runStillOpen(d *db.DB, a ai.AI, limit int) error {
	promptText, err := f.preparePrompt(promptStillOpen)
	if err != nil {
		return err
	}

	targets, skipped, err := f.collectTargets(d,
		db.ActionFilter{Status: db.StatusProposed, Action: db.ActionClose},
		passStillOpen, promptText, limit)
	if err != nil {
		return err
	}

	cout.Printf("still-open: <yellow>%d</> close candidates to double-check (<gray>%d cached or comment-free</>)\n", len(targets), skipped)
	if len(targets) == 0 {
		return nil
	}

	return f.runBatches(a, targets, promptText, stillOpenBlock, func(raw string, batch []target) error {
		var verdicts []stillOpenVerdict
		if err := ai.ExtractJSON(raw, &verdicts); err != nil {
			return err
		}
		byNumber := map[int]*stillOpenVerdict{}
		for i := range verdicts {
			byNumber[verdicts[i].Number] = &verdicts[i]
		}

		for _, t := range batch {
			v := byNumber[t.issue.Number]
			if v == nil {
				cout.Errorf("  <yellow>#%d:</> no verdict in response\n", t.issue.Number)
				continue
			}
			if err := f.applyStillOpenVerdict(d, t, v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f *FlagData) applyStillOpenVerdict(d *db.DB, t target, v *stillOpenVerdict) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshalling verdict for #%d: %w", t.issue.Number, err)
	}
	if err := d.SaveVerdict(&db.Verdict{
		IssueNumber: t.issue.Number, Pass: passStillOpen, PromptHash: t.hash,
		Model: aiIdent(f.AI.Cmd, f.AI.Model), Verdict: string(raw), Confidence: v.Confidence, CreatedAt: db.Now(),
	}); err != nil {
		return err
	}

	// a credible recent-version claim flips the close to keep
	if v.StillClaim && v.ClaimedMajor >= f.CurrentMajor-1 && v.Confidence >= 0.5 {
		cout.Printf("  <cyan>#%d</> <fg=208>flipped to keep:</> <bold>still an issue on v%d</> — <gray>%q</> <darkGray>%s</>\n",
			t.issue.Number, v.ClaimedMajor, text.TruncateRunes(v.Quote, 80), t.issue.URL)
		flip := &db.Action{
			IssueNumber:    t.issue.Number,
			Action:         db.ActionKeep,
			Reason:         triage.ReasonConfirmedRecent,
			Confidence:     v.Confidence,
			Source:         "ai",
			IssueUpdatedAt: t.issue.UpdatedAt,
			Evidence:       map[string]string{"ai": v.Quote},
		}
		_, err = d.ProposeAction(flip)
		return err
	}
	return nil
}

// stillOpenBlock renders one issue for the still-open prompt: recent comments only.
func stillOpenBlock(t target) string {
	var b strings.Builder
	i := t.issue
	fmt.Fprintf(&b, "### Issue #%d: %s\n", i.Number, i.Title)
	fmt.Fprintf(&b, "opened: %s | proposed close reason: %s\n", i.CreatedAt.Format("2006-01-02"), t.action.Reason)

	comments := t.comments
	if len(comments) > 8 {
		comments = comments[len(comments)-8:]
	}
	fmt.Fprintf(&b, "RECENT COMMENTS (%d of %d):\n", len(comments), len(t.comments))
	for _, c := range comments {
		fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", c.CreatedAt.Format("2006-01-02"), c.Author, c.AuthorAssociation,
			text.TruncateRunes(text.OneLine(triage.CleanBody(c.Body)), commentRunesFor))
	}
	return b.String()
}

// ---- batching ----

// runBatches sends targets to the AI CLI in batches, handing each raw response to
// apply. AI failures skip the batch (non-fatal) but too many in a row abort.
func (f *FlagData) runBatches(a ai.AI, targets []target, promptText string, block func(target) string, apply func(string, []target) error) error {
	consecFails := 0
	for start := 0; start < len(targets); start += aiBatchSize {
		batch := targets[start:min(start+aiBatchSize, len(targets))]

		var prompt strings.Builder
		prompt.WriteString(promptText)
		for _, t := range batch {
			prompt.WriteString("\n")
			prompt.WriteString(block(t))
		}

		cout.Printf("  batch <yellow>%d</>-<yellow>%d</> of <yellow>%d</>...", start+1, start+len(batch), len(targets))
		raw, err := a.Prompt(prompt.String())
		if err == nil {
			err = apply(raw, batch)
		}
		if err != nil {
			cout.Errorf(" <red>failed:</> %v\n", err)
			consecFails++
			if consecFails >= maxConsecFails {
				return fmt.Errorf("%d consecutive AI failures, aborting", consecFails)
			}
			continue
		}
		consecFails = 0
		cout.Printf(" <green>ok</>\n")
	}
	return nil
}

// aiIdent identifies the AI that produced a verdict: the CLI binary, plus the
// model when one is pinned — so mixed-CLI runs stay traceable.
func aiIdent(cmd, model string) string {
	if model == "" {
		return cmd
	}
	return cmd + "/" + model
}
