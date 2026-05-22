package brokerexec

import (
	"fmt"
	"strings"
	"unicode"

	"go.kenn.io/fotobank/internal/broker"
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
	prefix := "brokerexec " + op
	if tail != "" {
		prefix += ": " + tail
	}
	switch exitCode {
	case 65:
		return fmt.Errorf("%s: %w", prefix, broker.ErrBrokerPermanent)
	case 75:
		return fmt.Errorf("%s: %w", prefix, broker.ErrBrokerTransient)
	default:
		if runErr != nil {
			return fmt.Errorf("%s: exit=%d run-err=%v: %w",
				prefix, exitCode, runErr, broker.ErrBrokerTransient)
		}
		return fmt.Errorf("%s: exit=%d: %w",
			prefix, exitCode, broker.ErrBrokerTransient)
	}
}

// tailForError returns at most 256 bytes from the END of b. Whitespace
// runes (tab, newline, carriage return, NBSP, …) are folded to a single
// space so word boundaries survive; other control bytes (< 0x20 or 0x7f)
// are dropped; runs of whitespace are collapsed to one space. Used to
// fold stderr into the wrapped error message that ends up in
// scopes.broker_last_error.
func tailForError(b []byte) string {
	if len(b) > 256 {
		b = b[len(b)-256:]
	}
	s := strings.Map(func(r rune) rune {
		// IsSpace catches \n / \r / \t plus Unicode whitespace (NBSP,
		// etc.) so newlines do not get dropped as control bytes —
		// that would silently fuse adjacent words in stderr tails.
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
