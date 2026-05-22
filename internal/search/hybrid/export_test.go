package hybrid

// FlattenFilterForTest exposes the unexported flattenFilter helper to
// the hybrid_test package so cursor-hash tests can pin the contract
// of which Input fields contribute to the cursor hash without going
// through the full engine round-trip.
func FlattenFilterForTest(in Input) map[string]string {
	return flattenFilter(in)
}
