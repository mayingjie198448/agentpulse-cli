package safety

import (
	"os"
	"path/filepath"
	"strings"
)

// cleanChildEnv is an env -i style environment for a confined child.
// PATH is kept so the requested binary can run; it is not a write boundary.
// Credential-agent sockets and secret variables are not copied.
// XDG_RUNTIME_DIR is pointed at the run tmp so it does not name the host bus socket.
func cleanChildEnv(parent []string, extra map[string]string, home, xdgConfig, xdgData, xdgCache, xdgState, tmp string) []string {
	keep := map[string]string{}
	for _, kv := range parent {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || deniedEnvKey(key) {
			continue
		}
		switch key {
		case "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ", "USER", "LOGNAME", "SHELL":
			keep[key] = val
		}
	}
	for k, v := range extra {
		if deniedEnvKey(k) {
			continue
		}
		keep[k] = v
	}
	keep["HOME"] = home
	keep["XDG_CONFIG_HOME"] = xdgConfig
	keep["XDG_DATA_HOME"] = xdgData
	keep["XDG_CACHE_HOME"] = xdgCache
	keep["XDG_STATE_HOME"] = xdgState
	keep["XDG_RUNTIME_DIR"] = tmp
	keep["TMPDIR"] = tmp
	keep["TMP"] = tmp
	keep["TEMP"] = tmp
	if keep["PATH"] == "" {
		keep["PATH"] = "/usr/bin:/bin"
	}
	out := make([]string, 0, len(keep))
	for k, v := range keep {
		out = append(out, k+"="+v)
	}
	return out
}

func deniedEnvKey(key string) bool {
	u := strings.ToUpper(key)
	switch u {
	case "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO", "GNOME_KEYRING_CONTROL",
		"DBUS_SESSION_BUS_ADDRESS", "KRB5CCNAME", "GOOGLE_APPLICATION_CREDENTIALS",
		"XDG_RUNTIME_DIR":
		return true
	}
	if strings.HasPrefix(u, "AWS_") || strings.HasPrefix(u, "AZURE_") ||
		strings.HasPrefix(u, "AGENTPULSE_MQTT_") || strings.HasPrefix(u, "AGENTPULSE_BROKER_") {
		return true
	}
	if strings.Contains(u, "PASSWORD") || strings.Contains(u, "SECRET") ||
		strings.Contains(u, "TOKEN") || strings.Contains(u, "CREDENTIAL") {
		return true
	}
	return false
}

// protectedHomes is the process home captured before tests change HOME,
// plus any explicit home the caller is treating as real.
func protectedHomes(extra string) []string {
	var homes []string
	if h := RealHome(); h != "" {
		homes = append(homes, h)
	}
	if extra != "" {
		if abs, err := filepath.Abs(extra); err == nil {
			extra = abs
		}
		homes = append(homes, filepath.Clean(extra))
	}
	return homes
}

var realHomeSnapshot string

func init() {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			realHomeSnapshot = filepath.Clean(abs)
		}
	}
}

// RealHome is the home directory observed when the process started.
func RealHome() string { return realHomeSnapshot }
