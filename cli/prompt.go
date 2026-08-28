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
