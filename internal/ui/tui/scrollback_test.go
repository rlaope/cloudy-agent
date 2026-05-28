package tui

import (
	"reflect"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// printedText executes cmd (and any nested tea.Batch cmds) and returns the
// concatenated bodies of every tea.Println/Printf (printLineMessage) it
// produced. The native-scrollback redesign routes finished transcript
// content through tea.Println into the terminal's real scrollback instead
// of the in-memory viewport, so tests that used to inspect
// m.stream.content now inspect what was printed. printLineMessage is
// unexported in bubbletea, so its messageBody is read via reflection.
//
// Only Batch and printLineMessage are walked; any other message (ticks,
// pump reads, clear-screen) is ignored. Do NOT pass a cmd batch that
// contains a tick or the agent pump — executing those here would block or
// delay the test. Use it on the chrome/commit paths that emit prints.
func printedText(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	return collectPrinted(cmd())
}

// firstPrintln executes only the FIRST sub-command of a batch (where the
// chrome print always sits) and returns its printLineMessage body. Use it
// when the batch also carries a command that must NOT run in a unit test —
// the async setup-discovery scan or the GitHub self-update fetch — which
// the production code batches after the chrome print.
func firstPrintln(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		if len(batch) > 0 && batch[0] != nil {
			return collectPrinted(batch[0]())
		}
		return ""
	}
	return collectPrinted(msg)
}

func collectPrinted(msg tea.Msg) string {
	if msg == nil {
		return ""
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var b strings.Builder
		for _, c := range batch {
			if c == nil {
				continue
			}
			b.WriteString(collectPrinted(c()))
		}
		return b.String()
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Struct && v.Type().Name() == "printLineMessage" {
		if f := v.FieldByName("messageBody"); f.IsValid() && f.Kind() == reflect.String {
			return f.String() + "\n"
		}
	}
	return ""
}
