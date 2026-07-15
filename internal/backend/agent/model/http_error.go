// http_error.go 负责把非 2xx HTTP 响应整理成带响应体摘要的错误。
package modeladapter

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// maxErrorBodyBytes 表示错误响应体最多读取的字节数。
	maxErrorBodyBytes = 8192
)

// HTTPStatusError 保留 provider 的 HTTP 状态码，供上层决定是否恢复。
type HTTPStatusError struct {
	StatusCode int
	Message    string
}

func (err *HTTPStatusError) Error() string {
	if err == nil {
		return "provider HTTP error"
	}
	return err.Message
}

// buildHTTPStatusError 读取响应体摘要并生成带状态码的错误。
func buildHTTPStatusError(prefix string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("%s response is nil", strings.TrimSpace(prefix))
	}

	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		message := ""
		if retrySummary := ProviderRetryAttemptSummary(resp); retrySummary != "" {
			message = fmt.Sprintf("%s status=%d %s body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, err)
		} else {
			message = fmt.Sprintf("%s status=%d body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, err)
		}
		return &HTTPStatusError{StatusCode: resp.StatusCode, Message: message}
	}
	retrySummary := ProviderRetryAttemptSummary(resp)
	bodyText := strings.TrimSpace(string(limitedBody))
	message := ""
	switch {
	case bodyText == "" && retrySummary != "":
		message = fmt.Sprintf("%s status=%d %s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary)
	case bodyText == "":
		message = fmt.Sprintf("%s status=%d", strings.TrimSpace(prefix), resp.StatusCode)
	case retrySummary != "":
		message = fmt.Sprintf("%s status=%d %s body=%s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, bodyText)
	default:
		message = fmt.Sprintf("%s status=%d body=%s", strings.TrimSpace(prefix), resp.StatusCode, bodyText)
	}
	return &HTTPStatusError{StatusCode: resp.StatusCode, Message: message}
}
