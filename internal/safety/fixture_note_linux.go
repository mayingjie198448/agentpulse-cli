//go:build linux

package safety

import "strings"
import "os"

func noNewPrivsValue() string {
	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "0"
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "NoNewPrivs:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "NoNewPrivs:"))
		}
	}
	return "0"
}

func confinementNote() string { return "" }
