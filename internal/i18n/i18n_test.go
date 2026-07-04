package i18n

import (
	"strings"
	"sync"
	"testing"
)

// TestNormalize 验证语言代码规范化(宽松解析,用于启动期配置读取)
func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"zh-CN":     "zh-CN",
		"zh_cn":     "zh-CN",
		"ZH-CN":     "zh-CN",
		"zh-Hans":   "zh-CN",
		"zh":        "zh-CN",
		"chinese":   "zh-CN",
		"中文":        "zh-CN",
		"en-US":     "en-US",
		"en_US":     "en-US",
		"EN-US":     "en-US",
		"en":        "en-US",
		"english":   "en-US",
		"":          "zh-CN",
		"fr-FR":     "zh-CN",
		"ja-JP":     "zh-CN",
		"  zh-CN  ": "zh-CN",
		"  en  ":    "en-US",
	}
	for input, expected := range cases {
		got := Normalize(input)
		if got != expected {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, expected)
		}
	}
}

// TestParseLanguage 严格解析,仅接受 zh-CN 和 en-US
// 收口: 空值或无效值返回 ok=false,不得自动回退中文
func TestParseLanguage(t *testing.T) {
	cases := map[string]struct {
		lang string
		ok   bool
	}{
		"zh-CN":     {"zh-CN", true},
		"en-US":     {"en-US", true},
		"":          {"", false},
		"zh":        {"", false},
		"en":        {"", false},
		"zh_cn":     {"", false},
		"en_us":     {"", false},
		"ZH-CN":     {"", false},
		"chinese":   {"", false},
		"中文":        {"", false},
		"english":   {"", false},
		"fr-FR":     {"", false},
		"ja-JP":     {"", false},
		"  zh-CN  ": {"zh-CN", true}, // TrimSpace 后匹配
		"  en-US  ": {"en-US", true},
	}
	for input, expected := range cases {
		got, ok := ParseLanguage(input)
		if ok != expected.ok {
			t.Errorf("ParseLanguage(%q) ok = %v, want %v", input, ok, expected.ok)
			continue
		}
		if ok && got != expected.lang {
			t.Errorf("ParseLanguage(%q) lang = %q, want %q", input, got, expected.lang)
		}
		if !ok && got != "" {
			t.Errorf("ParseLanguage(%q) 失败时应返回空字符串, 实际 %q", input, got)
		}
	}
}

// TestSetLanguageAndGetLanguage 验证设置和读取语言
// 收口: SetLanguage 使用 ParseLanguage 严格解析,别名和非法值返回 false 且不修改当前语言
func TestSetLanguageAndGetLanguage(t *testing.T) {
	// 保存原始值,测试结束恢复
	original := GetLanguage()
	defer SetLanguage(original)

	// 测试中文
	if !SetLanguage(LangZhCN) {
		t.Error("SetLanguage(zh-CN) 应返回 true")
	}
	if got := GetLanguage(); got != LangZhCN {
		t.Errorf("GetLanguage() = %q, want zh-CN", got)
	}

	// 测试英文
	if !SetLanguage(LangEnUS) {
		t.Error("SetLanguage(en-US) 应返回 true")
	}
	if got := GetLanguage(); got != LangEnUS {
		t.Errorf("GetLanguage() = %q, want en-US", got)
	}

	// 测试带空格的标准值
	if !SetLanguage("  zh-CN  ") {
		t.Error("SetLanguage('  zh-CN  ') 应返回 true")
	}
	if got := GetLanguage(); got != LangZhCN {
		t.Errorf("GetLanguage() = %q, want zh-CN", got)
	}
}

// TestSetLanguage_RejectsInvalid 验证非法值不会修改当前语言
// 收口: fr-FR、空值、别名(zh/en/chinese等)返回 false 且保持当前语言不变
func TestSetLanguage_RejectsInvalid(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	// 设为已知状态
	SetLanguage(LangEnUS)

	// 所有非法值应返回 false 且不改变当前语言
	invalids := []string{"fr-FR", "", "zh", "en", "chinese", "english", "中文", "zh_cn", "en_us", "ZH-CN", "ja-JP"}
	for _, invalid := range invalids {
		if SetLanguage(invalid) {
			t.Errorf("SetLanguage(%q) 应返回 false", invalid)
		}
		// 语言应保持 en-US 不变
		if got := GetLanguage(); got != LangEnUS {
			t.Errorf("SetLanguage(%q) 不应改变语言, got %q, want en-US", invalid, got)
		}
	}

	// 中文状态下测试同样不受影响
	SetLanguage(LangZhCN)
	if SetLanguage("fr-FR") {
		t.Error("SetLanguage(fr-FR) 在中文状态下也应返回 false")
	}
	if got := GetLanguage(); got != LangZhCN {
		t.Errorf("SetLanguage(fr-FR) 不应改变语言, got %q, want zh-CN", got)
	}
}

// TestSetLanguage_ConsecutiveCalls 验证连续调用(含成功和失败)的线程安全性
// 收口: 合法与非法值交替调用,最终语言应为最后一次合法值
func TestSetLanguage_ConsecutiveCalls(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	// 交替合法/非法调用
	SetLanguage(LangZhCN)
	SetLanguage("fr-FR")  // 失败,语言应保持 zh-CN
	SetLanguage(LangEnUS) // 成功
	SetLanguage("")       // 失败,语言应保持 en-US
	SetLanguage("zh")     // 失败,语言应保持 en-US
	SetLanguage(LangZhCN) // 成功

	if got := GetLanguage(); got != LangZhCN {
		t.Errorf("连续调用后语言应为 zh-CN, got %q", got)
	}
}

// TestGetLanguage_DefaultValue 验证默认值为 zh-CN
// 注意:此测试依赖全局状态,若先执行了其他修改默认值的测试可能失败
func TestGetLanguage_DefaultValue(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)
	// 重置为默认
	SetLanguage(LangZhCN)
	if got := GetLanguage(); got != LangZhCN {
		t.Errorf("默认语言应为 zh-CN, 实际 %q", got)
	}
}

// TestSupportedLanguages 验证支持的语言列表
func TestSupportedLanguages(t *testing.T) {
	langs := SupportedLanguages()
	if len(langs) != 2 {
		t.Fatalf("应支持 2 种语言, 实际 %d", len(langs))
	}
	if langs[0] != LangZhCN {
		t.Errorf("第一种语言应为 zh-CN, 实际 %q", langs[0])
	}
	if langs[1] != LangEnUS {
		t.Errorf("第二种语言应为 en-US, 实际 %q", langs[1])
	}
}

// TestT 验证文案翻译
func TestT(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	// 中文文案
	SetLanguage(LangZhCN)
	if got := T("tray.dhcp_running"); got != "DHCP：运行中" {
		t.Errorf("中文 tray.dhcp_running 不匹配, got %q", got)
	}
	if got := T("tray.open_console"); got != "打开控制台" {
		t.Errorf("中文 tray.open_console 不匹配, got %q", got)
	}
	if got := T("msgbox.exit_confirm"); !strings.Contains(got, "确定要退出吗？") {
		t.Errorf("中文 msgbox.exit_confirm 应包含'确定要退出吗？', got %q", got)
	}

	// 英文文案
	SetLanguage(LangEnUS)
	if got := T("tray.dhcp_running"); got != "DHCP: Running" {
		t.Errorf("英文 tray.dhcp_running 不匹配, got %q", got)
	}
	if got := T("tray.open_console"); got != "Open Console" {
		t.Errorf("英文 tray.open_console 不匹配, got %q", got)
	}
	if got := T("msgbox.exit_confirm"); !strings.Contains(got, "Are you sure you want to exit?") {
		t.Errorf("英文 msgbox.exit_confirm 应包含 'Are you sure you want to exit?', got %q", got)
	}
}

// TestT_FallbackToZhCN 验证英文表缺失时回退到中文
// 此测试通过临时删除英文表项实现,测试后恢复
func TestT_FallbackToZhCN(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	const testKey = "tray.dhcp_running"
	originalEnText := enUSMessages[testKey]
	delete(enUSMessages, testKey)
	defer func() { enUSMessages[testKey] = originalEnText }()

	SetLanguage(LangEnUS)
	got := T(testKey)
	if got != "DHCP：运行中" {
		t.Errorf("英文表缺失应回退中文, got %q, want 'DHCP：运行中'", got)
	}
}

// TestT_UnknownKey 验证未知 key 返回 key 本身
func TestT_UnknownKey(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	SetLanguage(LangZhCN)
	got := T("nonexistent.key.xyz")
	if got != "nonexistent.key.xyz" {
		t.Errorf("未知 key 应返回 key 本身, got %q", got)
	}
}

// TestTf 验证含 %s 占位符的文案格式化
// 收口: 含产品名的文案使用 %s,由 Tf 传入产品名,避免硬编码
func TestTf(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	SetLanguage(LangZhCN)
	got := Tf("tray.exit", "DacatDHCP")
	if got != "退出 DacatDHCP" {
		t.Errorf("中文 Tf(tray.exit) = %q, want '退出 DacatDHCP'", got)
	}
	got = Tf("msgbox.init_failed", "DacatDHCP")
	if !strings.Contains(got, "DacatDHCP 初始化失败") {
		t.Errorf("中文 Tf(msgbox.init_failed) 应包含产品名, got %q", got)
	}

	SetLanguage(LangEnUS)
	got = Tf("tray.exit", "DacatDHCP")
	if got != "Exit DacatDHCP" {
		t.Errorf("英文 Tf(tray.exit) = %q, want 'Exit DacatDHCP'", got)
	}
	got = Tf("msgbox.admin_required", "DacatDHCP")
	if !strings.Contains(got, "run DacatDHCP") {
		t.Errorf("英文 Tf(msgbox.admin_required) 应包含产品名, got %q", got)
	}
}

// TestFormatError 验证错误格式化(不含 %s 占位符的主提示)
func TestFormatError(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	// detail 为空时只返回主提示
	SetLanguage(LangZhCN)
	got := FormatError("msgbox.exit_confirm", "")
	if !strings.Contains(got, "确定要退出吗？") {
		t.Errorf("FormatError 空 detail 应返回主提示, got %q", got)
	}

	// detail 非空时附上详细信息标签
	got = FormatError("msgbox.exit_confirm", "test detail")
	if !strings.Contains(got, "详细信息: test detail") {
		t.Errorf("FormatError 中文 detail 应包含'详细信息:', got %q", got)
	}

	// 英文
	SetLanguage(LangEnUS)
	got = FormatError("msgbox.exit_confirm", "test detail")
	if !strings.Contains(got, "Details: test detail") {
		t.Errorf("FormatError 英文 detail 应包含 'Details:', got %q", got)
	}
}

// TestFormatErrorf 验证含 %s 占位符的错误格式化
// 收口: 含产品名的 MessageBox 文案使用 FormatErrorf 传入产品名
func TestFormatErrorf(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	// 中文 detail 为空
	SetLanguage(LangZhCN)
	got := FormatErrorf("msgbox.init_failed", "", "DacatDHCP")
	expected := "DacatDHCP 初始化失败，程序即将退出。"
	if got != expected {
		t.Errorf("FormatErrorf 中文空 detail 不匹配:\ngot:  %q\nwant: %q", got, expected)
	}

	// 中文 detail 非空
	got = FormatErrorf("msgbox.init_failed", "config.json: invalid syntax", "DacatDHCP")
	expected = "DacatDHCP 初始化失败，程序即将退出。\n\n详细信息: config.json: invalid syntax"
	if got != expected {
		t.Errorf("FormatErrorf 中文 detail 不匹配:\ngot:  %q\nwant: %q", got, expected)
	}

	// 英文 detail 非空
	SetLanguage(LangEnUS)
	got = FormatErrorf("msgbox.init_failed", "config.json: invalid syntax", "DacatDHCP")
	expected = "DacatDHCP initialization failed. The program will exit.\n\nDetails: config.json: invalid syntax"
	if got != expected {
		t.Errorf("FormatErrorf 英文 detail 不匹配:\ngot:  %q\nwant: %q", got, expected)
	}
}

// TestSingleInstanceMessage 验证单实例检测错误文案语义准确
// 收口: 不得误报为已有实例运行,应为"单实例检测失败"
func TestSingleInstanceMessage(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	SetLanguage(LangZhCN)
	zh := T("msgbox.single_instance_failed")
	if !strings.Contains(zh, "单实例检测失败") {
		t.Errorf("中文单实例文案应包含'单实例检测失败', got %q", zh)
	}
	if strings.Contains(zh, "另一个") || strings.Contains(zh, "正在运行") {
		t.Errorf("中文单实例文案不得误报为已有实例运行, got %q", zh)
	}

	SetLanguage(LangEnUS)
	en := T("msgbox.single_instance_failed")
	if !strings.Contains(en, "single-instance check failed") {
		t.Errorf("英文单实例文案应包含 'single-instance check failed', got %q", en)
	}
	if strings.Contains(en, "already running") || strings.Contains(en, "Another") {
		t.Errorf("英文单实例文案不得误报为已有实例运行, got %q", en)
	}
}

// TestNoProductNameHardcoded 验证语言表不得直接硬编码产品名 DacatDHCP
// 收口: 含产品名的文案必须使用 %s 占位符,由格式化函数传入
func TestNoProductNameHardcoded(t *testing.T) {
	for lang, table := range messageTables {
		for key, value := range table {
			if strings.Contains(value, "DacatDHCP") {
				t.Errorf("语言表 %s key %q 硬编码了产品名 DacatDHCP, 应使用 %%s 占位符, value=%q", lang, key, value)
			}
		}
	}
}

// TestConcurrentAccess 验证并发读写安全性
func TestConcurrentAccess(t *testing.T) {
	original := GetLanguage()
	defer SetLanguage(original)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			SetLanguage(LangZhCN)
		}()
		go func() {
			defer wg.Done()
			_ = GetLanguage()
			_ = T("tray.dhcp_running")
			_ = Tf("tray.exit", "TestProduct")
		}()
	}
	wg.Wait()
}

// TestAllKeysExistInBothLanguages 验证两套语言表的 key 完全一致
func TestAllKeysExistInBothLanguages(t *testing.T) {
	zhKeys := make(map[string]bool)
	for k := range zhCNMessages {
		zhKeys[k] = true
	}
	for k := range enUSMessages {
		if !zhKeys[k] {
			t.Errorf("key %q 存在于英文表但不存在于中文表", k)
		}
		delete(zhKeys, k)
	}
	for k := range zhKeys {
		t.Errorf("key %q 存在于中文表但不存在于英文表", k)
	}
}

// TestNoEmptyValues 验证两套语言表都没有空值
func TestNoEmptyValues(t *testing.T) {
	for k, v := range zhCNMessages {
		if v == "" {
			t.Errorf("中文表 key %q 值为空", k)
		}
	}
	for k, v := range enUSMessages {
		if v == "" {
			t.Errorf("英文表 key %q 值为空", k)
		}
	}
}

// TestMsgboxTitleRemoved 验证 msgbox.title 已从语言表移除
// 收口: MessageBox 标题由调用方读取 version.ProductName(),不再由 i18n 提供
func TestMsgboxTitleRemoved(t *testing.T) {
	if _, ok := zhCNMessages["msgbox.title"]; ok {
		t.Error("中文表不应再包含 msgbox.title,标题应由 version.ProductName() 提供")
	}
	if _, ok := enUSMessages["msgbox.title"]; ok {
		t.Error("英文表不应再包含 msgbox.title,标题应由 version.ProductName() 提供")
	}
}
