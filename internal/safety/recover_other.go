//go:build !windows

package safety

func recoverProfile(dir string, m RunManifest) (CleanupOutcome, error) {
	_ = dir
	return manifestRecovery(m)
}
