//go:build !darwin

package safety

func getDarwinFileIdentity(path string) (DarwinTargetIdentity, error) {
	return DarwinTargetIdentity{}, nil
}

func createDarwinExclusiveSentinel(sentinelPath string, content []byte) (string, uint64, uint64, error) {
	return sentinelPath, 0, 0, nil
}
