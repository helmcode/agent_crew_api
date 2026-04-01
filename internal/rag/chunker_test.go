package rag

import (
	"strings"
	"testing"
)

func TestChunkText_BasicSplit(t *testing.T) {
	text := strings.Repeat("Hello world. ", 100) // ~1300 chars
	cfg := ChunkConfig{ChunkSize: 500, ChunkOverlap: 0}

	chunks := ChunkText(text, cfg)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	for i, c := range chunks {
		if len(c.Content) > cfg.ChunkSize+50 { // small tolerance for word boundaries
			t.Errorf("chunk %d exceeds max size: %d > %d", i, len(c.Content), cfg.ChunkSize)
		}
		if c.Index != i {
			t.Errorf("chunk %d has wrong index: %d", i, c.Index)
		}
		if c.Metadata == nil {
			t.Errorf("chunk %d has nil metadata", i)
		}
	}
}

func TestChunkText_SmallText(t *testing.T) {
	text := "Small text that fits in one chunk."
	cfg := DefaultChunkConfig()

	chunks := ChunkText(text, cfg)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Content != text {
		t.Errorf("chunk content mismatch: got %q", chunks[0].Content)
	}
	if chunks[0].Index != 0 {
		t.Errorf("expected index 0, got %d", chunks[0].Index)
	}
}

func TestChunkText_EmptyText(t *testing.T) {
	chunks := ChunkText("", DefaultChunkConfig())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty text, got %d", len(chunks))
	}
}

func TestChunkText_WhitespaceOnly(t *testing.T) {
	chunks := ChunkText("   \n\n  \n  ", DefaultChunkConfig())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for whitespace text, got %d", len(chunks))
	}
}

func TestChunkText_ParagraphSplit(t *testing.T) {
	// Create text with clear paragraph boundaries.
	para1 := strings.Repeat("First paragraph sentence. ", 30) // ~750 chars
	para2 := strings.Repeat("Second paragraph sentence. ", 30)
	text := para1 + "\n\n" + para2

	cfg := ChunkConfig{ChunkSize: 800, ChunkOverlap: 0}
	chunks := ChunkText(text, cfg)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for 2 paragraphs, got %d", len(chunks))
	}

	// First chunk should contain first paragraph content.
	if !strings.Contains(chunks[0].Content, "First paragraph") {
		t.Error("first chunk should contain first paragraph content")
	}
}

func TestChunkText_Overlap(t *testing.T) {
	// Create text long enough to produce multiple chunks.
	text := strings.Repeat("Word ", 300) // ~1500 chars
	cfg := ChunkConfig{ChunkSize: 500, ChunkOverlap: 100}

	chunks := ChunkText(text, cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// With overlap, consecutive chunks should share some content.
	// This is a structural test — we just verify multiple chunks are produced
	// and they have valid indices.
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has wrong index %d", i, c.Index)
		}
	}
}

func TestChunkText_InvalidConfig(t *testing.T) {
	text := "Some text to chunk."

	// Zero chunk size should use default.
	chunks := ChunkText(text, ChunkConfig{ChunkSize: 0, ChunkOverlap: 0})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk with zero config, got %d", len(chunks))
	}

	// Negative overlap should be treated as 0.
	chunks = ChunkText(text, ChunkConfig{ChunkSize: 100, ChunkOverlap: -5})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}

	// Overlap >= ChunkSize should be clamped.
	chunks = ChunkText(text, ChunkConfig{ChunkSize: 100, ChunkOverlap: 200})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestDefaultChunkConfig(t *testing.T) {
	cfg := DefaultChunkConfig()
	if cfg.ChunkSize != 1000 {
		t.Errorf("default ChunkSize: got %d, want 1000", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != 200 {
		t.Errorf("default ChunkOverlap: got %d, want 200", cfg.ChunkOverlap)
	}
}
