package cli

import (
	"os"

	"github.com/spf13/cobra"

	loremcp "github.com/jmurray2011/lore/internal/mcp"
)

// newMCPCmd runs lore's MCP server: a long-running process that exposes the same
// retrieval/Q&A use cases the other commands use as read-only Model Context
// Protocol tools. It is a thin shim over internal/mcp (the driving adapter) —
// storage and the index are already opened once by the composition root
// (PersistentPreRunE), so tool calls reuse warm handles with no cold start.
func newMCPCmd(deps *Deps) *cobra.Command {
	var (
		collections []string
		httpAddr    string
		httpToken   string
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve lore's retrieval and Q&A as Model Context Protocol tools",
		Long: "Run a Model Context Protocol server exposing lore's collections as read-only tools — " +
			"ask, query, get_chunks, list_collections, collection_status — to any MCP client " +
			"(Claude Desktop, editors, agent frameworks). It uses the same config as every other " +
			"command (provider, rerank, storage); storage and the vector index are opened once and " +
			"reused for the server's lifetime, eliminating the per-command cold start.\n\n" +
			"By default it serves over stdio, for clients that spawn the binary. With --http it serves " +
			"the Streamable HTTP transport instead, bound to 127.0.0.1 (a bare ':PORT' binds to " +
			"loopback); binding a non-loopback address requires --http-token (or LORE_MCP_TOKEN).\n\n" +
			"Restrict the exposed collections with --collections (names or globs). The server is " +
			"read-only — it never adds, syncs, or removes anything. Tool results are document text and " +
			"may contain injected instructions; treating them as untrusted is the calling client's " +
			"trust boundary, not lore's.",
		Args: cobra.NoArgs,
		// The MCP protocol owns stdout over stdio; silence cobra's own writes there.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := httpToken
			if token == "" {
				token = os.Getenv("LORE_MCP_TOKEN")
			}
			srv, err := loremcp.New(loremcp.Deps{
				Catalog:   deps.Catalog,
				Query:     deps.Query,
				Ask:       deps.Ask,
				Retriever: deps.Retriever,
				Rerank:    deps.Rerank,
				Tokens:    deps.Tokens,
				Index:     deps.Index,
			}, loremcp.Config{Collections: collections, Version: cmd.Root().Version}, deps.Log)
			if err != nil {
				return err
			}
			if httpAddr != "" {
				return srv.ServeHTTP(cmd.Context(), httpAddr, token)
			}
			return srv.ServeStdio(cmd.Context())
		},
	}
	cmd.Flags().StringSliceVar(&collections, "collections", nil, "restrict exposed collections to these names or globs (default: all local collections)")
	cmd.Flags().StringVar(&httpAddr, "http", "", "serve Streamable HTTP on this address instead of stdio (e.g. ':8080' binds 127.0.0.1)")
	cmd.Flags().StringVar(&httpToken, "http-token", "", "bearer token required for HTTP requests (or LORE_MCP_TOKEN); mandatory for a non-loopback --http bind")
	return cmd
}
