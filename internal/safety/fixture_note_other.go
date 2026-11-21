//go:build !linux && !windows

package safety

func noNewPrivsValue() string { return "0" }

func confinementNote() string { return "" }
