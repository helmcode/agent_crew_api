package rag

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
)

// Processor orchestrates document parsing, chunking, embedding, and indexing.
type Processor struct {
	db       *gorm.DB
	qdrant   *QdrantClient
	embedder *OllamaEmbedder
}

// NewProcessor creates a new RAG processor.
func NewProcessor(db *gorm.DB, qdrant *QdrantClient, embedder *OllamaEmbedder) *Processor {
	return &Processor{
		db:       db,
		qdrant:   qdrant,
		embedder: embedder,
	}
}

// EmbeddingBatchSize is the number of chunks to embed in a single API call.
const EmbeddingBatchSize = 50

// ProcessDocument takes a document through the full RAG pipeline:
// parse → chunk → embed → upsert to Qdrant → update status.
func (p *Processor) ProcessDocument(ctx context.Context, doc models.Document) error {
	// 1. Update status to processing.
	if err := p.db.Model(&doc).Updates(map[string]interface{}{
		"status":         models.DocStatusProcessing,
		"status_message": "Parsing document...",
	}).Error; err != nil {
		return fmt.Errorf("updating status to processing: %w", err)
	}

	// 2. Parse document.
	slog.Info("parsing document", "id", doc.ID, "file", doc.FileName, "mime", doc.MimeType)
	text, err := ParseDocument(doc.StoragePath, doc.MimeType)
	if err != nil {
		p.setError(doc.ID, "Failed to parse document: "+err.Error())
		return fmt.Errorf("parsing document: %w", err)
	}

	// 3. Chunk text.
	slog.Info("chunking document", "id", doc.ID, "text_len", len(text))
	p.db.Model(&doc).Update("status_message", "Chunking text...")

	cfg := DefaultChunkConfig()
	chunks := ChunkText(text, cfg)
	if len(chunks) == 0 {
		p.setError(doc.ID, "Document produced no chunks after parsing")
		return fmt.Errorf("no chunks produced")
	}

	slog.Info("document chunked", "id", doc.ID, "chunks", len(chunks))

	// Set metadata on each chunk.
	for i := range chunks {
		chunks[i].Metadata["doc_id"] = doc.ID
		chunks[i].Metadata["doc_name"] = doc.Name
		chunks[i].Metadata["org_id"] = doc.OrgID
		chunks[i].Metadata["file_name"] = doc.FileName
	}

	// 4. Ensure Qdrant collection exists for the org.
	p.db.Model(&doc).Update("status_message", "Preparing vector store...")
	if err := p.qdrant.EnsureCollection(ctx, doc.OrgID); err != nil {
		p.setError(doc.ID, "Failed to ensure Qdrant collection: "+err.Error())
		return fmt.Errorf("ensuring collection: %w", err)
	}

	// 5. Generate embeddings and upsert in batches.
	collection := CollectionName(doc.OrgID)
	totalPoints := 0

	for batchStart := 0; batchStart < len(chunks); batchStart += EmbeddingBatchSize {
		batchEnd := batchStart + EmbeddingBatchSize
		if batchEnd > len(chunks) {
			batchEnd = len(chunks)
		}
		batch := chunks[batchStart:batchEnd]

		p.db.Model(&doc).Update("status_message",
			fmt.Sprintf("Generating embeddings (%d/%d)...", batchStart+len(batch), len(chunks)))

		// Extract texts for embedding.
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}

		vectors, err := p.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			p.setError(doc.ID, "Failed to generate embeddings: "+err.Error())
			return fmt.Errorf("embedding batch at %d: %w", batchStart, err)
		}

		// Build Qdrant points.
		points := make([]Point, len(batch))
		for i, c := range batch {
			payload := make(map[string]interface{})
			for k, v := range c.Metadata {
				payload[k] = v
			}
			payload["content"] = c.Content
			payload["chunk_index"] = c.Index

			points[i] = Point{
				ID:      uuid.New().String(),
				Vector:  vectors[i],
				Payload: payload,
			}
		}

		// Upsert to Qdrant.
		if err := p.qdrant.UpsertPoints(ctx, collection, points); err != nil {
			p.setError(doc.ID, "Failed to upsert vectors: "+err.Error())
			return fmt.Errorf("upserting batch at %d: %w", batchStart, err)
		}

		totalPoints += len(points)
	}

	// 6. Update status to ready.
	if err := p.db.Model(&doc).Updates(map[string]interface{}{
		"status":         models.DocStatusReady,
		"status_message": "",
		"chunk_count":    totalPoints,
	}).Error; err != nil {
		return fmt.Errorf("updating status to ready: %w", err)
	}

	slog.Info("document processed successfully", "id", doc.ID, "chunks", totalPoints)
	return nil
}

// setError updates the document status to error with a message.
func (p *Processor) setError(docID, msg string) {
	p.db.Model(&models.Document{}).Where("id = ?", docID).Updates(map[string]interface{}{
		"status":         models.DocStatusError,
		"status_message": msg,
	})
}
