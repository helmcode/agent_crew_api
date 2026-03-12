package main

import (
	"encoding/json"
	"testing"

	"github.com/helmcode/agent-crew/internal/protocol"
)

func TestBuildInitialMcpStatus_EmptyEnv(t *testing.T) {
	_, ok := buildInitialMcpStatus("", "leader")
	if ok {
		t.Error("expected ok=false for empty env")
	}
}

func TestBuildInitialMcpStatus_InvalidJSON(t *testing.T) {
	_, ok := buildInitialMcpStatus("{not valid json", "leader")
	if ok {
		t.Error("expected ok=false for invalid JSON")
	}
}

func TestBuildInitialMcpStatus_EmptyArray(t *testing.T) {
	_, ok := buildInitialMcpStatus("[]", "leader")
	if ok {
		t.Error("expected ok=false for empty array")
	}
}

func TestBuildInitialMcpStatus_SingleStdioServer(t *testing.T) {
	servers := []protocol.McpServerConfig{
		{Name: "postgres", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/postgres"}},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "my-agent")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if payload.AgentName != "my-agent" {
		t.Errorf("AgentName: got %q, want %q", payload.AgentName, "my-agent")
	}
	if len(payload.Servers) != 1 {
		t.Fatalf("Servers length: got %d, want 1", len(payload.Servers))
	}
	if payload.Servers[0].Name != "postgres" {
		t.Errorf("Server name: got %q, want %q", payload.Servers[0].Name, "postgres")
	}
	if payload.Servers[0].Status != "configured" {
		t.Errorf("Server status: got %q, want %q", payload.Servers[0].Status, "configured")
	}
	if payload.Summary != "1 MCP server(s) configured" {
		t.Errorf("Summary: got %q, want %q", payload.Summary, "1 MCP server(s) configured")
	}
}

func TestBuildInitialMcpStatus_MultipleServers(t *testing.T) {
	servers := []protocol.McpServerConfig{
		{Name: "postgres", Transport: "stdio", Command: "npx"},
		{Name: "github", Transport: "http", URL: "https://mcp.github.com"},
		{Name: "slack", Transport: "sse", URL: "https://mcp.slack.com/sse"},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "leader")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if len(payload.Servers) != 3 {
		t.Fatalf("Servers length: got %d, want 3", len(payload.Servers))
	}

	expectedNames := []string{"postgres", "github", "slack"}
	for i, name := range expectedNames {
		if payload.Servers[i].Name != name {
			t.Errorf("Server[%d] name: got %q, want %q", i, payload.Servers[i].Name, name)
		}
		if payload.Servers[i].Status != "configured" {
			t.Errorf("Server[%d] status: got %q, want %q", i, payload.Servers[i].Status, "configured")
		}
		if payload.Servers[i].Error != "" {
			t.Errorf("Server[%d] error: got %q, want empty", i, payload.Servers[i].Error)
		}
	}

	if payload.Summary != "3 MCP server(s) configured" {
		t.Errorf("Summary: got %q, want %q", payload.Summary, "3 MCP server(s) configured")
	}
}

func TestBuildInitialMcpStatus_AllStatusesAreConfigured(t *testing.T) {
	// Even servers with env vars or complex configs should get "configured" status.
	servers := []protocol.McpServerConfig{
		{
			Name:      "db",
			Transport: "stdio",
			Command:   "uvx",
			Args:      []string{"postgres-mcp"},
			Env:       map[string]string{"DATABASE_URL": "postgres://localhost/test"},
		},
		{
			Name:      "api",
			Transport: "http",
			URL:       "https://api.example.com/mcp",
			Headers:   map[string]string{"Authorization": "Bearer token123"},
		},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "worker-1")
	if !ok {
		t.Fatal("expected ok=true")
	}

	for i, s := range payload.Servers {
		if s.Status != "configured" {
			t.Errorf("Server[%d] %q: got status %q, want 'configured'", i, s.Name, s.Status)
		}
	}
}

func TestBuildInitialMcpStatus_AgentNamePassthrough(t *testing.T) {
	servers := []protocol.McpServerConfig{
		{Name: "test", Transport: "stdio", Command: "echo"},
	}
	input, _ := json.Marshal(servers)

	testNames := []string{"leader", "worker-alpha", "my_agent_123"}
	for _, name := range testNames {
		payload, ok := buildInitialMcpStatus(string(input), name)
		if !ok {
			t.Fatalf("expected ok=true for agent name %q", name)
		}
		if payload.AgentName != name {
			t.Errorf("AgentName: got %q, want %q", payload.AgentName, name)
		}
	}
}

func TestBuildInitialMcpStatus_ServerNamesPreserved(t *testing.T) {
	// Verify server names with special characters are preserved as-is.
	servers := []protocol.McpServerConfig{
		{Name: "my-mcp-server", Transport: "stdio", Command: "npx"},
		{Name: "MCP_Server_v2", Transport: "http", URL: "https://example.com"},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "agent")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if payload.Servers[0].Name != "my-mcp-server" {
		t.Errorf("Server[0] name: got %q, want %q", payload.Servers[0].Name, "my-mcp-server")
	}
	if payload.Servers[1].Name != "MCP_Server_v2" {
		t.Errorf("Server[1] name: got %q, want %q", payload.Servers[1].Name, "MCP_Server_v2")
	}
}

func TestBuildInitialMcpStatus_PayloadSerializesCorrectly(t *testing.T) {
	servers := []protocol.McpServerConfig{
		{Name: "test-server", Transport: "stdio", Command: "npx"},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "leader")
	if !ok {
		t.Fatal("expected ok=true")
	}

	// Verify the payload can be serialized and deserialized (round-trip).
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	var decoded protocol.McpStatusPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if decoded.AgentName != "leader" {
		t.Errorf("round-trip AgentName: got %q, want %q", decoded.AgentName, "leader")
	}
	if len(decoded.Servers) != 1 {
		t.Fatalf("round-trip Servers length: got %d, want 1", len(decoded.Servers))
	}
	if decoded.Servers[0].Name != "test-server" {
		t.Errorf("round-trip Server name: got %q, want %q", decoded.Servers[0].Name, "test-server")
	}
	if decoded.Servers[0].Status != "configured" {
		t.Errorf("round-trip Server status: got %q, want %q", decoded.Servers[0].Status, "configured")
	}
	if decoded.Summary != "1 MCP server(s) configured" {
		t.Errorf("round-trip Summary: got %q, want %q", decoded.Summary, "1 MCP server(s) configured")
	}
}

func TestBuildInitialMcpStatus_NoErrorFieldsSet(t *testing.T) {
	// Verify that no Error fields are set on configured servers.
	servers := []protocol.McpServerConfig{
		{Name: "a", Transport: "stdio", Command: "cmd1"},
		{Name: "b", Transport: "http", URL: "https://example.com"},
		{Name: "c", Transport: "sse", URL: "https://sse.example.com"},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "agent")
	if !ok {
		t.Fatal("expected ok=true")
	}

	for i, s := range payload.Servers {
		if s.Error != "" {
			t.Errorf("Server[%d] %q should have no error, got %q", i, s.Name, s.Error)
		}
	}
}

func TestBuildInitialMcpStatus_MalformedJSONArray(t *testing.T) {
	// Valid JSON but not an array of McpServerConfig.
	_, ok := buildInitialMcpStatus(`{"name": "test"}`, "agent")
	if ok {
		t.Error("expected ok=false for non-array JSON")
	}
}

func TestBuildInitialMcpStatus_PartialServerFields(t *testing.T) {
	// Servers with only name field — buildInitialMcpStatus doesn't validate
	// server config, it only extracts names and sets "configured".
	servers := []protocol.McpServerConfig{
		{Name: "minimal"},
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "agent")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if payload.Servers[0].Name != "minimal" {
		t.Errorf("Server name: got %q, want %q", payload.Servers[0].Name, "minimal")
	}
	if payload.Servers[0].Status != "configured" {
		t.Errorf("Server status: got %q, want %q", payload.Servers[0].Status, "configured")
	}
}

func TestBuildInitialMcpStatus_LargeServerList(t *testing.T) {
	// Verify it handles a large number of servers.
	servers := make([]protocol.McpServerConfig, 50)
	for i := range servers {
		servers[i] = protocol.McpServerConfig{
			Name:      "server-" + string(rune('a'+i%26)),
			Transport: "stdio",
			Command:   "cmd",
		}
	}
	input, _ := json.Marshal(servers)

	payload, ok := buildInitialMcpStatus(string(input), "agent")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if len(payload.Servers) != 50 {
		t.Errorf("Servers length: got %d, want 50", len(payload.Servers))
	}
	if payload.Summary != "50 MCP server(s) configured" {
		t.Errorf("Summary: got %q, want %q", payload.Summary, "50 MCP server(s) configured")
	}
}
