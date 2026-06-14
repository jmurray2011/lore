package mcp

import (
	"fmt"

	"github.com/jmurray2011/lore/internal/domain"
)

// These are the pre-use-case errors the tool handlers raise before reaching a
// use case (the use cases themselves return their own errors — collection not
// found, space mismatch — which flow straight through). All of them are returned
// from a handler and so become MCP *tool errors* (the SDK packs them into the
// result with IsError set and the session stays alive); the wrapped sentinels are
// for callers that inspect them, not for the wire.

var (
	errNoCollections = fmt.Errorf("%w: at least one collection is required", domain.ErrInvalidArgument)
	errEmptyQuestion = fmt.Errorf("%w: question must not be empty", domain.ErrInvalidArgument)
	errEmptyQuery    = fmt.Errorf("%w: query must not be empty", domain.ErrInvalidArgument)
	errNoCollection  = fmt.Errorf("%w: a collection is required", domain.ErrInvalidArgument)
	errNoChunkIDs    = fmt.Errorf("%w: at least one chunk_id is required", domain.ErrInvalidArgument)
)
