package cli

import (
	"regexp"
	"strings"
	"testing"
)

// The fake-VPS parity suite compares help lines literally, so the layout is
// pinned here: summaries start at a fixed column and long forms wrap.
func TestHelpTextLayout(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(helpText(), "\n"), "\n")
	if got := strings.Join(lines[:3], "\n"); got != "Usage: codvps <command>\n\nCommands:" {
		t.Fatalf("help header = %q", got)
	}
	summaryCol := 2 + helpNameWidth + 1
	body := lines[3:]
	i := 0
	for _, e := range helpEntries() {
		if i >= len(body) {
			t.Fatalf("help ended before entry %q", e.form)
		}
		if len(e.form) > helpNameWidth {
			if body[i] != "  "+e.form {
				t.Errorf("long form line = %q, want %q", body[i], "  "+e.form)
			}
			i++
			if i >= len(body) || body[i] != strings.Repeat(" ", summaryCol)+e.summary {
				t.Errorf("wrapped summary for %q misaligned", e.form)
			}
		} else {
			want := "  " + e.form + strings.Repeat(" ", summaryCol-2-len(e.form)) + e.summary
			if body[i] != want {
				t.Errorf("line = %q, want %q", body[i], want)
			}
		}
		i++
	}
	if i != len(body) {
		t.Errorf("help has %d unexpected trailing lines", len(body)-i)
	}
}

func TestHelpTextExactLines(t *testing.T) {
	want := []string{
		"  login github                 Authenticate GitHub, then set up VPS SSH access and Git identity",
		"  pair codex                   Print a short-lived Codex pairing code as JSON",
		"  config link <repo-url>       Keep your agent config (CLAUDE.md, skills, …) in a git repo: clone it and link it into place",
		"  version                      Show version, commit and build date",
	}
	lines := strings.Split(helpText(), "\n")
	for _, w := range want {
		found := false
		for _, l := range lines {
			if l == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help is missing exact line %q", w)
		}
	}
}

// Literal subcommand words are never bracketed; only placeholders and
// alternations are.
func TestHelpFormsDoNotBracketLiterals(t *testing.T) {
	bracketedWord := regexp.MustCompile(`<[a-z-]+>`)
	allowed := map[string]bool{"<url>": true, "<name>": true, "<source>": true, "<args>": true, "<command>": true, "<list>": true, "<repo-url>": true}
	for _, e := range helpEntries() {
		for _, m := range bracketedWord.FindAllString(e.form, -1) {
			if !allowed[m] {
				t.Errorf("form %q brackets literal %s", e.form, m)
			}
		}
	}
}

func TestHelpOmitsInternalAndLegacyCommands(t *testing.T) {
	for _, e := range helpEntries() {
		first := strings.Fields(e.form)
		for _, hidden := range []string{"ensure", "heritage", "restart", "cod", "internal"} {
			for _, word := range first {
				if word == hidden {
					t.Errorf("help form %q exposes %q", e.form, hidden)
				}
			}
		}
	}
}
