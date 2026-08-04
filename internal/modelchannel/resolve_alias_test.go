package modelchannel

import "testing"

func TestFastIsNotMetaModelAlias(t *testing.T) {
	if IsMetaModelAlias("fast") {
		t.Fatal("fast must not be treated as an implicit model alias")
	}
	if !IsMetaModelAlias("default") || !IsMetaModelAlias("auto") {
		t.Fatal("default and auto must remain compatibility aliases")
	}
}
