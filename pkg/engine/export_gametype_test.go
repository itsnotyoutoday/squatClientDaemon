package engine

// SniffGameTypeForTest exposes sniffGameType to the external tests.
func SniffGameTypeForTest(text string) string { return sniffGameType(text) }
