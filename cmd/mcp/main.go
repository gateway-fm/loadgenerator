// Load Generator MCP server.
// Exposes load generator tools over MCP stdio transport.
package main

import (
	"fmt"
	"os"

	mcptools "github.com/gateway-fm/loadgenerator/internal/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	loadgenURL := os.Getenv("LOADGEN_URL")
	if loadgenURL == "" {
		loadgenURL = "http://localhost:13001"
	}

	s := server.NewMCPServer(
		"loadgenerator",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	client := mcptools.NewClient(loadgenURL)
	mcptools.RegisterTools(s, client)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
