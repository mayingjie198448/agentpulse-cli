//go:build darwin

package safety

func noNewPrivsValue() string { return "0" }

func confinementNote() string { return "sandbox_exec=1\n" }
