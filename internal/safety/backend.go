package safety

// Backend is the platform write-isolation gate.
// Available must fail when the backend is missing or was not verified.
// A nil error is not permission to skip the fresh child probe.
type Backend interface {
	Name() string
	Available() error
}

type unavailableBackend struct{}

func (unavailableBackend) Name() string { return "unavailable" }

func (unavailableBackend) Available() error {
	return fmtUnavailable()
}

func fmtUnavailable() error {
	return errString(ErrBackendUnavailable, "backend forced unavailable")
}

func errString(err error, detail string) error {
	return &joinedError{err: err, detail: detail}
}

type joinedError struct {
	err    error
	detail string
}

func (e *joinedError) Error() string { return e.err.Error() + ": " + e.detail }
func (e *joinedError) Unwrap() error { return e.err }

// UnavailableBackend is a test double that fails closed.
func UnavailableBackend() Backend { return unavailableBackend{} }

// SelectBackend returns this OS backend. It does not apply the sandbox.
func SelectBackend() (Backend, error) {
	b := platformBackend()
	if err := b.Available(); err != nil {
		return b, err
	}
	return b, nil
}
