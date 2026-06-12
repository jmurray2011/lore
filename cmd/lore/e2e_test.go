package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestEndToEndSQLitePersistence builds the real binary and drives
// init→add→ls→status→query→ask→rm across separate process invocations sharing
// one SQLite file. It exercises the composition root, the openai adapter's real
// HTTP path (against a stub provider), and persistence across processes — which
// only holds once sqlite is wired in (memstore is per-process).
func TestEndToEndSQLitePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds the binary and runs it as subprocesses")
	}

	const dims = 8
	provider := stubProvider(t, dims)
	defer provider.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "lore")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build binary: %v", err)
	}

	dbPath := filepath.Join(dir, "lore.db")
	env := append(os.Environ(),
		"LORE_BASE_URL="+provider.URL+"/v1",
		"LORE_API_KEY=test",
		"LORE_EMBED_MODEL=stub-embed",
		"LORE_CHAT_MODEL=stub-chat",
		"LORE_DIMENSIONS="+strconv.Itoa(dims),
		"LORE_STORAGE_BACKEND=sqlite",
		"LORE_DB_PATH="+dbPath,
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"), // isolate from any real config.toml
	)

	run := func(t *testing.T, args ...string) (stdout, stderr string, code int) {
		t.Helper()
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			code = 0
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			t.Fatalf("exec %v: %v", args, err)
		}
		return out.String(), errb.String(), code
	}

	mustSucceed := func(t *testing.T, args ...string) string {
		t.Helper()
		out, errb, code := run(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d, stderr=%s", args, code, errb)
		}
		return out
	}

	docPath := filepath.Join(dir, "alpha.txt")
	if err := os.WriteFile(docPath, []byte("alpha beta gamma delta epsilon"), 0o600); err != nil {
		t.Fatal(err)
	}

	mustSucceed(t, "init", "docs")
	mustSucceed(t, "add", "docs", docPath)

	// Fresh process must see the collection written by a prior process.
	if out := mustSucceed(t, "--json", "ls"); !strings.Contains(out, `"name": "docs"`) {
		t.Fatalf("ls did not persist collection across processes: %s", out)
	}
	if out := mustSucceed(t, "--json", "status", "docs"); !strings.Contains(out, "stub-embed") {
		t.Fatalf("status missing pinned model: %s", out)
	}
	if out := mustSucceed(t, "--json", "query", "docs", "alpha"); !strings.Contains(out, "alpha beta gamma") {
		t.Fatalf("query did not return the ingested chunk: %s", out)
	}
	if out := mustSucceed(t, "--json", "ask", "docs", "what is alpha?"); !strings.Contains(out, "stub answer") {
		t.Fatalf("ask did not return the synthesized answer: %s", out)
	}

	mustSucceed(t, "rm", "docs")
	if out := mustSucceed(t, "--json", "ls"); strings.Contains(out, `"name": "docs"`) {
		t.Fatalf("collection survived rm: %s", out)
	}
}

// stubProvider serves an OpenAI-compatible /v1/embeddings and
// /v1/chat/completions. Every embedding is the same unit vector, which is all
// retrieval needs to surface the ingested chunk.
func stubProvider(t *testing.T, dims int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vec := make([]float32, dims)
		if dims > 0 {
			vec[0] = 1
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var resp struct {
			Data []datum `json:"data"`
		}
		for i := range req.Input {
			resp.Data = append(resp.Data, datum{Index: i, Embedding: vec})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"stub answer grounded in context"}}]}`)
	})

	return httptest.NewServer(mux)
}
