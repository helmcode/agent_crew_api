// Package rag implements the RAG (Retrieval-Augmented Generation) processing pipeline.
package rag

import (
	"strings"
)

// ChunkConfig controls the text splitting behavior.
type ChunkConfig struct {
	ChunkSize    int // maximum characters per chunk
	ChunkOverlap int // characters of overlap between consecutive chunks
}

// DefaultChunkConfig returns the default chunking configuration.
func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		ChunkSize:    1000,
		ChunkOverlap: 200,
	}
}

// Chunk represents a single piece of split text with metadata.
type Chunk struct {
	Content  string            `json:"content"`
	Index    int               `json:"index"`
	Metadata map[string]string `json:"metadata"`
}

// ChunkText splits text into overlapping chunks using recursive character splitting.
// It splits by paragraph (\n\n), then line (\n), then sentence (. ), then space ( ).
func ChunkText(text string, cfg ChunkConfig) []Chunk {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 1000
	}
	if cfg.ChunkOverlap < 0 {
		cfg.ChunkOverlap = 0
	}
	if cfg.ChunkOverlap >= cfg.ChunkSize {
		cfg.ChunkOverlap = cfg.ChunkSize / 5
	}

	separators := []string{"\n\n", "\n", ". ", " "}
	pieces := recursiveSplit(text, separators, cfg.ChunkSize)

	// Merge small pieces into chunks respecting size and overlap.
	var chunks []Chunk
	idx := 0

	for i := 0; i < len(pieces); {
		var builder strings.Builder
		j := i
		for j < len(pieces) {
			candidate := pieces[j]
			if builder.Len() > 0 {
				candidate = " " + candidate
			}
			if builder.Len()+len(candidate) > cfg.ChunkSize && builder.Len() > 0 {
				break
			}
			builder.WriteString(candidate)
			j++
		}

		content := strings.TrimSpace(builder.String())
		if content != "" {
			chunks = append(chunks, Chunk{
				Content:  content,
				Index:    idx,
				Metadata: map[string]string{},
			})
			idx++
		}

		// Advance with overlap: step back to include overlap characters.
		if cfg.ChunkOverlap > 0 && j > i {
			overlapStart := j - 1
			overlapLen := 0
			for overlapStart > i && overlapLen < cfg.ChunkOverlap {
				overlapLen += len(pieces[overlapStart])
				overlapStart--
			}
			overlapStart++
			if overlapStart < j {
				i = overlapStart
			} else {
				i = j
			}
		} else {
			i = j
		}
	}

	return chunks
}

// recursiveSplit splits text using separators in order of preference.
// If the first separator produces pieces small enough, use it.
// Otherwise, recurse with the next separator on oversized pieces.
func recursiveSplit(text string, separators []string, maxSize int) []string {
	if len(text) <= maxSize {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	}

	if len(separators) == 0 {
		// No more separators — hard split at maxSize.
		var pieces []string
		for len(text) > 0 {
			end := maxSize
			if end > len(text) {
				end = len(text)
			}
			piece := strings.TrimSpace(text[:end])
			if piece != "" {
				pieces = append(pieces, piece)
			}
			text = text[end:]
		}
		return pieces
	}

	sep := separators[0]
	remaining := separators[1:]
	parts := strings.Split(text, sep)

	var result []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if len(trimmed) <= maxSize {
			result = append(result, trimmed)
		} else {
			// Recurse with next separator.
			result = append(result, recursiveSplit(trimmed, remaining, maxSize)...)
		}
	}

	return result
}
