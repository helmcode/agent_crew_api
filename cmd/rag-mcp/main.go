// Package main implements the RAG MCP server for AgentCrew.
// It exposes search_knowledge and list_documents tools via the MCP protocol
// (Streamable HTTP transport) so that agent containers can query the
// organization's knowledge base.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/helmcode/agent-crew/internal/rag"
)

func main() {
	qdrantURL := envOrDefault("QDRANT_URL", "http://agentcrew-qdrant:6333")
	ollamaURL := envOrDefault("OLLAMA_URL", "http://agentcrew-ollama:11434")
	listenAddr := envOrDefault("LISTEN_ADDR", ":8090")

	slog.Info("starting RAG MCP server",
		"qdrant_url", qdrantURL,
		"ollama_url", ollamaURL,
		"listen_addr", listenAddr,
	)

	qdrantClient := rag.NewQdrantClient(qdrantURL)
	embedder := rag.NewOllamaEmbedder(ollamaURL, "nomic-embed-text")

	mcpServer := server.NewMCPServer(
		"agentcrew-knowledge-base",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register search_knowledge tool.
	mcpServer.AddTool(
		mcp.NewTool("search_knowledge",
			mcp.WithDescription("Search the organization's knowledge base for relevant information. Returns the most relevant document chunks based on semantic similarity."),
			mcp.WithString("query",
				mcp.Description("The search query to find relevant documents"),
				mcp.Required(),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of results to return"),
				mcp.DefaultNumber(5),
			),
			mcp.WithNumber("min_score",
				mcp.Description("Minimum similarity score threshold (0.0 to 1.0)"),
				mcp.DefaultNumber(0.4),
			),
		),
		makeSearchHandler(qdrantClient, embedder),
	)

	// Register list_documents tool.
	mcpServer.AddTool(
		mcp.NewTool("list_documents",
			mcp.WithDescription("List all documents in the organization's knowledge base. Optionally filter by status."),
			mcp.WithString("status",
				mcp.Description("Filter by document status: pending, processing, ready, error. Leave empty for all."),
			),
		),
		makeListDocumentsHandler(qdrantClient),
	)

	// Create a custom HTTP mux to add a health endpoint alongside MCP.
	mux := http.NewServeMux()

	// Health endpoint for Docker healthcheck.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Mount the MCP Streamable HTTP handler.
	httpServer := server.NewStreamableHTTPServer(mcpServer)
	mux.Handle("/mcp", httpServer)

	slog.Info("RAG MCP server listening", "addr", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// getArgs extracts the arguments map from a CallToolRequest.
func getArgs(request mcp.CallToolRequest) map[string]interface{} {
	if args, ok := request.Params.Arguments.(map[string]interface{}); ok {
		return args
	}
	return map[string]interface{}{}
}

// makeSearchHandler creates a tool handler for search_knowledge.
func makeSearchHandler(qdrant *rag.QdrantClient, embedder *rag.OllamaEmbedder) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgID := extractOrgID(request)
		if orgID == "" {
			slog.Warn("search_knowledge called without X-Org-ID")
			return mcp.NewToolResultError("X-Org-ID header is required"), nil
		}

		args := getArgs(request)

		query, _ := args["query"].(string)
		if query == "" {
			return mcp.NewToolResultError("query parameter is required"), nil
		}

		limit := 5
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}

		minScore := 0.4
		if ms, ok := args["min_score"].(float64); ok {
			minScore = ms
		}

		slog.Info("search_knowledge called", "org_id", orgID, "query", query, "limit", limit, "min_score", minScore)

		// Generate embedding for the query.
		vector, err := embedder.Embed(ctx, query)
		if err != nil {
			slog.Error("failed to embed query", "error", err)
			return mcp.NewToolResultError("Failed to generate query embedding: " + err.Error()), nil
		}

		// Search Qdrant.
		collection := rag.CollectionName(orgID)
		results, err := qdrant.Search(ctx, collection, vector, limit, minScore)
		if err != nil {
			slog.Error("failed to search qdrant", "error", err, "collection", collection)
			return mcp.NewToolResultError("Failed to search knowledge base: " + err.Error()), nil
		}

		slog.Info("search_knowledge results", "query", query, "results", len(results), "collection", collection)

		if len(results) == 0 {
			return mcp.NewToolResultText("No relevant documents found for the query."), nil
		}

		// Format results as readable text.
		var output string
		for i, r := range results {
			content, _ := r.Payload["content"].(string)
			docName, _ := r.Payload["doc_name"].(string)
			fileName, _ := r.Payload["file_name"].(string)
			chunkIdx := 0
			if ci, ok := r.Payload["chunk_index"].(float64); ok {
				chunkIdx = int(ci)
			}

			output += fmt.Sprintf("--- Result %d (score: %.2f) ---\n", i+1, r.Score)
			output += fmt.Sprintf("Source: %s (%s), chunk %d\n", docName, fileName, chunkIdx)
			output += content + "\n\n"
		}

		return mcp.NewToolResultText(output), nil
	}
}

// makeListDocumentsHandler creates a tool handler for list_documents.
func makeListDocumentsHandler(qdrant *rag.QdrantClient) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		orgID := extractOrgID(request)
		if orgID == "" {
			slog.Warn("list_documents called without X-Org-ID")
			return mcp.NewToolResultError("X-Org-ID header is required"), nil
		}

		args := getArgs(request)
		status, _ := args["status"].(string)

		slog.Info("list_documents called", "org_id", orgID, "status_filter", status)

		// Check if the collection exists.
		collection := rag.CollectionName(orgID)
		exists, err := qdrant.CollectionExists(ctx, collection)
		if err != nil {
			return mcp.NewToolResultError("Failed to check collection: " + err.Error()), nil
		}

		if !exists {
			return mcp.NewToolResultText("No knowledge base found for this organization. Upload documents first."), nil
		}

		// Return collection info.
		var output string
		output += fmt.Sprintf("Knowledge base collection: %s\n", collection)
		output += "Status: exists\n"
		if status != "" {
			output += fmt.Sprintf("Filter: status=%s\n", status)
		}
		output += "\nNote: For detailed document listing, use the Knowledge Base page in the UI."

		return mcp.NewToolResultText(output), nil
	}
}

// extractOrgID extracts the organization ID from the MCP request.
// The org ID is passed via the X-Org-ID HTTP header which is injected when the
// MCP server config is added to the agent's environment.
func extractOrgID(request mcp.CallToolRequest) string {
	// Try HTTP headers first (set by the MCP client config).
	if request.Header != nil {
		if orgID := request.Header.Get("X-Org-ID"); orgID != "" {
			return orgID
		}
	}

	// Fallback: try environment variable (set per-container).
	if orgID := os.Getenv("ORG_ID"); orgID != "" {
		return orgID
	}

	return ""
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
