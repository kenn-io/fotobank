// Package brokerexec implements broker.BrokerClient by shelling out
// to a configurable broker CLI. See the package godoc on Registrar
// (added in a later task) for the wire contract.
package brokerexec

import "io"

// prefixBuffer is a fixed-cap byte sink that retains the LAST cap
// bytes written to it. Used to bound stderr capture without
// allocating O(input) memory on a misbehaving CLI.
type prefixBuffer struct {
	cap int
	buf []byte
}

func newPrefixBuffer(cap int) *prefixBuffer {
	return &prefixBuffer{cap: cap}
}

// Write appends b, dropping any bytes that fall outside the cap-sized
// trailing window.
func (p *prefixBuffer) Write(b []byte) (int, error) {
	n := len(b)
	if n >= p.cap {
		// Input alone exceeds cap; keep only the tail.
		p.buf = append(p.buf[:0], b[n-p.cap:]...)
		return n, nil
	}
	p.buf = append(p.buf, b...)
	if len(p.buf) > p.cap {
		// Slide the window forward: copy the trailing cap bytes to
		// the head of buf and truncate.
		copy(p.buf, p.buf[len(p.buf)-p.cap:])
		p.buf = p.buf[:p.cap]
	}
	return n, nil
}

// Bytes returns the retained tail. The returned slice aliases the
// internal buffer; callers must not mutate it after subsequent Write
// calls.
func (p *prefixBuffer) Bytes() []byte {
	return p.buf
}

var _ io.Writer = (*prefixBuffer)(nil)
