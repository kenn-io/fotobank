package backup

import "errors"

// ErrServerHoldsLock is returned by Restore when another process is using the
// database under the shared lifetime-lock protocol. Callers check it with
// errors.Is to distinguish an in-use database from other failure modes.
var ErrServerHoldsLock = errors.New("backup: another fotobank process is using the DB")
