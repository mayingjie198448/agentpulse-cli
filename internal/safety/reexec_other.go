//go:build !linux && !darwin && !windows

package safety

import (
	"fmt"
	"os"
)

func verifyAndProbe(cfg reexecCfg) error {
	return fmt.Errorf("landlock_failed")
}

func closeExtraFDs(keep int) error {
	return fmt.Errorf("fd_cleanup_failed")
}

func failVerify(confirm int, reason string) {
	os.Exit(HelperExitVerify)
}

func confirmAndExec(cfg reexecCfg) error {
	return fmt.Errorf("BLOCKED")
}
