//go:build windows

package safety

import (
	"fmt"
	"os"
	"path/filepath"
)

// createOwnedFixtureRoot creates a new directory and records the handle identity
// from that creation. A pre-existing path is not accepted.
func createOwnedFixtureRoot() (string, CreationRecord, error) {
	parent := os.TempDir()
	name, err := randomHex(8)
	if err != nil {
		return "", CreationRecord{}, err
	}
	dir := filepath.Join(parent, "agentpulse-owned-"+name)
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", CreationRecord{}, err
	}
	kpath, id, err := statNoFollow(dir)
	if err != nil || id.reparse || !id.dir || (id.volume == 0 && id.index == 0) {
		_ = os.Remove(dir)
		return "", CreationRecord{}, fmt.Errorf("%w: new fixture directory has no file identity", ErrSentinelUnusable)
	}
	nonce, err := randomHex(16)
	if err != nil {
		return "", CreationRecord{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentpulse-owned"), []byte(nonce), 0600); err != nil {
		return "", CreationRecord{}, err
	}
	rec, err := mintCreation(CreationRecord{
		Path: kpath, CanonicalRoot: canonicalWinPath(kpath),
		Volume: id.volume, FileIndex: id.index, Nonce: nonce,
	})
	if err != nil {
		return "", CreationRecord{}, err
	}
	return dir, rec, nil
}
