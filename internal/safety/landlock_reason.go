package safety

import "errors"

// landlockNoNewPrivsENOTSUPReason is the stable helper-pipe token when
// applyLandlock cannot set PR_SET_NO_NEW_PRIVS because AllThreadsSyscall
// returned ENOTSUP. The user command is not executed.
const landlockNoNewPrivsENOTSUPReason = "no_new_privs_enotsup"

// errNoNewPrivsAllThreadsUnsupported is that apply failure.
// It is not permission to start the child without landlock.
var errNoNewPrivsAllThreadsUnsupported = errors.New(landlockNoNewPrivsENOTSUPReason)
