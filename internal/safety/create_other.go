//go:build !linux

package safety

import "fmt"

func createSyntheticSentinel(base string) (string, error) {
	return "", fmt.Errorf("%w: sentinel creation is BLOCKED on this OS", ErrBackendUnavailable)
}

func createRunRoot(opts createOpts) (RunRecord, error) {
	return RunRecord{}, fmt.Errorf("%w: run-root identity creation is BLOCKED on this OS", ErrBackendUnavailable)
}

type createOpts struct {
	BaseDir     string
	RegistryDir string
	RealHome    string
	Sentinel    string
	ExeHash     string
}
