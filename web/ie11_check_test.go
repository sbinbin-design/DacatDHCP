package web

import (
	"io/fs"
	"strings"
	"testing"
)

// readWebAsset 从 embed.FS 读取嵌入式 Web 资源,断言读取成功
func readWebAsset(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(Assets, name)
	if err != nil {
		t.Fatalf("读取嵌入式资源 %s 失败: %v", name, err)
	}
	return string(data)
}

// scriptIndex 在 index.html 中查找 <script src="name"> 的位置,未找到时通过 t.Errorf 记录并返回 -1
func scriptIndex(t *testing.T, html, name string) int {
	t.Helper()
	needle := `src="` + name + `"`
	idx := strings.Index(html, needle)
	if idx < 0 {
		t.Errorf("index.html 中未找到 <script %s>", needle)
	}
	return idx
}

// TestIE11Check_ScriptOrder 验证 ie11-check.js 在 index.html 中位于 theme.js/i18n.js/app.js 之前
// IE 检测必须先执行,检测到 IE 时替换文档,后续脚本不再加载
func TestIE11Check_ScriptOrder(t *testing.T) {
	html := readWebAsset(t, "index.html")

	ie11 := scriptIndex(t, html, "ie11-check.js")
	theme := scriptIndex(t, html, "theme.js")
	i18n := scriptIndex(t, html, "i18n.js")
	app := scriptIndex(t, html, "app.js")
	if ie11 < 0 || theme < 0 || i18n < 0 || app < 0 {
		return
	}

	if ie11 >= theme {
		t.Errorf("ie11-check.js 必须在 theme.js 之前加载 (ie11=%d, theme=%d)", ie11, theme)
	}
	if ie11 >= i18n {
		t.Errorf("ie11-check.js 必须在 i18n.js 之前加载 (ie11=%d, i18n=%d)", ie11, i18n)
	}
	if ie11 >= app {
		t.Errorf("ie11-check.js 必须在 app.js 之前加载 (ie11=%d, app=%d)", ie11, app)
	}
}

// TestIE11Check_DocumentModeDetection 验证包含 document.documentMode 检测
// documentMode 是 IE8~IE11 专有属性,现代浏览器为 undefined
func TestIE11Check_DocumentModeDetection(t *testing.T) {
	js := readWebAsset(t, "ie11-check.js")
	if !strings.Contains(js, "document.documentMode") {
		t.Fatal("ie11-check.js 必须包含 document.documentMode 检测")
	}
	// 必须使用 typeof 安全检测,避免在非 IE 浏览器中访问未定义属性报错
	if !strings.Contains(js, "typeof document.documentMode") {
		t.Error("ie11-check.js 必须使用 typeof document.documentMode 安全检测")
	}
}

// TestIE11Check_DocumentBlocking 验证包含 document.open/write/close 阻断逻辑
// IE 检测命中后通过 document.open/write/close 替换整个文档,丢弃原页面及后续脚本
func TestIE11Check_DocumentBlocking(t *testing.T) {
	js := readWebAsset(t, "ie11-check.js")
	if !strings.Contains(js, "document.open()") {
		t.Error("ie11-check.js 必须包含 document.open() 阻断逻辑")
	}
	if !strings.Contains(js, "document.write(") {
		t.Error("ie11-check.js 必须包含 document.write() 阻断逻辑")
	}
	if !strings.Contains(js, "document.close()") {
		t.Error("ie11-check.js 必须包含 document.close() 阻断逻辑")
	}
}

// TestIE11Check_PromptText 验证中英文提示及 Microsoft Edge/Google Chrome 文字完整
// IE 环境只能看到升级浏览器提示页面,不得继续显示或初始化管理界面
func TestIE11Check_PromptText(t *testing.T) {
	js := readWebAsset(t, "ie11-check.js")

	// 中文提示文案(浏览器语言为 zh 开头时显示)
	zhTexts := []string{
		"当前浏览器不受支持",
		"Microsoft Edge",
		"Google Chrome",
		"Internet Explorer 11 已不再受支持",
	}
	for _, s := range zhTexts {
		if !strings.Contains(js, s) {
			t.Errorf("ie11-check.js 缺少中文提示文案: %q", s)
		}
	}

	// 英文提示文案(浏览器语言非 zh 时显示)
	enTexts := []string{
		"Unsupported Browser",
		"Microsoft Edge",
		"Google Chrome",
		"Internet Explorer 11 is no longer supported",
	}
	for _, s := range enTexts {
		if !strings.Contains(js, s) {
			t.Errorf("ie11-check.js 缺少英文提示文案: %q", s)
		}
	}
}

// TestIE11Check_NoModernCSS 验证提示页不使用 CSS 变量/Grid/gap/clamp 等现代特性
// IE11 不支持这些特性,提示页必须使用基础 HTML/CSS 确保正常显示
// 注: 仅检查 CSS 属性级模式(如 display:grid、grid-template、gap:),避免匹配注释中的说明文字
func TestIE11Check_NoModernCSS(t *testing.T) {
	js := readWebAsset(t, "ie11-check.js")
	lower := strings.ToLower(js)

	// 禁用的现代 CSS 特性: 自定义属性引用、Grid 布局、gap 间距、clamp 函数
	// 使用 CSS 属性级模式,避免误匹配文件头注释中的说明文字
	forbidden := []string{
		"var(--", // CSS 自定义属性引用 var(--xxx)
		":grid",  // display:grid (覆盖有无空格)
		"grid-",  // grid-template / grid-area / grid-gap / grid-column / grid-row
		"gap:",   // gap 属性 (含 grid-gap / flex-gap)
		"clamp(", // clamp() 函数
	}
	for _, s := range forbidden {
		if strings.Contains(lower, s) {
			t.Errorf("ie11-check.js 提示页不应使用现代 CSS 特性: %q", s)
		}
	}
}

// TestIE11Check_NoModernJS 验证检测脚本不使用 const/let/箭头函数/模板字符串
// IE11 不支持 ES6 语法,检测脚本必须使用 ES5 (var/function) 确保在 IE 中可执行
func TestIE11Check_NoModernJS(t *testing.T) {
	js := readWebAsset(t, "ie11-check.js")

	forbidden := []string{
		"const ", // const 声明
		"let ",   // let 声明
		"=>",     // 箭头函数
		"`",      // 模板字符串
	}
	for _, s := range forbidden {
		if strings.Contains(js, s) {
			t.Errorf("ie11-check.js 检测脚本不应使用现代 JS 语法: %q", s)
		}
	}
}

// TestIE11Check_NonIEEarlyReturn 验证非 IE 环境提前返回,不执行阻断逻辑
// 现代 Edge/Chrome 不得被误拦截,检测到 document.documentMode 非 number 时立即 return
func TestIE11Check_NonIEEarlyReturn(t *testing.T) {
	js := readWebAsset(t, "ie11-check.js")
	if !strings.Contains(js, "return") {
		t.Error("ie11-check.js 非 IE 环境必须提前 return,不执行阻断逻辑")
	}
}
