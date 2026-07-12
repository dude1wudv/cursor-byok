package bridge

import "github.com/pkg/browser"

const footerAuthorHomeURL = "https://github.com/dude1wudv/cursor-byok"

var footerAuthorInfo = FooterAuthorInfo{
	ButtonText:           "作者 dude1wudv",
	DialogTitle:          "项目与作者",
	DialogContent:        "当前维护者：dude1wudv\n项目仓库：https://github.com/dude1wudv/cursor-byok\n\n本软件永久免费。如果你通过付费渠道获得，请谨慎核实来源。",
	DialogDetailsTitle:   "详细信息",
	DialogDetailsContent: "本项目基于 leookun 的 cursor-byok 项目继续维护。\n原作者：leookun\n上游仓库：https://github.com/leookun/cursor-byok\n感谢原作者的设计与开源贡献。",
	DialogConfirmText:    "访问仓库",
	DialogCancelText:     "关闭",
}

// FooterAuthorInfo 定义首页底部作者入口的展示信息。
type FooterAuthorInfo struct {
	ButtonText           string `json:"buttonText"`
	DialogTitle          string `json:"dialogTitle"`
	DialogContent        string `json:"dialogContent"`
	DialogDetailsTitle   string `json:"dialogDetailsTitle,omitempty"`
	DialogDetailsContent string `json:"dialogDetailsContent,omitempty"`
	DialogConfirmText    string `json:"dialogConfirmText"`
	DialogCancelText     string `json:"dialogCancelText"`
}

// GetFooterAuthorInfo 返回首页底部作者入口的展示信息。
func (s *WindowService) GetFooterAuthorInfo() FooterAuthorInfo {
	return footerAuthorInfo
}

// OpenFooterAuthorHome 打开作者主页。
func (s *WindowService) OpenFooterAuthorHome() error {
	return browser.OpenURL(footerAuthorHomeURL)
}
