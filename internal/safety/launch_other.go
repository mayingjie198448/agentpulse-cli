//go:build !windows

package safety

func configureConfinedLaunch(p *Prepared) error { return nil }
