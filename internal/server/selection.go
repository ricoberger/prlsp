package server

import (
	"encoding/json"
	"strings"
	"unicode/utf16"

	"github.com/ricoberger/prlsp/internal/lsp"
)

// utf16OffsetToByte converts an LSP character offset (a count of UTF-16 code
// units) within line to a byte offset. Offsets past the end of the line are
// clamped to len(line), and an offset that would fall inside a surrogate pair
// or a multi-byte rune is rounded up to the following rune boundary so the
// returned index never splits a rune.
func utf16OffsetToByte(line string, offset int) int {
	if offset <= 0 {
		return 0
	}
	units := 0
	for i, r := range line {
		if units >= offset {
			return i
		}
		units += utf16.RuneLen(r)
	}
	return len(line)
}

// extractSelection returns the trimmed text covered by r within docContent.
// r's Character fields are LSP UTF-16 code-unit offsets, which are translated
// to byte offsets before slicing.
func extractSelection(docContent string, r lsp.Range) string {
	// SplitAfter keeps the trailing newline on each line and yields a final
	// empty element when docContent ends in "\n"; drop it so line indices line
	// up with the document.
	lines := strings.SplitAfter(docContent, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if r.Start.Line == r.End.Line {
		if r.Start.Line >= len(lines) {
			return ""
		}
		line := lines[r.Start.Line]
		start := utf16OffsetToByte(line, r.Start.Character)
		end := utf16OffsetToByte(line, r.End.Character)
		return strings.TrimSpace(line[start:end])
	}

	var parts []string
	for ln := r.Start.Line; ln <= r.End.Line && ln < len(lines); ln++ {
		lineText := lines[ln]
		switch ln {
		case r.Start.Line:
			start := utf16OffsetToByte(lineText, r.Start.Character)
			parts = append(parts, lineText[start:])
		case r.End.Line:
			end := utf16OffsetToByte(lineText, r.End.Character)
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
