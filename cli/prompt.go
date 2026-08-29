package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/katbyte/koi/lib/cout"
)

// stdinReader is shared so successive prompts don't lose buffered input.
var stdinReader = bufio.NewReader(os.Stdin)

// promptInput prints prompt and returns the trimmed line the user entered.
func promptInput(prompt string) (string, error) {
	cout.Printf("%s", prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptKey prints prompt and returns a single pressed key — no enter needed.
// Enter returns "" (the default choice), ctrl-c aborts the run. When stdin
// isn't a terminal it falls back to line input so pipes keep working.
func promptKey(prompt string) (string, error) {
	cout.Printf("%s", prompt)

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := stdinReader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("reading input: %w", err)
		}
		return strings.ToLower(strings.TrimSpace(line)), nil
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("entering raw mode: %w", err)
	}
	b, rerr := stdinReader.ReadByte()
	_ = term.Restore(fd, old)
	if rerr != nil {
		return "", fmt.Errorf("reading key: %w", rerr)
	}

	switch b {
	case 3: // ctrl-c
		cout.Printf("^C\n")
		return "", errors.New("interrupted")
	case '\r', '\n':
		cout.Printf("\n")
		return "", nil
	}
	cout.Printf("%c\n", b)
	return strings.ToLower(string(b)), nil
}

// confirm prints prompt and returns true when the user presses y.
func confirm(prompt string) (bool, error) {
	answer, err := promptKey(prompt + " <gray>[y/N]</> ")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(answer, "y"), nil
}

// askAccept is askClose's go-ahead — distinct from every msApply* code.
const askAccept = -1

// askClose is the per-candidate confirmation every close lens shares:
// (a)ccept the close, (s)kip it, (p)review the comment that would be posted,
// (o)pen the issue, (q)uit the run. Preview and open loop back to the prompt so
// the human answers after seeing what they asked for; with no comment to show
// (the milestone setter) the preview key drops out of the offer.
func askClose(question, comment, url string) (int, error) {
	// magenta, not lightMagenta — that one belongs to milestones and versions
	keys := "<green>(a)</>ccept <red>(s)</>kip (o)pen (q)uit"
	if comment != "" {
		keys = "<green>(a)</>ccept <red>(s)</>kip <magenta>(p)</>review (o)pen (q)uit"
	}
	for {
		ans, err := promptKey(fmt.Sprintf("      %s %s <gray>></> ", question, keys))
		if err != nil {
			return msApplyFailed, err
		}
		switch strings.ToLower(ans) {
		case "a", "y":
			return askAccept, nil
		case "s", "n", "":
			return msApplySkipped, nil
		case "p":
			printCommentPreview(comment)
		case "o":
			openIssueInBrowser(url)
		case "q":
			return msApplyQuit, nil
		}
	}
}

// printCommentPreview shows the comment exactly as it would land on the issue.
// The body goes to the writer unparsed — close comments are markdown, and an
// angle-bracketed url in one is not a colour tag.
func printCommentPreview(comment string) {
	if comment == "" {
		return
	}
	cout.Printf("\n      <magenta>the comment that would be posted:</>\n")
	for line := range strings.SplitSeq(comment, "\n") {
		_, _ = fmt.Fprintf(cout.Writer(), "        %s\n", line)
	}
	cout.Printf("\n")
}
