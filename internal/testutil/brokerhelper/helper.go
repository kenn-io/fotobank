// Package brokerhelper supplies a re-execable broker-CLI fake for
// tests. Test binaries opt in by checking IsHelper() in TestMain;
// when set, the binary calls Run() (which exits) instead of running
// its own test suite. Each test binary that uses this becomes its
// own broker CLI when invoked with BROKEREXEC_TEST_HELPER=1.
//
// Behaviour is driven entirely by env vars so callers don't have to
// recompile to vary the simulated broker:
//
//	BROKEREXEC_TEST_HELPER=1            // opt in
//	BROKEREXEC_TEST_EXPECT_OPERATION    // assert payload.operation; exit 65 on mismatch
//	BROKEREXEC_TEST_ECHO_ENV            // os.Getenv($value) -> stderr
//	BROKEREXEC_TEST_STDERR              // literal -> stderr
//	BROKEREXEC_TEST_SLEEP               // time.Duration sleep before exit
//	BROKEREXEC_TEST_EXIT                // exit code (default 0)
//	BROKEREXEC_TEST_RECORD_FILE         // append payload.operation + "\n" to file
package brokerhelper

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// EnvVar is the env-var name that switches a test binary into helper mode.
const EnvVar = "BROKEREXEC_TEST_HELPER"

// IsHelper reports whether the current process should run as the helper.
func IsHelper() bool { return os.Getenv(EnvVar) == "1" }

// Run reads stdin, optionally validates the operation discriminator,
// optionally records the invocation to a file (so callers can prove
// the helper ran, not the NoopBroker), optionally echoes a configured
// env var to stderr, optionally writes a literal stderr message,
// optionally sleeps, and exits with the configured exit code. Never
// returns.
func Run() {
	payload, _ := io.ReadAll(os.Stdin)
	var got struct {
		Operation string `json:"operation"`
	}
	_ = json.Unmarshal(payload, &got)
	if want := os.Getenv("BROKEREXEC_TEST_EXPECT_OPERATION"); want != "" {
		if got.Operation != want {
			fmt.Fprintf(os.Stderr, "operation mismatch: got=%q want=%q",
				got.Operation, want)
			os.Exit(65)
		}
	}
	if path := os.Getenv("BROKEREXEC_TEST_RECORD_FILE"); path != "" {
		// O_APPEND writes < PIPE_BUF are atomic on POSIX, so concurrent
		// broker children appending one short line each cannot interleave.
		if f, err := os.OpenFile(path,
			os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600); err == nil {
			fmt.Fprintln(f, got.Operation)
			_ = f.Close()
		}
	}
	if k := os.Getenv("BROKEREXEC_TEST_ECHO_ENV"); k != "" {
		fmt.Fprint(os.Stderr, os.Getenv(k))
	}
	if msg := os.Getenv("BROKEREXEC_TEST_STDERR"); msg != "" {
		fmt.Fprint(os.Stderr, msg)
	}
	if d := os.Getenv("BROKEREXEC_TEST_SLEEP"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			time.Sleep(dur)
		}
	}
	code, _ := strconv.Atoi(os.Getenv("BROKEREXEC_TEST_EXIT"))
	os.Exit(code)
}
