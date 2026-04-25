package backup

import "errors"

// ErrServerHoldsLock is returned by Restore when another process is
// currently holding the lifetime flock on the DB. Callers (CLI) check
// via errors.Is to distinguish "operator forgot to stop the server"
// from other failure modes.
var ErrServerHoldsLock = errors.New("backup: another fotobank process is using the DB")
