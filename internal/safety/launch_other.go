//go:build !windows && !darwin

package safety

func configureConfinedLaunch(p *Prepared) error { return nil }
