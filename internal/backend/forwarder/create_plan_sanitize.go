package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimecore "cursor/internal/backend/agent/core"
)

func (service *Service) sanitizeCreatePlanInvocationForCurrentPlan(stream *ActiveStream, invocation runtimecore.ToolInvocation) (runtimecore.ToolInvocation, error) {
	if strings.TrimSpace(invocation.ToolName) != "CreatePlan" {
		return invocation, nil
	}
	args, err := runtimecore.DecodeCreatePlanArgsJSON(invocation.ArgsJSON)
	if err != nil {
		// Claude 偶发 status 非法值 / todo 结构偏差：先容错清洗再派发，卡片不再整体失败。
		lenientArgs, lenientErr := runtimecore.DecodeCreatePlanArgsJSONLenient(invocation.ArgsJSON)
		if lenientErr != nil {
			return invocation, newRecoverableToolInvocationError(fmt.Errorf("decode CreatePlan args failed: %w", err))
		}
		cleaned, marshalErr := json.Marshal(lenientArgs)
		if marshalErr != nil {
			return invocation, newRecoverableToolInvocationError(fmt.Errorf("encode cleaned CreatePlan args failed: %w", marshalErr))
		}
		args = lenientArgs
		invocation.ArgsJSON = cleaned
	}
	if strings.TrimSpace(args.GetName()) == "" {
		return invocation, nil
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return invocation, err
	}
	if !hasCurrentPlan(conversation) {
		return invocation, nil
	}
	args.Name = ""
	sanitized, err := json.Marshal(args)
	if err != nil {
		return invocation, newRecoverableToolInvocationError(fmt.Errorf("encode sanitized CreatePlan args failed: %w", err))
	}
	invocation.ArgsJSON = sanitized
	return invocation, nil
}
