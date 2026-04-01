package rag

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// Supported MIME types for document parsing.
const (
	MimeTypePDF   = "application/pdf"
	MimeTypeText  = "text/plain"
	MimeTypeMD    = "text/markdown"
	MimeTypeCSV   = "text/csv"
	MimeTypeXLSX  = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	MimeTypeJSON  = "application/json"
)

// SupportedExtensions maps file extensions to their MIME types.
var SupportedExtensions = map[string]string{
	".pdf":  MimeTypePDF,
	".txt":  MimeTypeText,
	".md":   MimeTypeMD,
	".csv":  MimeTypeCSV,
	".xlsx": MimeTypeXLSX,
	".json": MimeTypeJSON,
}

// ParseDocument reads a file and extracts its text content based on MIME type.
func ParseDocument(filePath, mimeType string) (string, error) {
	switch mimeType {
	case MimeTypePDF:
		return parsePDF(filePath)
	case MimeTypeText, MimeTypeMD:
		return parseTextFile(filePath)
	case MimeTypeCSV:
		return parseCSV(filePath)
	case MimeTypeXLSX:
		return parseExcel(filePath)
	case MimeTypeJSON:
		return parseJSON(filePath)
	default:
		return "", fmt.Errorf("unsupported MIME type: %s", mimeType)
	}
}

func parsePDF(filePath string) (string, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("opening PDF: %w", err)
	}
	defer f.Close()

	var builder strings.Builder
	numPages := r.NumPage()
	for i := 1; i <= numPages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue // skip unreadable pages
		}
		builder.WriteString(text)
		if i < numPages {
			builder.WriteString("\n\n")
		}
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("PDF contains no extractable text")
	}
	return result, nil
}

func parseTextFile(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading text file: %w", err)
	}
	result := strings.TrimSpace(string(data))
	if result == "" {
		return "", fmt.Errorf("text file is empty")
	}
	return result, nil
}

func parseCSV(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("opening CSV: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1 // variable fields

	var builder strings.Builder
	rowNum := 0
	var headers []string

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading CSV row %d: %w", rowNum, err)
		}

		if rowNum == 0 {
			headers = record
			builder.WriteString("Headers: " + strings.Join(record, " | "))
			builder.WriteString("\n\n")
		} else {
			for i, val := range record {
				if i < len(headers) {
					builder.WriteString(headers[i] + ": " + val)
				} else {
					builder.WriteString(val)
				}
				if i < len(record)-1 {
					builder.WriteString(" | ")
				}
			}
			builder.WriteString("\n")
		}
		rowNum++
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("CSV file is empty")
	}
	return result, nil
}

func parseExcel(filePath string) (string, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return "", fmt.Errorf("opening Excel file: %w", err)
	}
	defer f.Close()

	var builder strings.Builder
	sheets := f.GetSheetList()

	for _, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		if len(rows) == 0 {
			continue
		}

		if len(sheets) > 1 {
			builder.WriteString("Sheet: " + sheet + "\n\n")
		}

		headers := rows[0]
		builder.WriteString("Headers: " + strings.Join(headers, " | "))
		builder.WriteString("\n\n")

		for i := 1; i < len(rows); i++ {
			for j, val := range rows[i] {
				if j < len(headers) {
					builder.WriteString(headers[j] + ": " + val)
				} else {
					builder.WriteString(val)
				}
				if j < len(rows[i])-1 {
					builder.WriteString(" | ")
				}
			}
			builder.WriteString("\n")
		}

		if len(sheets) > 1 {
			builder.WriteString("\n")
		}
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("Excel file is empty")
	}
	return result, nil
}

func parseJSON(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading JSON file: %w", err)
	}

	// Try to parse as JSON and re-format as readable text.
	var parsed interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("parsing JSON: %w", err)
	}

	text := flattenJSON("", parsed)
	result := strings.TrimSpace(text)
	if result == "" {
		return "", fmt.Errorf("JSON file is empty")
	}
	return result, nil
}

// flattenJSON converts a JSON value into human-readable text.
func flattenJSON(prefix string, v interface{}) string {
	var builder strings.Builder

	switch val := v.(type) {
	case map[string]interface{}:
		for k, v := range val {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			builder.WriteString(flattenJSON(key, v))
		}
	case []interface{}:
		for i, item := range val {
			key := fmt.Sprintf("%s[%d]", prefix, i)
			builder.WriteString(flattenJSON(key, item))
		}
	default:
		if prefix != "" {
			builder.WriteString(fmt.Sprintf("%s: %v\n", prefix, val))
		} else {
			builder.WriteString(fmt.Sprintf("%v\n", val))
		}
	}

	return builder.String()
}
