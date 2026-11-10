//go:build !darwin

package safety

func getDarwinFileIdentity(path string) (DarwinTargetIdentity, error) {
	return DarwinTargetIdentity{}, nil
}
