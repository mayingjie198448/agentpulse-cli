package safety

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MaybeHandleInternal runs the isolate helper when this process was re-exec'd
// for a confined launch. It does not return in that case.
func MaybeHandleInternal(args []string) {
	if len(args) < 2 || args[1] != ReexecArg {
		return
	}
	runReexec(args[2:])
	os.Exit(HelperExitVerify)
}

type reexecCfg struct {
	runRoot       string
	workRoot      string
	runID         string
	rootDev       uint64
	rootIno       uint64
	profileHash   string
	exeHash       string
	sentinel      string
	sentinelDev   uint64
	sentinelIno   uint64
	profileName   string
	packageSID    string
	capCount      int
	confirmHandle uint64
	user          []string
}

func runReexec(args []string) {
	cfg, err := parseReexec(args)
	confirm := 3
	if err == nil && cfg.confirmHandle != 0 {
		confirm = int(cfg.confirmHandle)
	}
	if err != nil {
		failVerify(confirm, "bad_helper_args")
	}
	if err := verifyAndProbe(cfg); err != nil {
		failVerify(confirm, err.Error())
	}
	if len(cfg.user) == 0 || strings.TrimSpace(cfg.user[0]) == "" {
		failVerify(confirm, "empty_user_command")
	}
	if err := closeExtraFDs(confirm); err != nil {
		failVerify(confirm, "fd_cleanup_failed")
	}
	if err := confirmAndExec(cfg); err != nil {
		os.Exit(127)
	}
}

func parseReexec(args []string) (reexecCfg, error) {
	var cfg reexecCfg
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			cfg.user = append([]string{}, args[i+1:]...)
			return cfg, nil
		}
		if i+1 >= len(args) {
			return cfg, fmt.Errorf("truncated")
		}
		val := args[i+1]
		switch args[i] {
		case "--run-root":
			cfg.runRoot = val
		case "--work-root":
			cfg.workRoot = val
		case "--run-id":
			cfg.runID = val
		case "--root-dev":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return cfg, err
			}
			cfg.rootDev = n
		case "--root-ino":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return cfg, err
			}
			cfg.rootIno = n
		case "--profile-hash":
			cfg.profileHash = val
		case "--exe-hash":
			cfg.exeHash = val
		case "--sentinel":
			cfg.sentinel = val
		case "--sentinel-dev":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return cfg, err
			}
			cfg.sentinelDev = n
		case "--sentinel-ino":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return cfg, err
			}
			cfg.sentinelIno = n
		case "--profile-name":
			cfg.profileName = val
		case "--package-sid":
			cfg.packageSID = val
		case "--cap-count":
			n, err := strconv.Atoi(val)
			if err != nil {
				return cfg, err
			}
			cfg.capCount = n
		case "--confirm-handle":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return cfg, err
			}
			cfg.confirmHandle = n
		default:
			return cfg, fmt.Errorf("unknown")
		}
		i++
	}
	return cfg, fmt.Errorf("missing command separator")
}
