package updater

import "testing"

func TestReleaseVersionIsNewerThanV0059(t *testing.T) {
	if compareVersions("0.0.61", "0.0.59") <= 0 {
		t.Fatal("0.0.61 must be newer than 0.0.59")
	}
}
