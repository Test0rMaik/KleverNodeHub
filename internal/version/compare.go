package version

import (
	"fmt"
	"strings"
)

// Compare compares two release versions ("v" prefix optional): 1 if a is
// newer, -1 if older, 0 if equal. Fork releases share the upstream X.Y.Z, so
// the fork revision (-knh.N) breaks ties.
func Compare(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	for i := 0; i < 3; i++ {
		var aNum, bNum int
		if i < len(aParts) {
			_, _ = fmt.Sscanf(aParts[i], "%d", &aNum)
		}
		if i < len(bParts) {
			_, _ = fmt.Sscanf(bParts[i], "%d", &bNum)
		}
		if aNum > bNum {
			return 1
		}
		if aNum < bNum {
			return -1
		}
	}
	if ra, rb := forkRevision(a), forkRevision(b); ra != rb {
		if ra > rb {
			return 1
		}
		return -1
	}
	return 0
}

// forkRevision extracts N from a fork suffix like "0.3.87-knh.3" (0 if absent).
func forkRevision(v string) int {
	_, suffix, ok := strings.Cut(v, "-knh.")
	if !ok {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(suffix, "%d", &n)
	return n
}
