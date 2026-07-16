package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jmurray2011/lore/internal/adapters/agecrypt"
	"github.com/jmurray2011/lore/internal/domain"
	"github.com/jmurray2011/lore/internal/secret"
)

// envExportKeyCmd is the environment fallback for --passphrase-cmd, on both
// export and import.
const envExportKeyCmd = "LORE_EXPORT_KEY_CMD"

func newExportCmd(deps *Deps) *cobra.Command {
	var (
		output     string
		encrypt    bool
		passCmd    string
		recipients []string
	)
	cmd := &cobra.Command{
		Use:   "export <collection> -o <file>",
		Short: "Export a collection to a single portable artifact file",
		Long: "Serialize a collection — its chunks, vectors, embedding-space and chunker pins, and " +
			"metadata — into one self-contained, versioned file you can commit, ship, or hand off " +
			"pre-indexed. Reconstruct it elsewhere with `lore import`.\n\n" +
			"With --encrypt the whole artifact (vectors included) is wrapped in an age envelope. The " +
			"automation-safe key source is --passphrase-cmd (or " + envExportKeyCmd + "), whose stdout is the " +
			"passphrase; on a terminal with no command source you are prompted. --recipient encrypts to " +
			"an age public key instead (mutually exclusive with a passphrase). Use '-o -' to write to stdout.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: export takes exactly one collection name", domain.ErrInvalidArgument)
			}
			if output == "" {
				return fmt.Errorf("%w: export requires -o/--output (use '-' for stdout)", domain.ErrInvalidArgument)
			}

			var buf bytes.Buffer
			sum, err := deps.Export.Export(cmd.Context(), args[0], &buf)
			if err != nil {
				return err
			}
			payload, encrypted, err := maybeEncrypt(cmd, buf.Bytes(), encrypt, passCmd, recipients)
			if err != nil {
				return err
			}
			if err := writeArtifact(cmd, output, payload); err != nil {
				return err
			}

			view := transferView{Collection: sum.Collection, Model: sum.Model, Dimensions: sum.Dimensions, Documents: sum.Documents, Chunks: sum.Chunks, Encrypted: encrypted, Output: output}
			human := fmt.Sprintf("Exported **%s** — %d documents, %d chunks → `%s`%s.", sum.Collection, sum.Documents, sum.Chunks, output, encNote(encrypted))
			// When the artifact itself goes to stdout, the summary moves to stderr so
			// the piped bytes stay clean.
			if output == "-" {
				return reportToStderr(cmd, view, human)
			}
			return render(cmd, view, human)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "destination file (use '-' for stdout)")
	cmd.Flags().BoolVar(&encrypt, "encrypt", false, "encrypt the artifact with age (passphrase or --recipient)")
	cmd.Flags().StringVar(&passCmd, "passphrase-cmd", "", "command whose stdout is the encryption passphrase (or set "+envExportKeyCmd+")")
	cmd.Flags().StringArrayVar(&recipients, "recipient", nil, "age recipient public key (age1...) to encrypt to (repeatable; mutually exclusive with a passphrase)")
	return cmd
}

func newImportCmd(deps *Deps) *cobra.Command {
	var (
		name     string
		force    bool
		passCmd  string
		identity string
	)
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import a collection from a portable artifact file",
		Long: "Reconstruct a collection from a `lore export` artifact into the local store, with its " +
			"embedding-space and chunker pins intact. --name imports under a different name; --force " +
			"overwrites an existing collection of that name.\n\n" +
			"Encryption is detected from the artifact itself (not the file name): an age-wrapped artifact " +
			"is decrypted with --passphrase-cmd (or " + envExportKeyCmd + "), --identity <age-key-file>, or an " +
			"interactive prompt. Use '-' to read the artifact from stdin.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: import takes exactly one artifact file (use '-' for stdin)", domain.ErrInvalidArgument)
			}
			data, err := readArtifact(cmd, args[0])
			if err != nil {
				return err
			}
			plaintext, encrypted, err := maybeDecrypt(cmd, data, passCmd, identity)
			if err != nil {
				return err
			}
			sum, err := deps.Import.Import(cmd.Context(), bytes.NewReader(plaintext), name, force)
			if err != nil {
				return err
			}
			// The artifact imports fine with no provider configured, but querying
			// needs an embedder that serves the collection's pinned space. Warn now
			// (on stderr) if the local embedder cannot, so the gap is not discovered
			// only at the first query.
			if note, ok := ImportQueryabilityNote(deps.EmbedSpace, sum.Model, sum.Dimensions); ok {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), note)
			}

			view := transferView{Collection: sum.Collection, Model: sum.Model, Dimensions: sum.Dimensions, Documents: sum.Documents, Chunks: sum.Chunks, Encrypted: encrypted}
			human := fmt.Sprintf("Imported **%s** — %d documents, %d chunks (%s/%d)%s.", sum.Collection, sum.Documents, sum.Chunks, sum.Model, sum.Dimensions, encNote(encrypted))
			return render(cmd, view, human)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "import under this name instead of the artifact's original")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing collection of the same name")
	cmd.Flags().StringVar(&passCmd, "passphrase-cmd", "", "command whose stdout is the decryption passphrase (or set "+envExportKeyCmd+")")
	cmd.Flags().StringVar(&identity, "identity", "", "age identity file to decrypt with (mutually exclusive with a passphrase)")
	return cmd
}

// maybeEncrypt returns the bytes to write and whether they were encrypted. With
// --encrypt off it returns the plaintext unchanged (and rejects a stray key
// source, so a user who meant to encrypt is not silently handed plaintext). With
// --encrypt on it encrypts to recipients, or via a passphrase from the command
// source, or — on a terminal — an interactive double prompt; with no key source
// and no TTY it is a usage error (exit 2).
func maybeEncrypt(cmd *cobra.Command, plaintext []byte, encrypt bool, passCmd string, recipients []string) ([]byte, bool, error) {
	effCmd := effectiveKeyCmd(passCmd)
	if !encrypt {
		if effCmd != "" || len(recipients) > 0 {
			return nil, false, fmt.Errorf("%w: --passphrase-cmd/--recipient require --encrypt", domain.ErrInvalidArgument)
		}
		return plaintext, false, nil
	}
	if len(recipients) > 0 && effCmd != "" {
		return nil, false, fmt.Errorf("%w: --recipient and a passphrase are mutually exclusive", domain.ErrInvalidArgument)
	}
	if len(recipients) > 0 {
		ct, err := agecrypt.EncryptRecipients(plaintext, recipients)
		return ct, true, err
	}
	pass, err := exportPassphrase(cmd, effCmd)
	if err != nil {
		return nil, false, err
	}
	ct, err := agecrypt.EncryptPassphrase(plaintext, pass)
	return ct, true, err
}

// maybeDecrypt detects encryption from the artifact content (not the file name)
// and decrypts when needed. A plaintext artifact is returned unchanged. An
// encrypted artifact is decrypted with an identity file, a passphrase command,
// or an interactive prompt; with none and no TTY it is a usage error (exit 2). A
// failed decryption surfaces agecrypt.ErrDecrypt (exit 1).
func maybeDecrypt(cmd *cobra.Command, data []byte, passCmd, identity string) ([]byte, bool, error) {
	if !agecrypt.IsEncrypted(data) {
		return data, false, nil
	}
	effCmd := effectiveKeyCmd(passCmd)
	if identity != "" && effCmd != "" {
		return nil, false, fmt.Errorf("%w: --identity and a passphrase are mutually exclusive", domain.ErrInvalidArgument)
	}
	if identity != "" {
		idBytes, err := os.ReadFile(identity)
		if err != nil {
			return nil, false, fmt.Errorf("read identity %q: %w", identity, err)
		}
		pt, err := agecrypt.DecryptIdentities(data, idBytes)
		return pt, true, err
	}

	var pass string
	switch {
	case effCmd != "":
		var err error
		if pass, err = secret.FromCommand(cmd.Context(), effCmd); err != nil {
			return nil, false, err
		}
	case inputIsTerminal(cmd):
		var err error
		if pass, err = readSecret(cmd, "Passphrase: "); err != nil {
			return nil, false, err
		}
	default:
		return nil, false, fmt.Errorf("%w: artifact is encrypted; provide --passphrase-cmd / %s / --identity, or run interactively", domain.ErrInvalidArgument, envExportKeyCmd)
	}
	pt, err := agecrypt.DecryptPassphrase(data, pass)
	return pt, true, err
}

// effectiveKeyCmd is the --passphrase-cmd flag, or the env fallback when unset.
func effectiveKeyCmd(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(envExportKeyCmd)
}

// exportPassphrase resolves the encryption passphrase from the command source,
// or an interactive confirm-twice prompt on a TTY, or a usage error.
func exportPassphrase(cmd *cobra.Command, effCmd string) (string, error) {
	if effCmd != "" {
		return secret.FromCommand(cmd.Context(), effCmd)
	}
	if !inputIsTerminal(cmd) {
		return "", fmt.Errorf("%w: --encrypt needs a passphrase; set --passphrase-cmd / %s, or run interactively", domain.ErrInvalidArgument, envExportKeyCmd)
	}
	p1, err := readSecret(cmd, "Passphrase: ")
	if err != nil {
		return "", err
	}
	p2, err := readSecret(cmd, "Confirm passphrase: ")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", fmt.Errorf("%w: passphrases do not match", domain.ErrInvalidArgument)
	}
	if p1 == "" {
		return "", fmt.Errorf("%w: passphrase must not be empty", domain.ErrInvalidArgument)
	}
	return p1, nil
}

// inputIsTerminal reports whether stdin is an interactive terminal (so a prompt
// is possible). In tests stdin is a non-*os.File reader, so this is false and the
// non-interactive guards fire.
func inputIsTerminal(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// readSecret reads one line of input without echo, prompting on stderr.
func readSecret(cmd *cobra.Command, prompt string) (string, error) {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return "", fmt.Errorf("%w: cannot prompt for a passphrase without a terminal", domain.ErrInvalidArgument)
	}
	_, _ = fmt.Fprint(cmd.ErrOrStderr(), prompt)
	b, err := term.ReadPassword(int(f.Fd()))
	_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	return string(b), nil
}

// readArtifact reads the artifact bytes from a file, or from stdin when file is "-".
func readArtifact(cmd *cobra.Command, file string) ([]byte, error) {
	if file == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", file, err)
	}
	return data, nil
}

// writeArtifact writes payload to a file (0600 — the artifact is the whole
// corpus), or to stdout when output is "-".
func writeArtifact(cmd *cobra.Command, output string, payload []byte) error {
	if output == "-" {
		_, err := cmd.OutOrStdout().Write(payload)
		return err
	}
	if err := os.WriteFile(output, payload, 0o600); err != nil {
		return fmt.Errorf("write artifact %q: %w", output, err)
	}
	return nil
}

// reportToStderr renders a summary to stderr (used by export -o -, where stdout
// carries the artifact bytes).
func reportToStderr(cmd *cobra.Command, view transferView, human string) error {
	w := cmd.ErrOrStderr()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(view)
	}
	_, err := fmt.Fprintln(w, human)
	return err
}

// ImportQueryabilityNote returns a stderr note, and true, when the local
// configured embedder (local) cannot query a just-imported collection because it
// serves a different embedding space than the one the collection is pinned to
// (model/dims). It returns ok=false when the spaces match, or when the local
// space is unknown (zero) — in which case there is nothing useful to say.
func ImportQueryabilityNote(local domain.EmbeddingSpace, model string, dims int) (string, bool) {
	if local.Model == "" || local.Dimensions == 0 {
		return "", false
	}
	if local.Model == model && local.Dimensions == dims {
		return "", false
	}
	return fmt.Sprintf("note: this collection is pinned to %s/%d, but your configured embedder is %s/%d. "+
		"To query it, configure an embedder that serves %s/%d (its exact model and dimensions).",
		model, dims, local.Model, local.Dimensions, model, dims), true
}

// encNote is a human suffix marking an encrypted artifact.
func encNote(encrypted bool) string {
	if encrypted {
		return " (encrypted)"
	}
	return ""
}
