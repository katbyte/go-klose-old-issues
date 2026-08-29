package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/katbyte/koi/assets"
	"github.com/katbyte/koi/lib/ai"
	"github.com/katbyte/koi/lib/cout"
	"github.com/katbyte/koi/lib/db"
	"github.com/katbyte/koi/lib/text"
	"github.com/katbyte/koi/lib/triage"
)

const (
	passMSMatch   = "ms-match"
	promptMSMatch = "milestone-evidence-match"
	// msMatchThreshold is the minimum AI confidence for --apply-with-ai to set a
	// milestone; below it the finding is reported and skipped.
	msMatchThreshold = 0.7
	// bulletRunesForAI keeps the raw changelog bullet (link refs included — the
	// URL is the number-collision tell) to a sane prompt size.
	bulletRunesForAI = 300
	// full-text budgets: accuracy beats cost here, so the model sees generous
	// slices of the issue and each evidence PR.
	msIssueBodyRunes = 3000
	msPRBodyRunes    = 2000
	// msMatchBatchSize is pairings per AI call — blocks carry full issue and PR
	// bodies, so batches stay small in favour of judgement quality.
	msMatchBatchSize = 10
)

// msMatchVerdict is the AI's judgement of one issue↔evidence pairing: how likely
// the changelog evidence actually resolves the issue, rjg-style.
type msMatchVerdict struct {
	Number     int     `json:"number"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// msJudgeTarget is one finding queued for AI judgement.
type msJudgeTarget struct {
	finding *msFinding
	block   string
	hash    string
	verdict *msMatchVerdict // from cache, or filled as batches come back
}

// msJudgedBatch is one AI call's raw result, delivered over a channel by the
// background judge.
type msJudgedBatch struct {
	raw   string
	model string // the model that answered, when the CLI disclosed it
	err   error
}

// applyMilestonesWithAI is --apply-with-ai: every issue↔evidence pairing is
// scored by the AI CLI, and only likely matches (≥ threshold) get their
// milestone set. Judging and applying are pipelined — while batch N's results
// are reviewed and applied, batch N+1 is already off being scored in the
// background, and the confirmation prompt comes right after batch 1 so answer
// time overlaps judging too. Verdicts cache in ai_verdicts so re-runs (and the
// real apply after a dry-run) only judge what changed.
func (f *FlagData) applyMilestonesWithAI(d *db.DB, todo []msFinding, milestones map[string]db.Milestone, o MilestoneOpts) error {
	promptText, err := assets.Prompt(promptMSMatch)
	if err != nil {
		return err
	}

	// resolve the canonical model first: a verdict is a function of the model
	// that produced it, so the cache must not serve one model's opinions to
	// another — an aliased --ai-model (fable vs claude-fable-5) canonicalises
	// here before the cache comparison
	if err := f.RequireAI(); err != nil {
		return err
	}
	a := f.NewAI()
	switch resolved, rerr := a.ResolveModel(); {
	case rerr != nil:
		cout.Errorf("<yellow>WARNING:</> resolving the AI model: %v — continuing as %q\n", rerr, aiIdent(f.AI.Cmd, f.AI.Model))
	case resolved != "":
		if f.AI.Model != "" && f.AI.Model != resolved {
			cout.Printf("<gray>model %s resolves to %s</>\n", f.AI.Model, resolved)
		}
		f.AI.Model = resolved
		a = f.NewAI()
	}
	ident := aiIdent(f.AI.Cmd, f.AI.Model)

	// the model judges on full text: fetch title + body for every candidate
	// issue and its evidence PRs not yet cached
	if err := f.fetchMatchTexts(d, todo); err != nil {
		return err
	}
	texts, err := d.Texts()
	if err != nil {
		return err
	}

	// build every target up front, pulling cached verdicts — a hit needs the
	// same evidence AND the same model
	var cachedTargets, uncached []*msJudgeTarget
	for i := range todo {
		fdg := &todo[i]
		block, berr := f.msMatchBlock(d, fdg, texts)
		if berr != nil {
			return berr
		}
		t := &msJudgeTarget{finding: fdg, block: block, hash: msMatchHash(promptText, block)}

		if v, err := d.GetVerdict(fdg.issue.Number, passMSMatch); err != nil {
			return err
		} else if v != nil && v.PromptHash == t.hash && v.Model == ident {
			var mv msMatchVerdict
			if json.Unmarshal([]byte(v.Verdict), &mv) == nil {
				t.verdict = &mv
				cachedTargets = append(cachedTargets, t)
				continue
			}
		}
		uncached = append(uncached, t)
	}

	auto := o.ApplyWithAIAuto
	threshold := o.Threshold
	if threshold <= 0 {
		threshold = msMatchThreshold
	}
	// interactive: the AI's score advises, the human confirms every set
	interactive := !auto && !f.DryRun

	model := f.AI.Model
	if model == "" {
		model = "CLI default"
	}
	mode := "<gray>you confirm each set</>"
	switch {
	case f.DryRun:
		mode = fmt.Sprintf("<gray>previewing the ≥</> <green>%.2f</> <gray>gate</>", threshold)
	case auto:
		mode = fmt.Sprintf("<gray>auto-applying ≥</> <green>%.2f</>", threshold)
	}
	cout.Printf("AI match check on <yellow>%d</> candidates in %s: <yellow>%d</> pairings to judge (<gray>%d cached</>) via <cyan>%s</> <gray>· model:</> <lightCyan>%s</> <gray>·</> %s\n",
		len(todo), f.repoTag(), len(uncached), len(cachedTargets), f.AI.Cmd, model, mode)

	repo, err := f.NewRepo()
	if err != nil {
		return err
	}
	throttle := newThrottle()

	// launch fires one batch's AI call in the background; parsing and verdict
	// persistence stay on the main goroutine at harvest time
	launch := func(start int) <-chan msJudgedBatch {
		batch := uncached[start:min(start+msMatchBatchSize, len(uncached))]
		var prompt strings.Builder
		prompt.WriteString(promptText)
		for _, t := range batch {
			prompt.WriteString("\n")
			prompt.WriteString(t.block)
		}
		ch := make(chan msJudgedBatch, 1)
		go func() {
			raw, respModel, err := a.PromptWithModel(prompt.String())
			ch <- msJudgedBatch{raw: raw, model: respModel, err: err}
		}()
		return ch
	}

	pos, applied, failed, previewed, below, unanswered, humanSkipped := 0, 0, 0, 0, 0, 0, 0
	stopped := false
	// process gates and applies one slice of judged targets; interactive mode
	// shows every scored candidate and asks, auto/dry-run gate on the threshold
	process := func(targets []*msJudgeTarget) error {
		for _, t := range targets {
			if stopped {
				return nil
			}
			pos++
			fdg, v := t.finding, t.verdict
			switch {
			case v == nil:
				unanswered++
				cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> <yellow>no verdict</> %s\n",
					pos, len(todo), fdg.issue.Number, text.TruncateRunes(fdg.issue.Title, 70))
			case !interactive && v.Confidence < threshold:
				below++
				cout.Printf("\n  <gray>%d/%d</> <gray>skip</> <cyan>#%d</> <%s>%.2f</> %s <darkGray>%s</>\n",
					pos, len(todo), fdg.issue.Number, scoreTag(v.Confidence), v.Confidence,
					text.TruncateRunes(fdg.issue.Title, 80), f.issueURL(fdg.issue.Number))
				cout.Printf("        <lightWhite>%s</>\n", text.OneLine(v.Reason))
			default:
				res, err := f.applyOneMilestone(d, repo, fdg, milestones, pos, len(todo), throttle, v, interactive)
				if err != nil {
					return err
				}
				switch res {
				case msApplySet:
					applied++
				case msApplyFailed:
					failed++
				case msApplyPreviewed:
					previewed++
				case msApplySkipped:
					humanSkipped++
				case msApplyQuit:
					cout.Printf("<gray>quitting — %d candidates left unreviewed</>\n", len(todo)-pos)
					stopped = true
				}
				if !f.DryRun && o.Max > 0 && applied >= o.Max {
					cout.Printf("<gray>--max reached: %d applied, skipping the rest (dry-run shows all)</>\n", o.Max)
					stopped = true
				}
			}
		}
		return nil
	}

	// harvest one background batch: report, parse, persist verdicts. The batch
	// line prints BEFORE blocking on the result so a still-running call shows
	// what's being waited on, and gets its " ok" when the answer lands.
	consecFails := 0
	harvest := func(start int, ch <-chan msJudgedBatch) error {
		end := min(start+msMatchBatchSize, len(uncached))
		cout.Printf("batch <yellow>%d</>-<yellow>%d</> of <yellow>%d</>...", start+1, end, len(uncached))
		res := <-ch

		var batchVerdicts []msMatchVerdict
		err := res.err
		if err == nil {
			err = ai.ExtractJSON(res.raw, &batchVerdicts)
		}
		if err != nil {
			cout.Errorf(" <red>failed:</> %v\n", err)
			consecFails++
			if consecFails >= maxConsecFails {
				return fmt.Errorf("%d consecutive AI failures, aborting", consecFails)
			}
			return nil // targets stay verdict-less and gate as unanswered
		}
		consecFails = 0
		cout.Printf(" <green>ok</>\n")
		// the model resolved up front; flag any mid-run drift (e.g. a CLI
		// fallback) since those verdicts would be another model's opinions
		if res.model != "" && res.model != f.AI.Model {
			cout.Printf("  <yellow>note: this batch was answered by %s, not %s</>\n", res.model, f.AI.Model)
		}

		byNumber := map[int]*msMatchVerdict{}
		for i := range batchVerdicts {
			byNumber[batchVerdicts[i].Number] = &batchVerdicts[i]
		}
		for _, t := range uncached[start:end] {
			v := byNumber[t.finding.issue.Number]
			if v == nil {
				continue
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("marshalling verdict for #%d: %w", t.finding.issue.Number, err)
			}
			if err := d.SaveVerdict(&db.Verdict{
				IssueNumber: t.finding.issue.Number, Pass: passMSMatch, PromptHash: t.hash,
				Model: ident, Verdict: string(raw), Confidence: v.Confidence, CreatedAt: db.Now(),
			}); err != nil {
				return err
			}
			t.verdict = v
		}
		return nil
	}

	// batch 1 runs in the foreground; the confirmation prompt follows it so the
	// user answers while batch 2 is already judging in the background
	var inflight <-chan msJudgedBatch
	if len(uncached) > 0 {
		if err := harvest(0, launch(0)); err != nil {
			return err
		}
		if len(uncached) > msMatchBatchSize {
			inflight = launch(msMatchBatchSize)
			cout.Printf("<gray>starting batch</> <yellow>%d</>-<yellow>%d</> <gray>in the background...</>\n",
				msMatchBatchSize+1, min(2*msMatchBatchSize, len(uncached)))
		}
	}

	// interactive mode asks per item; the up-front confirm is auto mode's gate
	if auto && !f.DryRun && !f.Yes {
		ok, err := confirm(fmt.Sprintf("auto-apply milestone sets the AI scores ≥ <green>%.2f</> (up to <yellow>%d</> candidates) in %s?", threshold, len(todo), f.repoTag()))
		if err != nil {
			return err
		}
		if !ok {
			cout.Printf("aborted\n")
			return nil
		}
	}

	// cached verdicts first (no AI wait), then batch 1, then the pipeline
	if err := process(cachedTargets); err != nil {
		return err
	}
	if len(uncached) > 0 {
		if err := process(uncached[:min(msMatchBatchSize, len(uncached))]); err != nil {
			return err
		}
	}
	for start := msMatchBatchSize; start < len(uncached) && !stopped; start += msMatchBatchSize {
		if err := harvest(start, inflight); err != nil {
			return err
		}
		if next := start + msMatchBatchSize; next < len(uncached) {
			inflight = launch(next)
			cout.Printf("<gray>starting batch</> <yellow>%d</>-<yellow>%d</> <gray>in the background...</>\n",
				next+1, min(next+msMatchBatchSize, len(uncached)))
		}
		if err := process(uncached[start:min(start+msMatchBatchSize, len(uncached))]); err != nil {
			return err
		}
	}

	if interactive {
		cout.Printf("\n<green>%d set</> · %d skipped by you · %d failed · <yellow>%d</> unanswered\n",
			applied, humanSkipped, failed, unanswered)
	} else {
		cout.Printf("\nAI match gate: <green>%d</> at or above %.2f · <fg=208>%d</> below · <yellow>%d</> unanswered\n",
			applied+failed+previewed, threshold, below, unanswered)
		if f.DryRun {
			cout.Printf("<yellow>dry-run:</> %d milestone sets previewed, nothing changed\n", previewed)
			return nil
		}
		cout.Printf("<green>%d set</> · %d failed\n", applied, failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d milestone sets failed", failed)
	}
	return nil
}

// judgeItem is one prepared block for the shared judge.
type judgeItem struct {
	number int
	block  string
}

// judgedTarget is one judged item handed to the caller's onBatch hook.
type judgedTarget struct {
	number  int
	verdict *msMatchVerdict // nil when the batch failed or omitted it
}

// judgeBlocks scores prepared issue↔evidence blocks with the AI CLI, pipelined
// one batch ahead exactly like the milestone apply: the batch line prints
// before blocking on the result, and while a batch's verdicts are handled the
// next batch is already off being judged in the background. Verdicts cache in
// ai_verdicts under pass, model-aware.
//
// The hooks make it serve both reports and applies: onReady runs once after
// the first batch lands (the natural place for an auto-mode confirm, so answer
// time overlaps batch two; return false to abort), and onBatch receives each
// slice of judged items as it becomes ready — cached verdicts first, then
// batch by batch — for interleaved review/apply (return true to stop). Both
// may be nil.
func (f *FlagData) judgeBlocks(d *db.DB, pass, promptText string, items []judgeItem,
	onReady func() (bool, error), onBatch func([]judgedTarget) (bool, error),
) (map[int]*msMatchVerdict, error) {
	if err := f.RequireAI(); err != nil {
		return nil, err
	}
	a := f.NewAI()
	switch resolved, rerr := a.ResolveModel(); {
	case rerr != nil:
		cout.Errorf("<yellow>WARNING:</> resolving the AI model: %v — continuing as %q\n", rerr, aiIdent(f.AI.Cmd, f.AI.Model))
	case resolved != "":
		f.AI.Model = resolved
		a = f.NewAI()
	}
	ident := aiIdent(f.AI.Cmd, f.AI.Model)

	verdicts := map[int]*msMatchVerdict{}
	type target struct {
		item    judgeItem
		hash    string
		verdict *msMatchVerdict
	}
	var cached []judgedTarget
	var uncached []*target
	for _, it := range items {
		hash := msMatchHash(promptText, it.block)
		if v, err := d.GetVerdict(it.number, pass); err != nil {
			return nil, err
		} else if v != nil && v.PromptHash == hash && v.Model == ident {
			var mv msMatchVerdict
			if json.Unmarshal([]byte(v.Verdict), &mv) == nil {
				verdicts[it.number] = &mv
				cached = append(cached, judgedTarget{number: it.number, verdict: &mv})
				continue
			}
		}
		uncached = append(uncached, &target{item: it, hash: hash})
	}

	cout.Printf("AI %s check: <yellow>%d</> pairings to judge (<gray>%d cached</>) via <cyan>%s</> <gray>· model:</> <lightCyan>%s</>\n",
		pass, len(uncached), len(cached), f.AI.Cmd, f.AI.Model)

	launch := func(start int) <-chan msJudgedBatch {
		batch := uncached[start:min(start+msMatchBatchSize, len(uncached))]
		var prompt strings.Builder
		prompt.WriteString(promptText)
		for _, t := range batch {
			prompt.WriteString("\n")
			prompt.WriteString(t.item.block)
		}
		ch := make(chan msJudgedBatch, 1)
		go func() {
			raw, respModel, err := a.PromptWithModel(prompt.String())
			ch <- msJudgedBatch{raw: raw, model: respModel, err: err}
		}()
		return ch
	}

	consecFails := 0
	harvest := func(start int, ch <-chan msJudgedBatch) error {
		end := min(start+msMatchBatchSize, len(uncached))
		cout.Printf("  batch <yellow>%d</>-<yellow>%d</> of <yellow>%d</>...", start+1, end, len(uncached))
		res := <-ch

		var batchVerdicts []msMatchVerdict
		err := res.err
		if err == nil {
			err = ai.ExtractJSON(res.raw, &batchVerdicts)
		}
		if err != nil {
			cout.Errorf(" <red>failed:</> %v\n", err)
			consecFails++
			if consecFails >= maxConsecFails {
				return fmt.Errorf("%d consecutive AI failures, aborting", consecFails)
			}
			return nil // targets stay verdict-less
		}
		consecFails = 0
		cout.Printf(" <green>ok</>\n")
		if res.model != "" && res.model != f.AI.Model {
			cout.Printf("  <yellow>note: this batch was answered by %s, not %s</>\n", res.model, f.AI.Model)
		}

		byNumber := map[int]*msMatchVerdict{}
		for i := range batchVerdicts {
			byNumber[batchVerdicts[i].Number] = &batchVerdicts[i]
		}
		for _, t := range uncached[start:end] {
			v := byNumber[t.item.number]
			if v == nil {
				cout.Errorf("  <yellow>#%d:</> no verdict in response\n", t.item.number)
				continue
			}
			raw, merr := json.Marshal(v)
			if merr != nil {
				return fmt.Errorf("marshalling verdict for #%d: %w", t.item.number, merr)
			}
			if err := d.SaveVerdict(&db.Verdict{
				IssueNumber: t.item.number, Pass: pass, PromptHash: t.hash,
				Model: ident, Verdict: string(raw), Confidence: v.Confidence, CreatedAt: db.Now(),
			}); err != nil {
				return err
			}
			t.verdict = v
			verdicts[t.item.number] = v
		}
		return nil
	}

	stopped := false
	emit := func(ts []judgedTarget) error {
		if onBatch == nil || stopped || len(ts) == 0 {
			return nil
		}
		stop, err := onBatch(ts)
		stopped = stopped || stop
		return err
	}
	ready := func() (bool, error) {
		if onReady == nil {
			return true, nil
		}
		return onReady()
	}
	slice := func(start int) []judgedTarget {
		end := min(start+msMatchBatchSize, len(uncached))
		out := make([]judgedTarget, 0, end-start)
		for _, t := range uncached[start:end] {
			out = append(out, judgedTarget{number: t.item.number, verdict: t.verdict})
		}
		return out
	}

	if len(uncached) == 0 {
		if ok, err := ready(); err != nil || !ok {
			return verdicts, err
		}
		return verdicts, emit(cached)
	}

	// batch 1 in the foreground, then the confirm (auto mode's answer time
	// overlaps batch 2), then cached + batch 1 + the pipeline
	inflight := launch(0)
	if err := harvest(0, inflight); err != nil {
		return verdicts, err
	}
	if len(uncached) > msMatchBatchSize {
		inflight = launch(msMatchBatchSize)
		cout.Printf("<gray>starting batch</> <yellow>%d</>-<yellow>%d</> <gray>in the background...</>\n",
			msMatchBatchSize+1, min(2*msMatchBatchSize, len(uncached)))
	}
	if ok, err := ready(); err != nil || !ok {
		return verdicts, err
	}
	if err := emit(cached); err != nil {
		return verdicts, err
	}
	if err := emit(slice(0)); err != nil {
		return verdicts, err
	}
	for start := msMatchBatchSize; start < len(uncached) && !stopped; start += msMatchBatchSize {
		if err := harvest(start, inflight); err != nil {
			return verdicts, err
		}
		if next := start + msMatchBatchSize; next < len(uncached) && !stopped {
			inflight = launch(next)
			cout.Printf("<gray>starting batch</> <yellow>%d</>-<yellow>%d</> <gray>in the background...</>\n",
				next+1, min(next+msMatchBatchSize, len(uncached)))
		}
		if err := emit(slice(start)); err != nil {
			return verdicts, err
		}
	}
	return verdicts, nil
}

// msMatchBlock renders one finding for the ms-match prompt: the issue (title +
// full body) and every piece of changelog evidence behind its determined
// milestone — each evidence PR's title and body alongside its raw changelog
// bullet, so the AI compares actual content, not just names. Bullets keep
// their link refs: the URL is the number-collision tell.
func (f *FlagData) msMatchBlock(d *db.DB, fdg *msFinding, texts map[int]db.Text) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "### Issue #%d: %s\n", fdg.issue.Number, text.OneLine(fdg.issue.Title))
	fmt.Fprintf(&b, "determined milestone: %s\n", fdg.expected)
	if t, ok := texts[fdg.issue.Number]; ok && t.Body != "" {
		fmt.Fprintf(&b, "ISSUE BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(t.Body), msIssueBodyRunes))
	}

	b.WriteString("EVIDENCE:\n")
	version := normalizeMilestone(fdg.expected)
	for i := range fdg.via {
		fx := &fdg.via[i]
		bullet, err := d.ChangelogTextFor(version, fx.PRNumber)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "- %s — %s changelog: %s\n", linkPhrase(fx), fdg.expected,
			text.OrDefault(text.TruncateRunes(text.OneLine(bullet), bulletRunesForAI), "(bullet not found)"))
		if t, ok := texts[fx.PRNumber]; ok && t.Title != "" {
			fmt.Fprintf(&b, "  PR #%d (%s): %s\n", fx.PRNumber, strings.ToLower(t.State), text.OneLine(t.Title))
			if t.Body != "" {
				fmt.Fprintf(&b, "  PR BODY:\n%s\n", text.TruncateRunes(triage.CleanBody(t.Body), msPRBodyRunes))
			}
		}
	}
	if fdg.cited {
		bullet, err := d.ChangelogTextFor(version, fdg.issue.Number)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "- the %s changelog cites #%d directly: %s\n", fdg.expected, fdg.issue.Number,
			text.OrDefault(text.TruncateRunes(text.OneLine(bullet), bulletRunesForAI), "(bullet not found)"))
	}
	return b.String(), nil
}

// fetchMatchTexts fills the texts cache for every candidate issue and evidence
// PR behind the findings.
func (f *FlagData) fetchMatchTexts(d *db.DB, todo []msFinding) error {
	numbers := map[int]bool{}
	for i := range todo {
		fdg := &todo[i]
		numbers[fdg.issue.Number] = true
		for _, fx := range fdg.via {
			numbers[fx.PRNumber] = true
		}
	}
	return f.fetchTexts(d, text.SortedKeys(numbers))
}

// fetchTexts fills the texts cache for every wanted number not yet cached, 25
// per aliased query. Numbers that no longer resolve are cached empty so they
// aren't refetched forever.
func (f *FlagData) fetchTexts(d *db.DB, want []int) error {
	cached, err := d.Texts()
	if err != nil {
		return err
	}
	var need []int
	for _, n := range want {
		if t, ok := cached[n]; !ok || !t.HasTail {
			need = append(need, n) // pre-tail rows refetch once to pick up the comments
		}
	}
	if len(need) == 0 {
		return nil
	}
	if f.NoAutoFetch {
		cout.Printf("<yellow>%d issue/PR texts not in the local cache and --no-auto-fetch is set — judging with what's cached</>\n", len(need))
		return nil
	}

	owner, name, err := f.RepoOwnerName()
	if err != nil {
		return err
	}
	client := f.NewGHQL()
	cout.Printf("fetching full text for <yellow>%d</> issues/PRs from <white>%s</>/<cyan>%s</>...\n", len(need), owner, name)

	for start := 0; start < len(need); start += 25 {
		batch := need[start:min(start+25, len(need))]
		nodes, rl, err := client.Texts(owner, name, batch)
		if err != nil {
			return err
		}
		texts := make([]db.Text, 0, len(batch))
		for _, n := range batch {
			node := nodes[n]
			if node == nil {
				texts = append(texts, db.Text{Number: n})
				continue
			}
			// full timestamps so splitTailAt can place same-day comments on the
			// right side of a close; a leading note marks truncated discussions
			var tail strings.Builder
			if hidden := node.Comments.TotalCount - len(node.Comments.Nodes); hidden > 0 {
				fmt.Fprintf(&tail, "(%d earlier comments not shown)\n", hidden)
			}
			for _, c := range node.Comments.Nodes {
				fmt.Fprintf(&tail, "[%s] %s: %s\n", c.CreatedAt, c.Author.Login, text.TruncateRunes(text.OneLine(c.Body), commentRunesFor))
			}
			texts = append(texts, db.Text{
				Number: n, IsPR: node.Typename == typePullRequest,
				State: node.State, Title: node.Title, Body: node.Body, Tail: tail.String(),
			})
		}
		if err := d.SaveTexts(texts); err != nil {
			return err
		}
		cout.Printf("  <gray>%d/%d fetched · rate limit: %d remaining</>\n", start+len(batch), len(need), rl.Remaining)
		rl.WaitIfLow()
	}
	return nil
}

func msMatchHash(promptText, block string) string {
	h := sha256.Sum256([]byte(promptText + "|" + block))
	return hex.EncodeToString(h[:8])
}

// scoreTag colours a match confidence: green at or above the apply threshold,
// orange in the murky middle, red for a clear non-match.
func scoreTag(confidence float64) string {
	switch {
	case confidence >= msMatchThreshold:
		return tagGreen
	case confidence >= 0.4:
		return tagOrange
	default:
		return tagRed
	}
}
