package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// replayView is the JSON shape of a replay outcome.
type replayView struct {
	Reproduced     bool        `json:"reproduced"`
	Attempted      bool        `json:"attempted"`
	Drift          []driftView `json:"drift,omitempty"`
	RetrievalMatch bool        `json:"retrieval_match"`
	MissingCited   []string    `json:"missing_cited,omitempty"`
	AnswerMatch    bool        `json:"answer_match"`
	AnswerDigest   string      `json:"answer_digest,omitempty"`
	ExpectedDigest string      `json:"expected_answer_digest,omitempty"`
}

type driftView struct {
	Collection string `json:"collection"`
	Was        string `json:"was"`
	Now        string `json:"now"`
}

func newReplayCmd(deps *Deps) *cobra.Command {
	var allowDrift bool
	cmd := &cobra.Command{
		Use:   "replay [manifest]",
		Short: "Re-run an ask manifest and verify the answer reproduces",
		Long: "Re-run the provenance manifest emitted by `ask --reproducible --json`: verify the corpus has " +
			"not drifted (each collection's digest still matches), that the cited evidence still retrieves, and " +
			"that the answer reproduces. Reads the manifest from the file argument or stdin, accepting either a " +
			"bare manifest or a full `ask --json` envelope.\n\n" +
			"Fails closed on corpus drift (exit 5); pass --allow-drift to re-retrieve and re-synthesize anyway.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if deps.Replay == nil {
				return fmt.Errorf("%w: replay is not available", domain.ErrInvalidArgument)
			}
			raw, err := readManifestInput(cmd, args)
			if err != nil {
				return err
			}
			m, err := decodeManifest(raw)
			if err != nil {
				return err
			}
			report, err := deps.Replay.Replay(cmd.Context(), m, allowDrift)
			if err != nil {
				return err
			}
			reproduced := report.Reproduced(allowDrift)
			if err := render(cmd, replayViewOf(report, reproduced), replayMarkdown(report, reproduced, allowDrift)); err != nil {
				return err
			}
			if !reproduced {
				return fmt.Errorf("replay: %w: the exhibit did not reproduce", app.ErrGateUnmet)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowDrift, "allow-drift", false, "re-retrieve and re-synthesize even if the corpus digest changed (default: fail closed on drift)")
	return cmd
}

// readManifestInput reads the manifest bytes from the file argument, or stdin
// when no argument is given.
func readManifestInput(cmd *cobra.Command, args []string) ([]byte, error) {
	if len(args) == 1 {
		raw, err := os.ReadFile(args[0])
		if err != nil {
			return nil, fmt.Errorf("read manifest %q: %w", args[0], err)
		}
		return raw, nil
	}
	return io.ReadAll(cmd.InOrStdin())
}

// decodeManifest parses either a bare manifest or a full `ask --json` envelope
// (whose manifest rides under the "manifest" key), so a user can replay the
// saved output of `ask --json` directly.
func decodeManifest(raw []byte) (app.Manifest, error) {
	var envelope struct {
		Manifest *app.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Manifest != nil {
		return *envelope.Manifest, nil
	}
	var m app.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return app.Manifest{}, fmt.Errorf("%w: invalid manifest JSON: %v", domain.ErrInvalidArgument, err)
	}
	if len(m.Corpus) == 0 {
		return app.Manifest{}, fmt.Errorf("%w: manifest has no corpus references", domain.ErrInvalidArgument)
	}
	return m, nil
}

func replayViewOf(r app.ReplayReport, reproduced bool) replayView {
	v := replayView{
		Reproduced:     reproduced,
		Attempted:      r.Attempted,
		RetrievalMatch: r.RetrievalMatch,
		MissingCited:   r.MissingCited,
		AnswerMatch:    r.AnswerMatch,
		AnswerDigest:   string(r.AnswerDigest),
		ExpectedDigest: string(r.ExpectedAnswer),
	}
	for _, d := range r.Drift {
		v.Drift = append(v.Drift, driftView{Collection: d.Collection, Was: string(d.Was), Now: string(d.Now)})
	}
	return v
}

func replayMarkdown(r app.ReplayReport, reproduced, allowDrift bool) string {
	var b strings.Builder
	verdict := "NOT reproduced"
	if reproduced {
		verdict = "reproduced"
	}
	fmt.Fprintf(&b, "## Replay — %s\n\n", verdict)

	if len(r.Drift) == 0 {
		b.WriteString("- **corpus** — unchanged\n")
	} else {
		fmt.Fprintf(&b, "- **corpus** — DRIFTED (%d collection(s))\n", len(r.Drift))
		for _, d := range r.Drift {
			fmt.Fprintf(&b, "  - %s: was `%s` now `%s`\n", d.Collection, short12(d.Was), short12(d.Now))
		}
	}

	if !r.Attempted {
		b.WriteString("- **reproduction** — not attempted (corpus drifted; rerun with --allow-drift)\n")
		return b.String()
	}
	if r.RetrievalMatch {
		b.WriteString("- **retrieval** — all cited evidence still retrieved\n")
	} else {
		fmt.Fprintf(&b, "- **retrieval** — %d cited chunk(s) no longer retrieved\n", len(r.MissingCited))
	}
	if r.AnswerMatch {
		b.WriteString("- **answer** — reproduced (digest matches)\n")
	} else {
		fmt.Fprintf(&b, "- **answer** — differs (got `%s`, expected `%s`)\n", short12(r.AnswerDigest), short12(r.ExpectedAnswer))
	}
	return b.String()
}

func short12(h domain.ContentHash) string {
	s := string(h)
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
