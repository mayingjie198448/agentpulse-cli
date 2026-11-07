package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FixtureWorkerArg is the confined child used by safety fixture.
// It is not an agent and it does not receive real credentials.
const FixtureWorkerArg = "--agentpulse-safety-worker"

// MaybeFixtureWorker runs the synthetic fixture child and does not return.
func MaybeFixtureWorker(args []string) {
	if len(args) > 1 && args[1] == FixtureWorkerArg {
		os.Exit(FixtureWorker(args[2:]))
	}
}

// FixtureWorker writes a project file inside the working directory and, when
// asked, tries to modify an existing sentinel outside that root.
func FixtureWorker(args []string) int {
	exitCode := 0
	projectRel := filepath.Join("project", "result.txt")
	var escape string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return 2
		}
		switch args[i] {
		case "--project-out":
			i++
			projectRel = args[i]
		case "--escape":
			i++
			escape = args[i]
		case "--exit":
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return 2
			}
			exitCode = n
		default:
			return 2
		}
	}
	if err := os.MkdirAll(filepath.Dir(projectRel), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %v\n", err)
		return 51
	}
	if err := os.WriteFile(projectRel, []byte("wrote-ok\n"), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "write project %v\n", err)
		return 51
	}
	var status strings.Builder
	fmt.Fprintf(&status, "no_new_privs=%s\n", noNewPrivsValue())
	fmt.Fprintf(&status, "home=%s\n", os.Getenv("HOME"))
	if _, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
		status.WriteString("ssh_auth_sock_set=true\n")
	} else {
		status.WriteString("ssh_auth_sock_set=false\n")
	}
	if os.Getenv("MQTT_PASSWORD") != "" {
		status.WriteString("mqtt_password_set=true\n")
	} else {
		status.WriteString("mqtt_password_set=false\n")
	}
	status.WriteString(confinementNote())
	if err := os.WriteFile("status.txt", []byte(status.String()), 0600); err != nil {
		return 52
	}
	if escape != "" {
		writeErr := os.WriteFile(escape, []byte("hacked"), 0600)
		removeErr := os.Remove(escape)
		msg := fmt.Sprintf("write=%s\nremove=%s\n", errnoClass(writeErr), errnoClass(removeErr))
		if err := os.WriteFile("escape.txt", []byte(msg), 0600); err != nil {
			return 53
		}
	}
	return exitCode
}

func errnoClass(err error) string {
	if err == nil {
		return "nil"
	}
	if os.IsNotExist(err) {
		return "enoent"
	}
	if os.IsPermission(err) {
		return "eperm"
	}
	return "other"
}
