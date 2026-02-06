package mcp

import (
	"fmt"
	"strings"
)

// formatNumber adds comma separators to integers.
func formatNumber(n any) string {
	var s string
	switch v := n.(type) {
	case float64:
		if v == float64(int64(v)) {
			s = fmt.Sprintf("%d", int64(v))
		} else {
			return fmt.Sprintf("%.1f", v)
		}
	case int64:
		s = fmt.Sprintf("%d", v)
	case uint64:
		s = fmt.Sprintf("%d", v)
	case int:
		s = fmt.Sprintf("%d", v)
	default:
		return fmt.Sprintf("%v", n)
	}

	if len(s) <= 3 {
		return s
	}

	var result strings.Builder
	start := len(s) % 3
	if start > 0 {
		result.WriteString(s[:start])
	}
	for i := start; i < len(s); i += 3 {
		if result.Len() > 0 {
			result.WriteByte(',')
		}
		result.WriteString(s[i : i+3])
	}
	return result.String()
}

// kv formats a key-value pair with aligned values (20 char key width).
func kv(key string, value any) string {
	return fmt.Sprintf("%-20s %v", key+":", value)
}

// section returns a markdown section header.
func section(title string) string {
	return "## " + title
}

// joinLines joins non-empty lines with newlines.
func joinLines(lines ...string) string {
	var result []string
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return strings.Join(result, "\n")
}

// formatPct formats a float as a percentage string.
func formatPct(v float64) string {
	return fmt.Sprintf("%.1f%%", v)
}

// formatMs formats milliseconds with a "ms" suffix.
func formatMs(v float64) string {
	return fmt.Sprintf("%.1fms", v)
}
