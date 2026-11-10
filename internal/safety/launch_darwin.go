//go:build darwin

package safety

import (
	"fmt"
	"os"
)

// configureConfinedLaunch wraps the command under /usr/bin/sandbox-exec using
// the generated seatbelt profile for the run root.
func configureConfinedLaunch(p *Prepared) error {
	if p == nil || p.rec == nil || p.Cmd == nil {
		return fmt.Errorf("%w: invalid prepared state", ErrRefused)
	}
	st, err := os.Stat(sandboxExecPath)
	if err != nil {
		return fmt.Errorf("%w: /usr/bin/sandbox-exec unavailable: %v", ErrBackendUnavailable, err)
	}
	if st.Mode()&0111 == 0 {
		return fmt.Errorf("%w: /usr/bin/sandbox-exec is not executable", ErrBackendUnavailable)
	}
	profile, err := DarwinSeatbeltProfile(p.rec.Root)
	if err != nil {
		return fmt.Errorf("%w: seatbelt profile generation: %v", ErrRefused, err)
	}

	selfExe := p.Cmd.Path
	isolateArgs := p.Cmd.Args[1:]
	argv, err := buildDarwinSandboxExecArgv(sandboxExecPath, profile, selfExe, isolateArgs)
	if err != nil {
		return err
	}

	p.Cmd.Path = sandboxExecPath
	p.Cmd.Args = argv
	return nil
}
