package safety

import (
	"path/filepath"
	"strings"
)

// Danger flags that must not be injected unless this call opts in.
// Their presence on argv is itself an opt-in and still requires isolation.
var dangerFlags = []string{
	"--dangerously-skip-permissions",
	"--dangerously-bypass-approvals-and-sandbox",
	"--dangerously-bypass-approvals",
}

// Decide is the launch policy for one argv.
// Explicit opt-in may add agy's skip-permissions flag. A false opt-in never does.
// The returned argv is what a confined launch may exec; callers still have to
// pass Prepare, which drops it if isolation does not verify.
func Decide(argv []string, explicitDanger bool) (out []string, danger bool) {
	out = append([]string{}, argv...)
	danger = explicitDanger || containsDangerFlag(out)
	if explicitDanger && len(out) > 0 && isAgy(out[0]) && !containsDangerFlag(out) {
		out = append([]string{out[0], "--dangerously-skip-permissions"}, out[1:]...)
	}
	return out, danger
}

// PrepareLaunchArgv is Decide under the name used by callers and tests.
func PrepareLaunchArgv(argv []string, explicitDanger bool) ([]string, bool) {
	return Decide(argv, explicitDanger)
}

func containsDangerFlag(argv []string) bool {
	for _, a := range argv {
		flag := a
		if i := strings.IndexByte(a, '='); i > 0 {
			flag = a[:i]
		}
		for _, d := range dangerFlags {
			if flag == d {
				return true
			}
		}
	}
	return false
}

func isAgy(bin string) bool {
	base := strings.ToLower(filepath.Base(bin))
	return base == "agy" || base == "agy.exe"
}
