package identity

import (
	"fmt"
	"os"
	"time"
)

// LocalAdmin builds an Identity for in-process CLI callers. It carries no
// scopes and synthesises a "cli-<pid>-<unix-nanos>" RequestID so log
// records from CLI invocations are distinguishable from HTTP requests.
func LocalAdmin(p Principal) Identity {
	return Identity{
		Principal: p,
		RequestID: fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}
}
