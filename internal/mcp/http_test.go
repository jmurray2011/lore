package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTP_RoundTrip(t *testing.T) {
	ts := httptest.NewServer(newHarness(t, nil, false).srv.httpHandler(""))
	defer ts.Close()

	ctx := context.Background()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect over streamable HTTP: %v", err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "query", Arguments: QueryInput{Collections: []string{"docs"}, Query: "auth"}})
	if err != nil {
		t.Fatalf("call tool over HTTP: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", textOf(t, res))
	}
	if out := decodeOut[QueryOutput](t, res); len(out.Hits) == 0 {
		t.Error("want hits over the HTTP transport")
	}
}

func TestHTTP_BearerAuthGate(t *testing.T) {
	ts := httptest.NewServer(newHarness(t, nil, false).srv.httpHandler("s3cret"))
	defer ts.Close()

	// No / wrong token → 401, before any MCP handling.
	for _, h := range []string{"", "Bearer wrong"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL, http.NoBody)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q: status = %d, want 401", h, resp.StatusCode)
		}
	}

	// Correct token → the MCP round trip works.
	ctx := context.Background()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "v1"}, nil)
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearerInjector{token: "s3cret", base: http.DefaultTransport}},
	}
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect with token: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_collections", Arguments: ListCollectionsInput{}})
	if err != nil {
		t.Fatalf("authenticated call: %v", err)
	}
	if res.IsError {
		t.Errorf("authenticated call should succeed: %s", textOf(t, res))
	}
}

// bearerInjector adds the Authorization header to every request, so the SDK
// client can authenticate against the bearer gate.
type bearerInjector struct {
	token string
	base  http.RoundTripper
}

func (b bearerInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

func TestHTTP_CrossOriginProtection(t *testing.T) {
	ts := httptest.NewServer(newHarness(t, nil, false).srv.httpHandler(""))
	defer ts.Close()

	// A state-changing request a browser marks as cross-site must be rejected
	// before reaching the MCP handler — defense against DNS-rebinding / CSRF
	// from a malicious page. Non-browser MCP clients send no Sec-Fetch-Site and
	// are unaffected (covered by TestHTTP_RoundTrip).
	req, _ := http.NewRequest(http.MethodPost, ts.URL, http.NoBody)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d, want 403", resp.StatusCode)
	}
}

func TestRequireTokenOffLoopback(t *testing.T) {
	tests := []struct {
		addr, token string
		wantErr     bool
	}{
		{"127.0.0.1:8080", "", false},      // loopback, no token: ok
		{"[::1]:8080", "", false},          // loopback v6, no token: ok
		{"localhost:8080", "", false},      // localhost, no token: ok
		{"0.0.0.0:8080", "", true},         // all interfaces, no token: refuse
		{"192.168.1.5:8080", "", true},     // LAN, no token: refuse
		{"0.0.0.0:8080", "tok", false},     // non-loopback with token: ok
		{"192.168.1.5:8080", "tok", false}, // LAN with token: ok
	}
	for _, tc := range tests {
		err := requireTokenOffLoopback(tc.addr, tc.token)
		if (err != nil) != tc.wantErr {
			t.Errorf("requireTokenOffLoopback(%q, token=%q) err=%v, wantErr=%v", tc.addr, tc.token, err, tc.wantErr)
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	tests := map[string]string{
		":8080":            "127.0.0.1:8080",
		"0.0.0.0:8080":     "0.0.0.0:8080",
		"127.0.0.1:9090":   "127.0.0.1:9090",
		"192.168.1.5:8080": "192.168.1.5:8080",
	}
	for in, want := range tests {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
