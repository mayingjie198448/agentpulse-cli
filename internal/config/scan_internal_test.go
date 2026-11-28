package config

import (
	"path/filepath"
	"testing"
)

func TestBuildSearchDirs_OverridePATHSkipsSystemDirs(t *testing.T) {
	overrideDir := filepath.Join(t.TempDir(), "custom-path")
	dirs := buildSearchDirs(overrideDir, "", nil, false)
	if len(dirs) != 1 || dirs[0] != filepath.Clean(overrideDir) {
		t.Fatalf("expected only [%q] when includeSystemDirs=false, got %v", filepath.Clean(overrideDir), dirs)
	}
	for _, sysDir := range []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/bin", "/bin"} {
		for _, d := range dirs {
			if d == sysDir {
				t.Fatalf("unexpected system dir %q in OverridePATH search dirs: %v", sysDir, dirs)
			}
		}
	}
}
