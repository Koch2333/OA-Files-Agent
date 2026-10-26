package archive

// NFC returns s composed to Unicode NFC (same small composer the archive naming uses), so other packages
// (the file manager) compare names exactly like the archive planner does.
func NFC(s string) string { return nfc(s) }
