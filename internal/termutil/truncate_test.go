package termutil_test

import (
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
)

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		tail     string
	}{
		{name: "empty string", input: "", maxWidth: 10, tail: "..."},
		{name: "narrow negative", input: "Hello World", maxWidth: -5, tail: "..."},
		{name: "narrow zero", input: "Hello World", maxWidth: 0, tail: "..."},
		{name: "narrow 1", input: "Hello World", maxWidth: 1, tail: "..."},
		{name: "narrow 2", input: "Hello World", maxWidth: 2, tail: "..."},
		{name: "narrow 3", input: "Hello World", maxWidth: 3, tail: "..."},
		{name: "fits exactly", input: "Hello", maxWidth: 5, tail: "..."},
		{name: "longer ascii", input: "Hello World from Antigravity", maxWidth: 10, tail: "..."},
		{name: "cjk characters", input: "优化数据库连接池与查询性能", maxWidth: 12, tail: "..."},
		{name: "emoji and cjk", input: "🚀🔥Agent任务执行完成", maxWidth: 10, tail: "..."},
		{name: "emoji alone", input: "🚀🎉✨🔥💡", maxWidth: 6, tail: "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := termutil.TruncateString(tt.input, tt.maxWidth, tt.tail)

			// 1. Must be valid UTF-8
			if !utf8.ValidString(got) {
				t.Fatalf("TruncateString produced invalid UTF-8: %q", got)
			}

			// 2. Width must not exceed maxWidth (for maxWidth > 0)
			expectedMax := tt.maxWidth
			if expectedMax < 0 {
				expectedMax = 0
			}
			gotWidth := runewidth.StringWidth(got)
			if gotWidth > expectedMax {
				t.Errorf("TruncateString width = %d, expected <= %d for %q", gotWidth, expectedMax, got)
			}
		})
	}
}

func TestTruncateLeft(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		prefix   string
	}{
		{name: "empty string", input: "", maxWidth: 10, prefix: "..."},
		{name: "negative width", input: "/a/b/c", maxWidth: -2, prefix: "..."},
		{name: "zero width", input: "/a/b/c", maxWidth: 0, prefix: "..."},
		{name: "narrow 1", input: "/Users/dev/repo", maxWidth: 1, prefix: "..."},
		{name: "narrow 2", input: "/Users/dev/repo", maxWidth: 2, prefix: "..."},
		{name: "narrow 3", input: "/Users/dev/repo", maxWidth: 3, prefix: "..."},
		{name: "path with cjk", input: "/Users/张三/Projects/repo", maxWidth: 15, prefix: "..."},
		{name: "path with emoji", input: "/Users/user/🚀/agentpulse-cli", maxWidth: 20, prefix: "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := termutil.TruncateLeft(tt.input, tt.maxWidth, tt.prefix)

			// 1. Must be valid UTF-8
			if !utf8.ValidString(got) {
				t.Fatalf("TruncateLeft produced invalid UTF-8: %q", got)
			}

			// 2. Width must not exceed maxWidth
			expectedMax := tt.maxWidth
			if expectedMax < 0 {
				expectedMax = 0
			}
			gotWidth := runewidth.StringWidth(got)
			if gotWidth > expectedMax {
				t.Errorf("TruncateLeft width = %d, expected <= %d for %q", gotWidth, expectedMax, got)
			}
		})
	}
}

func TestPadRight(t *testing.T) {
	p1 := termutil.PadRight("优化数据库", 20)
	p2 := termutil.PadRight("Agent任务", 20)
	p3 := termutil.PadRight("Fast", 20)

	if runewidth.StringWidth(p1) != 20 {
		t.Errorf("expected visual width 20, got %d for %q", runewidth.StringWidth(p1), p1)
	}
	if runewidth.StringWidth(p2) != 20 {
		t.Errorf("expected visual width 20, got %d for %q", runewidth.StringWidth(p2), p2)
	}
	if runewidth.StringWidth(p3) != 20 {
		t.Errorf("expected visual width 20, got %d for %q", runewidth.StringWidth(p3), p3)
	}
}
