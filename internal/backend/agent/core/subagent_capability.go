package runtimecore

import (
	"fmt"
	"strings"
)

// SubagentCapability is the normalized authorization for a Task child.
type SubagentCapability struct {
	Type     string
	Readonly bool
}

// ResolveSubagentCapability validates the supported Task type and access pair.
func ResolveSubagentCapability(subagentType string, readonly bool) (SubagentCapability, error) {
	capability := SubagentCapability{
		Type:     strings.TrimSpace(subagentType),
		Readonly: readonly,
	}
	switch capability.Type {
	case "explore":
		if !capability.Readonly {
			return SubagentCapability{}, fmt.Errorf("subagent type %q must be readonly", capability.Type)
		}
	case "generalPurpose":
	default:
		return SubagentCapability{}, fmt.Errorf("unsupported subagent type %q", capability.Type)
	}
	return capability, nil
}
