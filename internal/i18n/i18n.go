// Package i18n 提供 Go 端用户可见文案的国际化支持
// 仅用于托盘菜单、Tooltip 和原生 MessageBox,前端国际化由 web/i18n.js 独立维护
// 产品名和版本号继续读取 internal/version,本包不重复硬编码
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// 支持的语言常量
const (
	LangZhCN = "zh-CN"
	LangEnUS = "en-US"
)

// currentLang 保存当前语言,通过 atomic.Value 实现线程安全读写
var currentLang atomic.Value

func init() {
	// 默认中文,首次运行无配置时使用
	currentLang.Store(LangZhCN)
}

// messageTables 两套语言文案,key 为消息标识,value 为本地化文本
// 含产品名的文案使用 %s 占位符,由 Tf/FormatErrorf 传入 version.ProductName()
var messageTables = map[string]map[string]string{
	LangZhCN: zhCNMessages,
	LangEnUS: enUSMessages,
}

// zhCNMessages 中文文案表
// 收口: 移除 msgbox.title(改由调用方读取 version.ProductName())
// 含产品名的文案使用 %s 占位符,避免硬编码 DacatDHCP
var zhCNMessages = map[string]string{
	// 托盘状态菜单(右键菜单顶部只读项)
	"tray.dhcp_running": "DHCP：运行中",
	"tray.dhcp_stopped": "DHCP：已停止",
	"tray.exiting":      "正在退出",
	// 托盘右键菜单操作项(%s = 产品名)
	"tray.open_console": "打开控制台",
	"tray.exit":         "退出 %s",
	// Tooltip 状态后缀(拼在产品名+版本号后)
	"tray.tooltip_running": "DHCP 运行中",
	"tray.tooltip_stopped": "DHCP 已停止",
	"tray.tooltip_exiting": "正在退出",
	// 原生 MessageBox 正文(%s = 产品名)
	"msgbox.exit_confirm":           "DHCP 服务正在运行中，退出将停止 DHCP 服务。\n\n确定要退出吗？",
	"msgbox.admin_required":         "需要管理员权限才能运行 %s。\n\n请以管理员身份重新启动程序。",
	"msgbox.single_instance_failed": "单实例检测失败，程序即将退出。",
	"msgbox.init_failed":            "%s 初始化失败，程序即将退出。",
	"msgbox.http_start_failed":      "%s 管理服务启动失败，程序即将退出。",
	"msgbox.tray_create_failed":     "%s 托盘创建失败，程序即将退出。",
	"msgbox.tray_icon_failed":       "%s 托盘图标添加失败，程序即将退出。",
	"msgbox.tray_exit_forced":       "%s 托盘无法正常退出，将强制关闭。",
	// 技术错误详细信息标签
	"msgbox.details_label": "详细信息",
}

// enUSMessages 英文文案表
var enUSMessages = map[string]string{
	"tray.dhcp_running":             "DHCP: Running",
	"tray.dhcp_stopped":             "DHCP: Stopped",
	"tray.exiting":                  "Exiting",
	"tray.open_console":             "Open Console",
	"tray.exit":                     "Exit %s",
	"tray.tooltip_running":          "DHCP Running",
	"tray.tooltip_stopped":          "DHCP Stopped",
	"tray.tooltip_exiting":          "Exiting",
	"msgbox.exit_confirm":           "The DHCP service is running. Exiting will stop it.\n\nAre you sure you want to exit?",
	"msgbox.admin_required":         "Administrator privileges are required to run %s.\n\nPlease restart the program as an administrator.",
	"msgbox.single_instance_failed": "The single-instance check failed. The program will exit.",
	"msgbox.init_failed":            "%s initialization failed. The program will exit.",
	"msgbox.http_start_failed":      "%s management service failed to start. The program will exit.",
	"msgbox.tray_create_failed":     "%s tray creation failed. The program will exit.",
	"msgbox.tray_icon_failed":       "%s tray icon could not be added. The program will exit.",
	"msgbox.tray_exit_forced":       "%s tray could not exit normally and will be forcibly closed.",
	"msgbox.details_label":          "Details",
}

// Normalize 将任意输入规范化为支持的语言代码
// 接受 zh-CN/zh_CN/zh/en-US/en_US/en 等形式,无法识别时回退到 zh-CN
// 注意: 用于启动期读取配置的宽松解析;API 入口必须使用 ParseLanguage 严格校验
func Normalize(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh-cn", "zh_cn", "zh-hans", "zh", "chinese", "中文":
		return LangZhCN
	case "en-us", "en_us", "en", "english":
		return LangEnUS
	default:
		return LangZhCN
	}
}

// ParseLanguage 严格解析语言代码,仅接受 zh-CN 和 en-US
// 空值或无效值返回 ok=false,不得自动回退中文
// 用于启动期配置读取和 API 入口校验,确保只接受标准值
func ParseLanguage(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case LangZhCN:
		return LangZhCN, true
	case LangEnUS:
		return LangEnUS, true
	default:
		return "", false
	}
}

// SetLanguage 设置当前语言,使用 ParseLanguage 严格解析
// 仅接受 zh-CN/en-US,非法值(如 fr-FR、空值、zh、en 等别名)返回 false 且不修改当前语言
func SetLanguage(lang string) bool {
	parsed, ok := ParseLanguage(lang)
	if !ok {
		return false
	}
	currentLang.Store(parsed)
	return true
}

// GetLanguage 返回当前语言代码,始终为 zh-CN 或 en-US
func GetLanguage() string {
	v := currentLang.Load()
	if s, ok := v.(string); ok && (s == LangZhCN || s == LangEnUS) {
		return s
	}
	return LangZhCN
}

// SupportedLanguages 返回支持的语言列表,顺序固定为 zh-CN、en-US
func SupportedLanguages() []string {
	return []string{LangZhCN, LangEnUS}
}

// T 返回当前语言下 key 对应的文案
// key 不存在时先回退到中文,中文也不存在时返回 key 本身(便于排查遗漏)
func T(key string) string {
	lang := GetLanguage()
	if msg, ok := messageTables[lang][key]; ok && msg != "" {
		return msg
	}
	// 回退到中文
	if msg, ok := messageTables[LangZhCN][key]; ok && msg != "" {
		return msg
	}
	return key
}

// Tf 返回当前语言下 key 对应的文案,并使用 fmt.Sprintf 格式化参数
// 用于含 %s 占位符(产品名等)的文案,避免硬编码产品名
func Tf(key string, args ...interface{}) string {
	return fmt.Sprintf(T(key), args...)
}

// FormatError 拼接本地化主提示与技术错误详情
// detail 为空时只返回主提示,非空时附上本地化的"详细信息"标签
// 用于在 MessageBox 中向用户展示可读主提示,同时保留技术错误便于排查
// 主提示不含 %s 占位符时使用此函数;含占位符时使用 FormatErrorf
func FormatError(mainKey, detail string) string {
	main := T(mainKey)
	if detail == "" {
		return main
	}
	return main + "\n\n" + T("msgbox.details_label") + ": " + detail
}

// FormatErrorf 拼接含 %s 占位符的本地化主提示与技术错误详情
// args 用于格式化主提示(如产品名),detail 为技术错误详情
// 用于含产品名的 MessageBox 文案,避免硬编码产品名
func FormatErrorf(mainKey, detail string, args ...interface{}) string {
	main := Tf(mainKey, args...)
	if detail == "" {
		return main
	}
	return main + "\n\n" + T("msgbox.details_label") + ": " + detail
}
