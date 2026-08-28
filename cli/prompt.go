package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

// confirm prints prompt and returns true when the user answers y.
func confirm(prompt string) (bool, error) {
	answer, err := promptInput(prompt + " <gray>[y/N]</> ")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(answer, "y"), nil
}
