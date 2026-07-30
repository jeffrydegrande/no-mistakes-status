package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// logTailBytes is how much of a log file to read. Step logs are usually small,
// but an agent that loops can write megabytes, and only the end is interesting.
const logTailBytes = 256 * 1024

// readTail returns the last max bytes of a file, dropping a leading partial
// line so the first thing shown is never half a sentence.
func readTail(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	truncated := false
	if info.Size() > max {
		if _, err := f.Seek(info.Size()-max, io.SeekStart); err != nil {
			return "", err
		}
		truncated = true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	text := string(data)
	if truncated {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text, nil
}

// logEntry is one unit of a step log: either a line the tool printed, or a
// structured payload an agent emitted.
type logEntry struct {
	text   string
	object map[string]any
}

// parseLog splits a raw log into entries. Agents write JSON payloads into the
// same stream as plain progress lines, in two shapes: pretty-printed across
// many lines, and several objects concatenated onto one line with no separator.
// Both have to come apart cleanly, or the log reads as a wall of braces.
func parseLog(raw string) []logEntry {
	var (
		entries []logEntry
		pending []string
	)

	flushPending := func() {
		for _, line := range pending {
			entries = append(entries, logEntry{text: line})
		}
		pending = nil
	}

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(pending) == 0 && !strings.HasPrefix(trimmed, "{") {
			if trimmed != "" {
				entries = append(entries, logEntry{text: trimmed})
			}
			continue
		}

		pending = append(pending, trimmed)
		candidate := strings.Join(pending, "\n")
		if objects, ok := decodeObjects(candidate); ok {
			for _, obj := range objects {
				entries = append(entries, logEntry{object: obj})
			}
			pending = nil
			continue
		}
		// A payload that never closes is a truncated or malformed write; give
		// up on it as JSON rather than swallowing the rest of the log.
		if len(pending) > 400 {
			flushPending()
		}
	}
	flushPending()
	return entries
}

// decodeObjects reads one or more JSON objects out of a string, which is how
// agents concatenate payloads. It succeeds only when the whole string is
// consumed, so a half-written payload is not mistaken for a complete one.
func decodeObjects(s string) ([]map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	var out []map[string]any
	for {
		var obj map[string]any
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		if obj == nil {
			return nil, false
		}
		out = append(out, obj)
	}
	if len(out) == 0 {
		return nil, false
	}
	// Anything left over is trailing garbage, which means this was not a clean
	// run of objects.
	var rest bytes.Buffer
	if _, err := rest.ReadFrom(dec.Buffered()); err == nil {
		if strings.TrimSpace(rest.String()) != "" {
			return nil, false
		}
	}
	return out, true
}

// preferredKeys are rendered first, in this order: an agent payload's point is
// usually its summary, not its schema.
var preferredKeys = []string{
	"summary", "testing_summary", "risk_level", "risk_rationale", "risk_scope",
	"findings", "tested", "artifacts", "fix_summary",
}

// renderLog turns a raw log into display lines, formatted for reading rather
// than for machines.
func renderLog(raw string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	for _, entry := range parseLog(raw) {
		if entry.object != nil {
			out = append(out, renderObject(entry.object, width)...)
			continue
		}
		for _, line := range wrap(entry.text, width) {
			out = append(out, logLineStyle(entry.text).Render(line))
		}
	}
	return out
}

// logLineStyle colors a plain log line by what it says happened.
func logLineStyle(line string) lipglossStyle {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "status=success"):
		return sGreen
	case strings.Contains(lower, "status=fail"), strings.Contains(lower, "error:"), strings.Contains(lower, "panic:"):
		return sRed
	case strings.HasSuffix(lower, "started") || strings.Contains(lower, " started pid="):
		return sDim
	case strings.Contains(lower, " exited pid="):
		return sDim
	default:
		return sText
	}
}

// renderObject prints a structured payload as labelled prose.
func renderObject(obj map[string]any, width int) []string {
	var out []string
	for _, key := range orderedKeys(obj) {
		value := obj[key]
		if isEmptyValue(value) {
			continue
		}
		for i, line := range renderValue(value, width-18) {
			label := ""
			if i == 0 {
				label = sAccent.Render(pad(key, 16)) + "  "
			} else {
				label = strings.Repeat(" ", 18)
			}
			out = append(out, label+line)
		}
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}

// orderedKeys puts the keys a human reads first at the top and the rest in a
// stable order, so a payload never shuffles between refreshes.
func orderedKeys(obj map[string]any) []string { return orderKeysWith(preferredKeys, obj) }

func orderKeysWith(first []string, obj map[string]any) []string {
	var rest []string
	seen := map[string]bool{}
	for key := range obj {
		seen[key] = true
	}
	var out []string
	for _, key := range first {
		if seen[key] {
			out = append(out, key)
			delete(seen, key)
		}
	}
	for key := range seen {
		rest = append(rest, key)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func renderValue(value any, width int) []string {
	switch v := value.(type) {
	case string:
		return wrapStyled(v, width, sText)
	case bool:
		return []string{sText.Render(fmt.Sprintf("%t", v))}
	case float64:
		return []string{sText.Render(formatNumber(v))}
	case []any:
		return renderList(v, width)
	case map[string]any:
		return renderInlineObject(v, width)
	default:
		return nil
	}
}

// renderList prints an array as bullets, capped so one long list cannot bury
// the rest of the payload.
func renderList(items []any, width int) []string {
	const maxItems = 8
	var out []string
	for i, item := range items {
		if i == maxItems {
			out = append(out, sDim.Render(fmt.Sprintf("… %d more", len(items)-maxItems)))
			break
		}
		lines := renderValue(item, width-2)
		for j, line := range lines {
			if j == 0 {
				out = append(out, sDim.Render("• ")+line)
				continue
			}
			out = append(out, "  "+line)
		}
	}
	return out
}

// inlineKeys orders the fields of a nested object. A finding reads as "how bad,
// where, what to do, what is wrong"; the schema's own key order does not.
var inlineKeys = []string{"severity", "file", "line", "action", "label", "description"}

// renderInlineObject prints a nested object as one compact line, which is what
// a finding or an artifact is: a handful of short fields.
func renderInlineObject(obj map[string]any, width int) []string {
	var parts []string
	for _, key := range orderKeysWith(inlineKeys, obj) {
		value := obj[key]
		if isEmptyValue(value) {
			continue
		}
		switch v := value.(type) {
		case string:
			parts = append(parts, key+"="+v)
		case float64:
			parts = append(parts, key+"="+formatNumber(v))
		case bool:
			parts = append(parts, fmt.Sprintf("%s=%t", key, v))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return wrapStyled(strings.Join(parts, "  "), width, sText)
}

func wrapStyled(text string, width int, style lipglossStyle) []string {
	var out []string
	for _, line := range wrap(text, width) {
		out = append(out, style.Render(line))
	}
	return out
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

// isEmptyValue hides nulls and empty collections: an agent payload is mostly
// unset fields, and printing them all buries the two that matter.
func isEmptyValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	default:
		return false
	}
}
