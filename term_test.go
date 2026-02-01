package immterm_test

import (
	"strings"
	"testing"

	"github.com/Kodecable/immterm"
)

func TestVersion(t *testing.T) {
	term, err := immterm.Start(immterm.CommandConfig{
		Argv: []string{"uname", "-a"},
	}, immterm.HistoryConfig{})
	if err != nil {
		t.Error(err)
	}

	term.Wait()
	fr := term.Fetch()
	term.Close()

	t.Log(fr.Screen[0])
}

func comapreLine(outputLine string, str string) bool {
	return strings.TrimSpace(outputLine) == str
}

func splitHistory(buf []byte) []string {
	if len(buf) == 0 {
		return nil
	}
	str := string(buf)
	if before, ok := strings.CutSuffix(str, "\r\n"); ok {
		str = before
	}
	return strings.Split(str, "\r\n")
}

func TestLongOutput(t *testing.T) {
	term, err := immterm.Start(immterm.CommandConfig{
		Argv: []string{"awk", "BEGIN { for(i=1; i<=500; i++) print \"test line \" i}"},
		Size: immterm.TermSize{
			Rows: 24,
			Cols: 80,
		},
	}, immterm.HistoryConfig{})
	if err != nil {
		t.Error(err)
	}

	term.Wait()
	fr := term.Fetch()
	term.Close()

	if len(fr.Screen) != 24 {
		t.Fatalf("expect screen col 24, got %d", len(fr.Screen))
	}

	if !comapreLine(fr.Screen[len(fr.Screen)-1], "test line 500") &&
		!comapreLine(fr.Screen[len(fr.Screen)-2], "test line 500") {
		t.Fatalf("expect line 500 in screen, not found")
	}

	historyLines := splitHistory(fr.History)
	if len(historyLines) == 0 {
		t.Fatalf("expect history not empty")
	}
	if !(comapreLine(historyLines[len(historyLines)-1], "test line 476") ||
		comapreLine(historyLines[len(historyLines)-1], "test line 477")) {
		t.Fatalf("expect last history line 476/477, got %q", historyLines[len(historyLines)-1])
	}
	if total := len(historyLines) + fr.Missed; total != 500-24 && total != 500-(24-1) {
		t.Fatalf("unexpected total lines accounted (history+missed): %d", total)
	}
}

func TestLongLongOutput(t *testing.T) {
	term, err := immterm.Start(immterm.CommandConfig{
		Argv: []string{"awk", "BEGIN { for(i=1; i<=10240; i++) print \"test line \" i}"},
		Size: immterm.TermSize{
			Rows: 24,
			Cols: 80,
		},
	}, immterm.HistoryConfig{
		Size: 4096,
	})
	if err != nil {
		t.Error(err)
	}

	term.Wait()
	fr := term.Fetch()
	term.Close()

	if len(fr.Screen) != 24 {
		t.Fatalf("expect screen col 24, got %d", len(fr.Screen))
	}

	if !comapreLine(fr.Screen[len(fr.Screen)-1], "test line 10240") &&
		!comapreLine(fr.Screen[len(fr.Screen)-2], "test line 10240") {
		t.Fatalf("expect line 10240 in screen, not found")
	}

	historyLines := splitHistory(fr.History)

	if !comapreLine(historyLines[len(historyLines)-1], "test line 10216") &&
		!comapreLine(historyLines[len(historyLines)-2], "test line 10216") {
		t.Fatalf("expect line 10216 in history, not found")
	}

	total := len(historyLines) + fr.Missed
	if total != 10240-24 && total != 10240-(24-1) {
		t.Fatalf("unexpected total lines accounted (history+missed): %d", total)
	}
}
