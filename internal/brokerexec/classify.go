package brokerexec

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/wesm/fotobank/internal/broker"
)

// classifyExit maps a (exitCode, runErr) pair plus a normalized
// stderr tail into a wrapped broker sentinel:
//
//   - 0 should never reach here; callers gate success out before
//     calling classifyExit. We treat it as transient defensively.
//   - 65 -> ErrBrokerPermanent (sysexits EX_DATAERR; broker
//     rejected the scope).
//   - 75 -> ErrBrokerTransient (sysexits EX_TEMPFAIL).
//   - Any other exit code, including -1 (process never started),
//     is treated as transient. The share worker's MaxBrokerAttempts
//     cap (10) provides a backstop against runaway retries on a
//     persistently broken CLI.
//
// runErr is included verbatim for diagnostic value when the process
// never started (e.g. "executable file not found in $PATH").
func classifyExit(op string, exitCode int, tail string, runErr error) error {
	switch exitCode {
	case 65:
		return fmt.Errorf("brokerexec %s: %s: %w",
			op, tail, broker.ErrBrokerPermanent)
	case 75:
		return fmt.Errorf("brokerexec %s: %s: %w",
			op, tail, broker.ErrBrokerTransient)
	default:
		if runErr != nil {
			return fmt.Errorf("brokerexec %s: %s: exit=%d run-err=%v: %w",
				op, tail, exitCode, runErr, broker.ErrBrokerTransient)
		}
		return fmt.Errorf("brokerexec %s: %s: exit=%d: %w",
			op, tail, exitCode, broker.ErrBrokerTransient)
	}
}

// tailForError returns at most 256 bytes from the END of b, with
// control bytes stripped, tabs replaced by spaces, and whitespace
// runs collapsed. Used to fold stderr into the wrapped error
// message that ends up in scopes.broker_last_error.
func tailForError(b []byte) string {
	if len(b) > 256 {
		b = b[len(b)-256:]
	}
	s := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, string(b))
	return strings.Join(strings.Fields(s), " ")
}
