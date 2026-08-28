package testutil

import "sync/atomic"

const syntheticDocbankNodeFloor int64 = 1 << 62

var syntheticDocbankNodeSequence atomic.Int64

// NextSyntheticDocbankNodeID returns a process-unique positive node ID for
// tests that do not open a real Docbank vault. The high range keeps synthetic
// mappings separate from Docbank's ordinary low, sequential IDs.
func NextSyntheticDocbankNodeID() int64 {
	return syntheticDocbankNodeFloor + syntheticDocbankNodeSequence.Add(1)
}
