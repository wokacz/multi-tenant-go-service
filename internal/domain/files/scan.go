package files

import "context"

type nopScanner struct{}

// NopScanner records that no engine ran. Development and tests use it; a
// production deployment that wants a scan sets FILES_SCAN_MODE.
func NopScanner() Scanner {
	return nopScanner{}
}

func (nopScanner) Scan(context.Context, []byte) (ScanOutcome, error) {
	return ScanOutcome{Status: ScanSkipped}, nil
}
