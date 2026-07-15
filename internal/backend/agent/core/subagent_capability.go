package runtimecore

import (
	"fmt"
	"strings"
)

const SubagentTypeLongContextRead = "longContextRead"

// SubagentCapability is the normalized authorization for a Task child.
type SubagentCapability struct {
	Type     string
	Readonly bool
}

// ResolveTaskSubagentCapability applies the Task access default before validation.
func ResolveTaskSubagentCapability(subagentType string, readonly *bool) (SubagentCapability, error) {
	requestedReadonly := false
	if readonly != nil {
		requestedReadonly = *readonly
	}
	if strings.TrimSpace(subagentType) == "explore" || strings.TrimSpace(subagentType) == SubagentTypeLongContextRead {
		requestedReadonly = true
	}
	return ResolveSubagentCapability(subagentType, requestedReadonly)
}

// ResolveSubagentCapability validates the supported Task type and access pair.
func ResolveSubagentCapability(subagentType string, readonly bool) (SubagentCapability, error) {
	capability := SubagentCapability{
		Type:     strings.TrimSpace(subagentType),
		Readonly: readonly,
	}
	switch capability.Type {
	case "explore", SubagentTypeLongContextRead:
		if !capability.Readonly {
			return SubagentCapability{}, fmt.Errorf("subagent type %q must be readonly", capability.Type)
		}
	case "generalPurpose":
	default:
		return SubagentCapability{}, fmt.Errorf("unsupported subagent type %q", capability.Type)
	}
	return capability, nil
}
