package termutil

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// TruncateString truncates a string to at most maxWidth visual columns based on rune display width.
// If truncated, tail (e.g. "...") is appended.
// It never panics on narrow or non-positive maxWidth and never breaks multi-byte UTF-8 sequences.
func TruncateString(s string, maxWidth int, tail string) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	tailWidth := runewidth.StringWidth(tail)
	if maxWidth <= tailWidth {
		var b strings.Builder
		cur := 0
		for _, r := range tail {
			rw := runewidth.RuneWidth(r)
			if cur+rw > maxWidth {
				break
			}
			b.WriteRune(r)
			cur += rw
		}
		return b.String()
	}
	return runewidth.Truncate(s, maxWidth, tail)
}

// TruncateLeft truncates a string from the left (keeping the right tail),
// prepending prefix (e.g. "...") if truncated, fitting within maxWidth visual columns.
// Safe for paths, CJK characters, emojis, and narrow/non-positive maxWidth.
func TruncateLeft(s string, maxWidth int, prefix string) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	prefixWidth := runewidth.StringWidth(prefix)
	if maxWidth <= prefixWidth {
		return TruncateString(prefix, maxWidth, "")
	}
	targetWidth := maxWidth - prefixWidth
	runes := []rune(s)
	curWidth := 0
	startIndex := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := runewidth.RuneWidth(runes[i])
		if curWidth+rw > targetWidth {
			break
		}
		curWidth += rw
		startIndex = i
	}
	return prefix + string(runes[startIndex:])
}

// PadRight pads s with spaces on the right to visual column width.
// If s is already >= width visual columns, s is returned unchanged.
func PadRight(s string, width int) string {
	w := runewidth.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}
