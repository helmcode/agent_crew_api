package rag

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDocument_TextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "Hello, this is a test document.\nIt has multiple lines.\n\nAnd paragraphs."
	os.WriteFile(path, []byte(content), 0644)

	text, err := ParseDocument(path, MimeTypeText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != content {
		t.Errorf("got %q, want %q", text, content)
	}
}

func TestParseDocument_MarkdownFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := "# Title\n\nSome markdown content with **bold** text."
	os.WriteFile(path, []byte(content), 0644)

	text, err := ParseDocument(path, MimeTypeMD)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != content {
		t.Errorf("got %q, want %q", text, content)
	}
}

func TestParseDocument_EmptyTextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte(""), 0644)

	_, err := ParseDocument(path, MimeTypeText)
	if err == nil {
		t.Error("expected error for empty file")
	}
}

func TestParseDocument_CSVFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.csv")

	f, _ := os.Create(path)
	w := csv.NewWriter(f)
	w.Write([]string{"Name", "Age", "City"})
	w.Write([]string{"Alice", "30", "NYC"})
	w.Write([]string{"Bob", "25", "LA"})
	w.Flush()
	f.Close()

	text, err := ParseDocument(path, MimeTypeCSV)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(text, "Headers: Name | Age | City") {
		t.Error("expected headers in output")
	}
	if !strings.Contains(text, "Name: Alice") {
		t.Error("expected first row data in output")
	}
	if !strings.Contains(text, "Age: 25") {
		t.Error("expected second row data in output")
	}
}

func TestParseDocument_JSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	data := map[string]interface{}{
		"name": "AgentCrew",
		"version": "1.0",
	}
	jsonData, _ := json.Marshal(data)
	os.WriteFile(path, jsonData, 0644)

	text, err := ParseDocument(path, MimeTypeJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(text, "name: AgentCrew") {
		t.Errorf("expected 'name: AgentCrew' in output, got: %s", text)
	}
	if !strings.Contains(text, "version: 1") {
		t.Errorf("expected 'version: 1' in output, got: %s", text)
	}
}

func TestParseDocument_JSONArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	data := []map[string]string{
		{"key": "value1"},
		{"key": "value2"},
	}
	jsonData, _ := json.Marshal(data)
	os.WriteFile(path, jsonData, 0644)

	text, err := ParseDocument(path, MimeTypeJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "value1") {
		t.Error("expected value1 in output")
	}
	if !strings.Contains(text, "value2") {
		t.Error("expected value2 in output")
	}
}

func TestParseDocument_UnsupportedMime(t *testing.T) {
	_, err := ParseDocument("/tmp/fake.xyz", "application/octet-stream")
	if err == nil {
		t.Error("expected error for unsupported MIME type")
	}
	if !strings.Contains(err.Error(), "unsupported MIME type") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseDocument_NonexistentFile(t *testing.T) {
	_, err := ParseDocument("/nonexistent/path/file.txt", MimeTypeText)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestSupportedExtensions(t *testing.T) {
	expected := map[string]string{
		".pdf":  MimeTypePDF,
		".txt":  MimeTypeText,
		".md":   MimeTypeMD,
		".csv":  MimeTypeCSV,
		".xlsx": MimeTypeXLSX,
		".json": MimeTypeJSON,
	}

	for ext, mime := range expected {
		got, ok := SupportedExtensions[ext]
		if !ok {
			t.Errorf("extension %s not in SupportedExtensions", ext)
			continue
		}
		if got != mime {
			t.Errorf("SupportedExtensions[%s] = %q, want %q", ext, got, mime)
		}
	}
}

func TestFlattenJSON(t *testing.T) {
	nested := map[string]interface{}{
		"a": map[string]interface{}{
			"b": "deep",
		},
		"arr": []interface{}{"x", "y"},
	}

	text := flattenJSON("", nested)
	if !strings.Contains(text, "a.b: deep") {
		t.Errorf("expected 'a.b: deep' in output, got: %s", text)
	}
	if !strings.Contains(text, "arr[0]: x") {
		t.Errorf("expected 'arr[0]: x' in output, got: %s", text)
	}
}
