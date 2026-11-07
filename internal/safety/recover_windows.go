//go:build windows

package safety

// recoverProfile keeps the named profile. A manifest is evidence for the
// operator; it is not a cross-process ownership proof.
func recoverProfile(dir string, m RunManifest) (CleanupOutcome, error) {
	_ = dir
	return manifestRecovery(m)
}
