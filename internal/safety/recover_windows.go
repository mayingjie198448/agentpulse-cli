//go:build windows

package safety

import (
	"fmt"
	"os"
)

// recoverProfile continues from a manifest this run wrote. A still-present root
// is deleted only when its file identity matches and deleteOwned accepts the
// marker. A missing root deletes only the named profile.
func recoverProfile(dir string, m RunManifest) (CleanupOutcome, error) {
	rootMissing := m.Root == ""
	idMatches := false
	if m.Root != "" {
		_, id, serr := statNoFollow(m.Root)
		if serr == nil {
			idMatches = (m.RootDev != 0 || m.RootIno != 0) && id.volume == m.RootDev && id.index == m.RootIno && id.dir && !id.reparse
		} else if os.IsNotExist(serr) {
			rootMissing = true
		} else {
			return CleanupOutcome{Result: "BLOCKED", Reason: "cannot prove the package root is gone: " + serr.Error()}, fmt.Errorf("%w: cannot prove the package root is gone", ErrCleanupBlocked)
		}
	}
	plan, err := cleanupPlan(rootMissing, idMatches, m)
	if err != nil {
		return CleanupOutcome{Result: "BLOCKED", Reason: err.Error()}, err
	}
	if plan == "root_then_profile" {
		rec := RunRecord{
			RunID: m.RunID, Root: m.Root, Dev: m.RootDev, Ino: m.RootIno,
			Nonce: m.Nonce, ProfileName: m.ProfileName, RegistryDir: dir,
		}
		return deleteOwned(rec, nil)
	}
	if plan != "profile_only" {
		return CleanupOutcome{Result: "BLOCKED", Reason: "manifest is not an owned profile"}, fmt.Errorf("%w: manifest is not an owned profile", ErrCleanupBlocked)
	}
	if err := deleteAppProfile(m.ProfileName); err != nil {
		m.Phase = "delete_failed"
		_ = saveManifest(dir, m)
		return CleanupOutcome{Result: "BLOCKED", Reason: "profile delete failed: " + err.Error()}, fmt.Errorf("%w: profile delete failed: %v", ErrCleanupBlocked, err)
	}
	if err := os.Remove(manifestPath(dir, m.RunID)); err != nil && !os.IsNotExist(err) {
		return CleanupOutcome{Result: "BLOCKED", Reason: "manifest remove failed after profile delete: " + err.Error()}, fmt.Errorf("%w: manifest remove failed after profile delete", ErrCleanupBlocked)
	}
	return CleanupOutcome{Result: "deleted", Targets: []string{m.ProfileName}, Reason: "profile removed after owned root was already gone"}, nil
}
