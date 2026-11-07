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
	runRoot     string
	workRoot    string
	runID       string
	rootDev     uint64
	rootIno     uint64
	profileHash string
	exeHash     string
	sentinel    string
	sentinelDev uint64
	sentinelIno uint64
	user        []string
}

func runReexec(args []string) {
	cfg, err := parseReexec(args)
	if err != nil {
		failVerify(3, "bad_helper_args")
	}
	if err := verifyAndProbe(cfg); err != nil {
		failVerify(3, err.Error())
	}
	if len(cfg.user) == 0 || strings.TrimSpace(cfg.user[0]) == "" {
		failVerify(3, "empty_user_command")
	}
	if err := closeExtraFDs(3); err != nil {
		failVerify(3, "fd_cleanup_failed")
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
		default:
			return cfg, fmt.Errorf("unknown")
		}
		i++
	}
	return cfg, fmt.Errorf("missing command separator")
}
