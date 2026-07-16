package cli_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/cli"
	"github.com/jmurray2011/lore/internal/domain"
)

// execE runs one command and returns the raw error (cobra silences it in
// SilenceErrors mode), so tests can assert on the guidance text, not just the
// exit code.
func execE(deps cli.Deps, args ...string) error {
	root := cli.NewRootCommand(depsBuilder(deps), "test", io.Discard, io.Discard)
	root.SetArgs(args)
	return root.Execute()
}

func TestUnquotedQuestionSuggestsQuoting(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	for _, args := range [][]string{
		{"ask", "docs", "how", "does", "auth", "work"},
		{"query", "docs", "what", "is", "this"},
	} {
		err := execE(deps, args...)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("%v: want usage error, got %v", args, err)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "quote") {
			t.Errorf("%v: message should mention quoting, got: %v", args, err)
		}
	}
}

func TestRerankUnconfiguredNamesConfigKeys(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	// query/ask --rerank without a configured provider must name the same keys the
	// standalone rerank command does, not the app layer's key-less phrasing.
	err := execE(deps, "query", "docs", "q", "--rerank")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("want usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "rerank.base_url") {
		t.Errorf("rerank-unconfigured error should name config keys, got: %v", err)
	}
}

func TestUnknownCollectionListsExisting(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	if _, err := deps.Catalog.Init(context.Background(), "realkb"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"query", "ghost", "anything"},
		{"status", "ghost"},
	} {
		err := execE(deps, args...)
		if !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("%v: want not-found, got %v", args, err)
		}
		if !strings.Contains(err.Error(), "realkb") || !strings.Contains(err.Error(), "lore ls") {
			t.Errorf("%v: should list existing collections, got: %v", args, err)
		}
	}
}

func TestUnknownCollectionWithNoneSuggestsInit(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	err := execE(deps, "status", "ghost")
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("want not-found, got %v", err)
	}
	if !strings.Contains(err.Error(), "lore init") {
		t.Errorf("with no collections, should suggest lore init, got: %v", err)
	}
}
