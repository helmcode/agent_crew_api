package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/models"
	"github.com/helmcode/agent-crew/internal/rag"
	"github.com/helmcode/agent-crew/internal/runtime"
)

// KnowledgeNetworkName is the Docker network used to connect shared RAG infra
// (Qdrant + Ollama) during document processing. It is created once and reused.
const KnowledgeNetworkName = "agentcrew-knowledge"

// MaxUploadSize is the maximum file size for document uploads (50MB).
const MaxUploadSize = 50 * 1024 * 1024

// knowledgeStorageBase is the base path for document storage.
const knowledgeStorageBase = "/data/knowledge"

// allowedExtensions maps file extensions to their MIME types for upload validation.
var allowedExtensions = map[string]bool{
	".pdf":  true,
	".txt":  true,
	".md":   true,
	".csv":  true,
	".xlsx": true,
	".json": true,
}

// GetKnowledgeStatus returns the current status of the knowledge base infrastructure.
func (s *Server) GetKnowledgeStatus(c *fiber.Ctx) error {
	orgID := GetOrgID(c)

	resp := KnowledgeStatusResponse{
		EmbeddingModel: "nomic-embed-text",
	}

	// Check Qdrant status.
	if qm, ok := s.runtime.(runtime.QdrantManager); ok {
		running, err := qm.IsQdrantRunning(c.Context())
		if err != nil {
			slog.Error("failed to check qdrant status", "error", err)
		}
		resp.QdrantRunning = running
	}

	// Query document counts.
	var totalCount, readyCount, processingCount, errorCount int64
	var totalChunks int64

	s.db.Model(&models.Document{}).Where("org_id = ?", orgID).Count(&totalCount)
	s.db.Model(&models.Document{}).Where("org_id = ? AND status = ?", orgID, models.DocStatusReady).Count(&readyCount)
	s.db.Model(&models.Document{}).Where("org_id = ? AND status = ?", orgID, models.DocStatusProcessing).Count(&processingCount)
	s.db.Model(&models.Document{}).Where("org_id = ? AND status = ?", orgID, models.DocStatusError).Count(&errorCount)

	// Sum total chunks.
	s.db.Model(&models.Document{}).Where("org_id = ? AND status = ?", orgID, models.DocStatusReady).
		Select("COALESCE(SUM(chunk_count), 0)").Scan(&totalChunks)

	resp.DocumentCount = int(totalCount)
	resp.ReadyCount = int(readyCount)
	resp.ProcessingCount = int(processingCount)
	resp.ErrorCount = int(errorCount)
	resp.TotalChunks = int(totalChunks)

	return c.JSON(resp)
}

// ListDocuments returns all documents for the current organization.
func (s *Server) ListDocuments(c *fiber.Ctx) error {
	orgID := GetOrgID(c)

	var docs []models.Document
	if err := s.db.Where("org_id = ?", orgID).Order("created_at DESC").Find(&docs).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list documents")
	}

	return c.JSON(docs)
}

// UploadDocument handles multipart file upload for knowledge base documents.
func (s *Server) UploadDocument(c *fiber.Ctx) error {
	orgID := GetOrgID(c)

	file, err := c.FormFile("file")
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "file is required")
	}

	// Validate file size.
	if file.Size > MaxUploadSize {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("file size %d exceeds maximum %d bytes", file.Size, MaxUploadSize))
	}
	if file.Size == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "file is empty")
	}

	// Sanitize and validate filename.
	sanitizedName := filepath.Base(file.Filename)
	ext := strings.ToLower(filepath.Ext(sanitizedName))
	if !allowedExtensions[ext] {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("unsupported file extension %q: allowed extensions are .pdf, .txt, .md, .csv, .xlsx, .json", ext))
	}

	// Detect MIME type from file content (magic bytes).
	f, err := file.Open()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to open uploaded file")
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	detectedMime := http.DetectContentType(buf[:n])

	// Map extension to expected MIME type and resolve the actual MIME to use.
	mimeType := resolveDocumentMime(ext, detectedMime)
	if mimeType == "" {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("file content does not match extension %q (detected: %s)", ext, detectedMime))
	}

	// Generate document ID and build storage path (server-side only).
	docID := uuid.New().String()
	storagePath := filepath.Join(knowledgeStorageBase, orgID, docID, sanitizedName)

	// Validate path is under the base directory (prevent traversal).
	relPath, err := filepath.Rel(knowledgeStorageBase, storagePath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid file path")
	}

	// Create directory and save file.
	storageDir := filepath.Dir(storagePath)
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		slog.Error("failed to create storage directory", "path", storageDir, "error", err)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create storage directory")
	}

	if err := c.SaveFile(file, storagePath); err != nil {
		slog.Error("failed to save file", "path", storagePath, "error", err)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save file")
	}

	// Optionally use the "name" form field for a display name; fallback to filename.
	displayName := c.FormValue("name")
	if displayName == "" {
		displayName = sanitizedName
	}

	doc := models.Document{
		ID:          docID,
		OrgID:       orgID,
		Name:        displayName,
		FileName:    sanitizedName,
		FileSize:    file.Size,
		MimeType:    mimeType,
		StoragePath: storagePath,
		Status:      models.DocStatusPending,
	}

	if err := s.db.Create(&doc).Error; err != nil {
		// Clean up saved file on DB error.
		os.RemoveAll(storageDir)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create document record")
	}

	// Process asynchronously.
	go s.processDocumentAsync(doc)

	return c.Status(fiber.StatusCreated).JSON(UploadDocumentResponse{
		Document: doc,
		Message:  "Document uploaded successfully. Processing will begin shortly.",
	})
}

// GetDocument returns a single document by ID.
func (s *Server) GetDocument(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	docID := c.Params("id")

	var doc models.Document
	if err := s.db.Where("id = ? AND org_id = ?", docID, orgID).First(&doc).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "document not found")
	}

	return c.JSON(doc)
}

// DeleteDocument removes a document, its Qdrant vectors, and its file from disk.
func (s *Server) DeleteDocument(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	docID := c.Params("id")

	var doc models.Document
	if err := s.db.Where("id = ? AND org_id = ?", docID, orgID).First(&doc).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "document not found")
	}

	// Delete vectors from Qdrant (best effort — Qdrant may not be running).
	if qm, ok := s.runtime.(runtime.QdrantManager); ok {
		running, _ := qm.IsQdrantRunning(c.Context())
		if running {
			collection := rag.CollectionName(orgID)
			qdrantClient := rag.NewQdrantClient(runtime.QdrantInternalURL)
			if err := qdrantClient.DeleteByDocID(c.Context(), collection, docID); err != nil {
				slog.Error("failed to delete vectors from qdrant", "doc_id", docID, "error", err)
				// Continue with deletion — don't block on Qdrant errors.
			}
		}
	}

	// Delete file from disk.
	if doc.StoragePath != "" {
		storageDir := filepath.Dir(doc.StoragePath)
		if err := os.RemoveAll(storageDir); err != nil {
			slog.Error("failed to delete file from disk", "path", storageDir, "error", err)
		}
	}

	// Delete from database.
	if err := s.db.Delete(&doc).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete document")
	}

	return c.JSON(fiber.Map{"message": "Document deleted successfully"})
}

// processDocumentAsync runs the full RAG pipeline for a document in a goroutine.
func (s *Server) processDocumentAsync(doc models.Document) {
	ctx := context.Background()

	// Ensure Qdrant is running.
	if qm, ok := s.runtime.(runtime.QdrantManager); ok {
		s.db.Model(&doc).Update("status_message", "Starting Qdrant...")
		if _, err := qm.EnsureQdrant(ctx); err != nil {
			slog.Error("failed to ensure qdrant for document processing", "doc_id", doc.ID, "error", err)
			s.db.Model(&doc).Updates(map[string]interface{}{
				"status":         models.DocStatusError,
				"status_message": "Failed to start Qdrant: " + err.Error(),
			})
			return
		}

		// Connect Qdrant to the knowledge network.
		if err := ensureKnowledgeNetwork(ctx, s.runtime); err != nil {
			slog.Error("failed to ensure knowledge network", "error", err)
		}
		if err := qm.ConnectQdrantToNetwork(ctx, KnowledgeNetworkName); err != nil {
			slog.Error("failed to connect qdrant to knowledge network", "error", err)
		}
	} else {
		slog.Error("runtime does not support QdrantManager", "doc_id", doc.ID)
		s.db.Model(&doc).Updates(map[string]interface{}{
			"status":         models.DocStatusError,
			"status_message": "Runtime does not support Qdrant",
		})
		return
	}

	// Ensure Ollama is running and pull embedding model.
	if om, ok := s.runtime.(runtime.OllamaManager); ok {
		s.db.Model(&doc).Update("status_message", "Starting Ollama...")
		if _, err := om.EnsureOllama(ctx); err != nil {
			slog.Error("failed to ensure ollama for document processing", "doc_id", doc.ID, "error", err)
			s.db.Model(&doc).Updates(map[string]interface{}{
				"status":         models.DocStatusError,
				"status_message": "Failed to start Ollama: " + err.Error(),
			})
			return
		}

		// Connect Ollama to the knowledge network.
		if err := om.ConnectOllamaToNetwork(ctx, KnowledgeNetworkName); err != nil {
			slog.Error("failed to connect ollama to knowledge network", "error", err)
		}

		s.db.Model(&doc).Update("status_message", "Pulling embedding model...")
		if err := om.PullOllamaModel(ctx, "nomic-embed-text", func(status string) {
			s.db.Model(&doc).Update("status_message", "Pulling model: "+status)
		}); err != nil {
			slog.Error("failed to pull embedding model", "doc_id", doc.ID, "error", err)
			s.db.Model(&doc).Updates(map[string]interface{}{
				"status":         models.DocStatusError,
				"status_message": "Failed to pull embedding model: " + err.Error(),
			})
			return
		}
	} else {
		slog.Error("runtime does not support OllamaManager", "doc_id", doc.ID)
		s.db.Model(&doc).Updates(map[string]interface{}{
			"status":         models.DocStatusError,
			"status_message": "Runtime does not support Ollama",
		})
		return
	}

	// Create RAG processor and process the document.
	qdrantClient := rag.NewQdrantClient(runtime.QdrantInternalURL)
	embedder := rag.NewOllamaEmbedder(runtime.OllamaInternalURL, "nomic-embed-text")
	processor := rag.NewProcessor(s.db, qdrantClient, embedder)

	if err := processor.ProcessDocument(ctx, doc); err != nil {
		slog.Error("document processing failed", "doc_id", doc.ID, "error", err)
		// Error status is set by the processor itself.
		return
	}

	slog.Info("document processed successfully", "doc_id", doc.ID, "name", doc.Name)
}

// ensureKnowledgeNetwork creates the dedicated Docker network for RAG infra
// and connects the API container to it so it can reach Qdrant/Ollama via DNS.
func ensureKnowledgeNetwork(ctx context.Context, rt runtime.AgentRuntime) error {
	nm, ok := rt.(runtime.NetworkManager)
	if !ok {
		slog.Warn("runtime does not support NetworkManager, skipping knowledge network setup")
		return nil
	}
	if err := nm.EnsureNetwork(ctx, KnowledgeNetworkName); err != nil {
		return fmt.Errorf("creating knowledge network: %w", err)
	}
	if err := nm.ConnectSelfToNetwork(ctx, KnowledgeNetworkName); err != nil {
		return fmt.Errorf("connecting API to knowledge network: %w", err)
	}
	return nil
}

// resolveDocumentMime maps a file extension and detected MIME type to the canonical MIME type.
// Returns empty string if the content doesn't match the extension.
func resolveDocumentMime(ext, detected string) string {
	switch ext {
	case ".pdf":
		if detected == "application/pdf" || detected == "application/octet-stream" {
			return rag.MimeTypePDF
		}
	case ".txt":
		if strings.HasPrefix(detected, "text/") || detected == "application/octet-stream" {
			return rag.MimeTypeText
		}
	case ".md":
		if strings.HasPrefix(detected, "text/") || detected == "application/octet-stream" {
			return rag.MimeTypeMD
		}
	case ".csv":
		if strings.HasPrefix(detected, "text/") || detected == "application/octet-stream" {
			return rag.MimeTypeCSV
		}
	case ".xlsx":
		// Excel files are detected as "application/zip" or "application/octet-stream" by magic bytes.
		if detected == "application/zip" || detected == "application/octet-stream" ||
			detected == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
			return rag.MimeTypeXLSX
		}
	case ".json":
		if strings.HasPrefix(detected, "text/") || detected == "application/json" || detected == "application/octet-stream" {
			return rag.MimeTypeJSON
		}
	}
	return ""
}
