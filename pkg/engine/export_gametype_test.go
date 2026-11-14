package engine

// SniffGameTypeForTest exposes sniffGameType to the external tests.
func SniffGameTypeForTest(text string) string { return sniffGameType(text) }

// LoginLineForTest exposes loginLine to the external tests.
func LoginLineForTest(w World, password string) string { return loginLine(w, password) }
