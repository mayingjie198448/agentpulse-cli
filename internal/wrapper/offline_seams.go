package wrapper

import "context"

// SetLocalIPDetectorForTest replaces the probe used by listener and wrapper
// machine records. Production leaves the default, which still dials UDP.
// Calling restore puts the default back.
func SetLocalIPDetectorForTest(fn func() string) (restore func()) {
	prev := localIPDetector
	if fn == nil {
		localIPDetector = DetectLocalIP
	} else {
		localIPDetector = fn
	}
	return func() { localIPDetector = prev }
}

// SetRepoGitLookupForTest replaces the git command inside InferRepoAndProject.
// Production leaves the default, which still executes git. The restore
// function puts that default back. A test replacement must not start git.
func SetRepoGitLookupForTest(fn func(context.Context, string, ...string) ([]byte, error)) (restore func()) {
	prev := repoGitLookup
	if fn == nil {
		repoGitLookup = defaultRepoGitLookup
	} else {
		repoGitLookup = fn
	}
	return func() { repoGitLookup = prev }
}
