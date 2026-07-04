package server

import (
	"encoding/json"
	"strings"

	"github.com/ricoberger/prlsp/internal/lsp"
)

// extractSelection returns the trimmed text covered by r within docContent.
func extractSelection(docContent string, r lsp.Range) string {
	lines := strings.SplitAfter(docContent, "\n")
	if len(lines) == 0 {
		return ""
	}
	// SplitAfter keeps the newline; handle last empty element.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if r.Start.Line == r.End.Line {
		if r.Start.Line >= len(lines) {
			return ""
		}
		line := lines[r.Start.Line]
		start := min(r.Start.Character, len(line))
		end := min(r.End.Character, len(line))
		return strings.TrimSpace(line[start:end])
	}

	var parts []string
	for ln := r.Start.Line; ln <= r.End.Line && ln < len(lines); ln++ {
		lineText := lines[ln]
		switch ln {
		case r.Start.Line:
			start := min(r.Start.Character, len(lineText))
			parts = append(parts, lineText[start:])
		case r.End.Line:
			end := min(r.End.Character, len(lineText))
			parts = append(parts, lineText[:end])
		default:
			parts = append(parts, lineText)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// extractDiagData unwraps the diagnostic "data" payload, handling the case
// where the Neovim client nests the original under a "data" key.
func extractDiagData(data *json.RawMessage) map[string]any {
	if data == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(*data, &m); err != nil {
		return nil
	}
	if _, ok := m["thread_id"]; ok {
		return m
	}
	// Neovim wraps: data.data contains the original.
	if inner, ok := m["data"]; ok {
		if innerMap, ok := inner.(map[string]any); ok {
			return innerMap
		}
	}
	return m
}
