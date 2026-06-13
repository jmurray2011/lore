package cli

import (
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

func TestShortLabel(t *testing.T) {
	cases := map[string]string{
		"file:///mnt/c/Users/x/SSP-v2.docx": "SSP-v2.docx",
		"file:///a/b/c.md":                  "c.md",
		"notes.txt":                         "notes.txt",
		"":                                  "",
	}
	for in, want := range cases {
		if got := shortLabel(in); got != want {
			t.Errorf("shortLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnswerMarkdown(t *testing.T) {
	cite := func(id, src string, seq int) domain.Citation {
		return domain.Citation{ChunkID: domain.ChunkID(id), Source: src, Seq: seq}
	}

	t.Run("renumbers inline references by first appearance and dedupes", func(t *testing.T) {
		ans := app.Answer{
			Text: "Sky [ida] and grass [idb]; sky again [ida].",
			Citations: []domain.Citation{
				cite("ida", "file:///x/a.docx", 3),
				cite("idb", "file:///y/b.md", 5),
			},
		}
		got := answerMarkdown(ans)
		if !strings.Contains(got, "Sky [1] and grass [2]; sky again [1].") {
			t.Errorf("inline renumber wrong:\n%s", got)
		}
		if !strings.Contains(got, "## Sources") {
			t.Errorf("missing Sources heading:\n%s", got)
		}
		if !strings.Contains(got, "1. a.docx · chunk 3") || !strings.Contains(got, "2. b.md · chunk 5") {
			t.Errorf("sources list wrong:\n%s", got)
		}
	})

	t.Run("comma-separated references expand to separate markers", func(t *testing.T) {
		ans := app.Answer{
			Text:      "Both [ida, idb].",
			Citations: []domain.Citation{cite("ida", "a.docx", 0), cite("idb", "b.docx", 1)},
		}
		if got := answerMarkdown(ans); !strings.Contains(got, "Both [1][2].") {
			t.Errorf("comma expand wrong:\n%s", got)
		}
	})

	t.Run("structured answers with no inline refs still list sources", func(t *testing.T) {
		ans := app.Answer{
			Text:      "Plain prose, no brackets.",
			Citations: []domain.Citation{cite("ida", "a.docx", 2)},
		}
		got := answerMarkdown(ans)
		if !strings.HasPrefix(got, "Plain prose, no brackets.") {
			t.Errorf("text changed unexpectedly:\n%s", got)
		}
		if !strings.Contains(got, "1. a.docx · chunk 2") {
			t.Errorf("sources missing:\n%s", got)
		}
	})

	t.Run("no citations yields just the text", func(t *testing.T) {
		if got := answerMarkdown(app.Answer{Text: "Nothing to cite."}); got != "Nothing to cite." {
			t.Errorf("got %q", got)
		}
	})

	t.Run("an unknown bracketed token is left untouched", func(t *testing.T) {
		ans := app.Answer{Text: "See [not-a-citation] here.", Citations: nil}
		if got := answerMarkdown(ans); got != "See [not-a-citation] here." {
			t.Errorf("got %q", got)
		}
	})
}

func TestMDTable(t *testing.T) {
	got := mdTable([]string{"Name", "Dims"}, [][]string{{"docs", "3"}, {"pi|pe", "5"}})
	want := "| Name | Dims |\n| --- | --- |\n| docs | 3 |\n| pi\\|pe | 5 |\n"
	if got != want {
		t.Errorf("mdTable =\n%q\nwant\n%q", got, want)
	}
}
