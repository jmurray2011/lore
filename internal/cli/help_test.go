package cli_test

import (
	"strings"
	"testing"
)

func TestRootHelpTeachesWorkflow(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	out, code := exec(deps, "--help")
	if code != 0 {
		t.Fatalf("help exit %d", code)
	}
	// A layman running `lore` with no idea where to start should see the three-step
	// workflow and the key prerequisite, not just an alphabetical command dump.
	for _, want := range []string{"lore init", "lore add", "lore ask", "LORE_API_KEY"} {
		if !strings.Contains(out, want) {
			t.Errorf("root help should teach the workflow / key; missing %q\n%s", want, out)
		}
	}
}

func TestInitHelpExplainsPinning(t *testing.T) {
	deps, _, _, _ := newDeps(stubEmbedder{space: testSpace()}, stubGenerator{})
	out, code := exec(deps, "init", "--help")
	if code != 0 {
		t.Fatalf("help exit %d", code)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "embedding") || !strings.Contains(lower, "configure") {
		t.Errorf("init help should explain pinning + configure-first, got:\n%s", out)
	}
}
