package broker

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Optional offline integration test against a locally installed Hermes Python MCP SDK.
// Set JEV_HERMES_PYTHON to its interpreter; no API key or provider network is used.
func TestHermesSDKStatelessHandshakeOffline(t *testing.T) {
	python := os.Getenv("JEV_HERMES_PYTHON")
	if python == "" {
		t.Skip("set JEV_HERMES_PYTHON for offline Hermes SDK compatibility")
	}
	service, err := NewService(providerFunc(func(context.Context, ProviderRequest) ([]byte, error) {
		return []byte(syntheticReply), nil
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(testRegistry(t), service))
	defer server.Close()
	source := `import asyncio, sys
import mcp.client.streamable_http as transport
from mcp import ClientSession
async def main():
    httpx = transport.httpx2
    async with httpx.AsyncClient(headers={'Authorization': 'Bearer '+sys.argv[2], 'MCP-Protocol-Version': '2026-07-28'}) as client:
        async with transport.streamable_http_client(sys.argv[1], http_client=client) as streams:
            async with ClientSession(streams[0], streams[1]) as session:
                result = await session.discover()
                assert result is not None
                tools = await session.list_tools()
                assert any(t.name == 'evaluate' for t in tools.tools)
                print('Hermes SDK discover and list_tools passed; zero provider calls')
asyncio.run(main())`
	cmd := exec.Command(python, "-c", source, server.URL+"/mcp", testToken)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("local Hermes SDK handshake failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
	t.Log(strings.TrimSpace(string(output)))
}
