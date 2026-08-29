package cli

import (
	"bufio"
	"strings"
	"testing"

	"github.com/katbyte/koi/lib/cout"
)

// TestAskClose pins the one interactive path every close lens runs through:
// accept must be distinguishable from every msApply* code (a lens comparing it
// to the wrong one silently skips the close), and preview must loop back for a
// real answer rather than deciding anything itself. Not parallel — it swaps the
// package's stdin reader.
//
//nolint:paralleltest // swaps the package's stdin reader, so the cases run in order
func TestAskClose(t *testing.T) {
	cout.Level = cout.VerbositySilent
	t.Cleanup(func() { cout.Level = cout.VerbosityNormal })

	for _, tc := range []struct {
		name, keys string
		want       int
	}{
		{"accept", "a\n", askAccept},
		{"enter is skip", "\n", msApplySkipped},
		{"preview then skip", "p\ns\n", msApplySkipped},
		{"preview then accept", "p\na\n", askAccept},
		{"quit", "q\n", msApplyQuit},
		{"unknown key asks again", "z\ns\n", msApplySkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdinReader = bufio.NewReader(strings.NewReader(tc.keys))
			got, err := askClose("close <cyan>#1</>?", "the comment that would be posted", "https://example.invalid/1")
			if err != nil {
				t.Fatalf("askClose: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}
