// Package mcp is lore's MCP (Model Context Protocol) driving adapter: it exposes
// lore's grounded retrieval and Q&A use cases as MCP tools so any MCP client
// (Claude Desktop, editors, agent frameworks) can call a private corpus and get
// back cited answers. Like internal/cli it is a driving adapter — it wires the
// same internal/app use cases to a protocol surface (the official Go MCP SDK as
// transport) and holds no business logic. It is read-only: no add/sync/rm/init
// tools are registered.
//
// Because it is lore's first long-running process, the storage and provider
// handles in Deps are opened once by the composition root and reused for the
// server's lifetime; tool calls hit that warm state and never re-open the DB.
package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmurray2011/lore/internal/app"
	"github.com/jmurray2011/lore/internal/domain"
)

// Deps are the lore use cases the MCP tools invoke, wired by the composition root
// (via the cli mcp command). They are warm, already-opened handles reused across
// every tool call.
type Deps struct {
	Catalog *app.Catalog
	Query   *app.Querier
	Ask     *app.Asker
	// Rerank is nil when no rerank provider is configured; the ask/query tools
	// return a tool error if rerank is requested in that case (mirroring the CLI).
	Rerank *app.Reranker
	// Tokens backs the ask tool's budget trimming.
	Tokens app.TokenCounter
	// Index backs collection_status's chunk count (read-only; Entries).
	Index app.VectorIndex
}

// Config tunes the server: the collection scope and the version reported to
// clients.
type Config struct {
	// Collections restricts which collections the tools expose, as exact names or
	// globs (path.Match against the collection name). Empty exposes all local
	// collections.
	Collections []string
	// Version is reported to clients in the MCP initialize handshake.
	Version string
}

// Server is lore's configured MCP server: the SDK server with lore's tools
// registered, plus the scope it enforces. Build it with New, then serve over
// stdio (ServeStdio) or Streamable HTTP (ServeHTTP).
type Server struct {
	deps  Deps
	scope scope
	log   *slog.Logger
	mcp   *mcpsdk.Server
}

// New builds the server, validating the collection scope and registering every
// read-only tool. A nil logger discards logs (never writes to stdout).
func New(deps Deps, cfg Config, logger *slog.Logger) (*Server, error) {
	sc, err := newScope(cfg.Collections)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	version := cfg.Version
	if version == "" {
		version = "dev"
	}
	s := &Server{deps: deps, scope: sc, log: logger}

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "lore", Title: "lore", Version: version}, nil)
	// Every tool is read-only and idempotent: it never mutates a corpus.
	readOnly := &mcpsdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "ask", Description: askDescription, Annotations: readOnly}, s.ask)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "query", Description: queryDescription, Annotations: readOnly}, s.query)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "get_chunks", Description: getChunksDescription, Annotations: readOnly}, s.getChunks)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "list_collections", Description: listCollectionsDescription, Annotations: readOnly}, s.listCollections)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "collection_status", Description: collectionStatusDescription, Annotations: readOnly}, s.collectionStatus)
	s.mcp = srv
	return s, nil
}

// ServeStdio serves the MCP protocol over stdin/stdout until the client
// disconnects or ctx is cancelled. stdout carries JSON-RPC only — logs go to the
// injected logger (stderr), never stdout, so the protocol stream stays clean.
//
// A startup failure (Connect) is returned. Once serving, the client closing the
// pipe (EOF) or ctx being cancelled (SIGINT) is normal stdio termination and
// returns nil; only a genuine mid-session transport error is surfaced.
func (s *Server) ServeStdio(ctx context.Context) error {
	ss, err := s.mcp.Connect(ctx, &mcpsdk.StdioTransport{}, nil)
	if err != nil {
		return fmt.Errorf("mcp: start stdio server: %w", err)
	}
	s.log.Info("lore mcp: serving over stdio")

	done := make(chan error, 1)
	go func() { done <- ss.Wait() }()
	select {
	case <-ctx.Done():
		_ = ss.Close()
		<-done
		return nil
	case werr := <-done:
		// The client going away is the normal stdio termination signal, so it is a
		// clean (exit 0) shutdown whether or not the handshake completed; the SDK
		// reports a pre-handshake disconnect as an internal "server is closing"
		// error not exposed for errors.Is, so anything unusual is logged rather
		// than failing the process.
		if werr != nil && !isDisconnect(werr) {
			s.log.Info("lore mcp: stdio session ended", "err", werr)
		}
		return nil
	}
}

// isDisconnect reports whether err is the ordinary, expected end-of-session for a
// stdio server: no error, the client closing the pipe (io.EOF), the stream being
// closed (net.ErrClosed), or context cancellation.
func isDisconnect(err error) bool {
	return err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// ServeHTTP serves the MCP protocol over the SDK's Streamable HTTP transport,
// bound to addr. A bare ":port" (empty host) is bound to 127.0.0.1 — local by
// default. When token is non-empty every request must carry
// "Authorization: Bearer <token>". Binding to a non-loopback host without a token
// is refused (a usage error), so a network-exposed server is never unauthenticated.
// The server stays up across tool failures and shuts down gracefully on ctx cancel.
func (s *Server) ServeHTTP(ctx context.Context, addr, token string) error {
	addr = normalizeAddr(addr)
	if err := requireTokenOffLoopback(addr, token); err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: s.httpHandler(token), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("lore mcp: serving over streamable HTTP", "addr", addr, "auth", token != "")

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// httpHandler builds the Streamable HTTP handler for the server, gated behind a
// bearer-token check when token is non-empty. The same getServer closure serves
// every request from the one warm server.
func (s *Server) httpHandler(token string) http.Handler {
	var handler http.Handler = mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s.mcp }, nil)
	// Defense-in-depth CSRF / DNS-rebinding guard: reject state-changing
	// requests a browser marks as cross-origin. Non-browser MCP clients send no
	// Sec-Fetch-Site/Origin and pass through; the SDK's own localhost
	// (Host-header) protection stays in force independently.
	handler = http.NewCrossOriginProtection().Handler(handler)
	if token == "" {
		return handler
	}
	return bearerAuth(token, handler)
}

// normalizeAddr binds a bare ":port" (empty host) to loopback, making local the
// default; an unparsable addr is returned unchanged for ListenAndServe to reject.
func normalizeAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// requireTokenOffLoopback enforces that a non-loopback bind carries a token: a
// network-reachable MCP server must not be unauthenticated. A loopback bind may
// run tokenless. addr is assumed already normalized.
func requireTokenOffLoopback(addr, token string) error {
	if token != "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: refusing to serve MCP on non-loopback address %q without --http-token (or LORE_MCP_TOKEN)", domain.ErrInvalidArgument, addr)
}

// bearerAuth gates next behind a constant-time bearer-token check.
func bearerAuth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
