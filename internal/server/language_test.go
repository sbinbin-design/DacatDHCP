package server

// 语言国际化测试套件
// 覆盖: 语言规范化与回退、配置迁移、语言 API 校验与安全保护、
// 保存失败回滚、运行中切换语言、onLanguageChange 回调触发、
// 首页语言 meta 注入
// 所有测试通过 a.securityMiddleware(a.buildMux()) 走真实路由+中间件,
// 与 security_test.go 共用 newSecurityTestApp/newLocalWriteRequest 等辅助函数

import (
	"DacatDHCP/internal/i18n"
	"DacatDHCP/internal/version"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// ---- 语言规范化与回退测试 ----

// TestLanguage_DefaultAfterNewAppServer 新建 AppServer 后 GET /api/language 返回非空语言
// 语言新增: 首次运行无配置时 Normalize 回退 zh-CN,NewAppServer 应将 config.Language 设置为规范化后的值
func TestLanguage_DefaultAfterNewAppServer(t *testing.T) {
	// 保存原始全局语言,测试结束恢复
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/api/language", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/language 应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Language != i18n.LangZhCN && resp.Language != i18n.LangEnUS {
		t.Errorf("默认语言应为 zh-CN 或 en-US, 实际 %q", resp.Language)
	}
	// 内存配置应与全局 i18n 状态一致
	app.mu.RLock()
	cfgLang := app.config.Language
	app.mu.RUnlock()
	if cfgLang != resp.Language {
		t.Errorf("内存配置语言 %q 与接口返回 %q 不一致", cfgLang, resp.Language)
	}
}

// TestLanguage_InvalidConfigKeepsGlobalLanguage 配置中语言无效时,NewAppServer 沿用全局语言
// 语言重构: 不再用空配置覆盖 main.go 已确定的语言
// 配置中 Language 无效(如变体 "en")时,NewAppServer 应沿用 main.go 通过 Windows 检测设置的全局语言
// 并将 config.Language 写为标准值,实现旧配置迁移
func TestLanguage_InvalidConfigKeepsGlobalLanguage(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	// 模拟 main.go 已通过 Windows 检测确定语言为 en-US
	i18n.SetLanguage(i18n.LangEnUS)

	dataDir := t.TempDir()
	// 写入包含无效语言变体的配置文件
	configPath := filepath.Join(dataDir, "config.json")
	cfg := Config{
		AdapterName:  "Eth",
		WebPort:      8765,
		LeaseMinutes: 60,
		Language:     "en", // 变体,ParseLanguage 不接受,应沿用全局 en-US
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}

	app, err := NewAppServer(dataDir, testSecurityWebFS())
	if err != nil {
		t.Fatalf("NewAppServer 失败: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	// config.Language 应被写为全局标准值 en-US,而非保留无效的 "en"
	if app.config.Language != i18n.LangEnUS {
		t.Errorf("无效配置 'en' 应沿用全局语言 en-US, 实际 %q", app.config.Language)
	}
	// 全局 i18n 不应被覆盖
	if i18n.GetLanguage() != i18n.LangEnUS {
		t.Errorf("全局 i18n 应保持 en-US, 实际 %q", i18n.GetLanguage())
	}
}

// ---- 配置迁移测试 ----

// TestLanguage_ConfigMigration 旧配置无 Language 字段时,NewAppServer 应正常加载并填充默认语言
// 语言新增: 模拟从旧版本升级,config.json 中没有 language 字段
func TestLanguage_ConfigMigration(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	dataDir := t.TempDir()
	// 写入不含 language 字段的旧配置(模拟旧版本)
	oldConfig := `{"adapter_name":"Eth","pool_start":"192.168.1.100","pool_end":"192.168.1.200","lease_minutes":60,"web_port":8765,"gateway":"","dns_servers":[]}`
	configPath := filepath.Join(dataDir, "config.json")
	if err := os.WriteFile(configPath, []byte(oldConfig), 0644); err != nil {
		t.Fatalf("写入旧配置文件失败: %v", err)
	}

	app, err := NewAppServer(dataDir, testSecurityWebFS())
	if err != nil {
		t.Fatalf("NewAppServer 失败: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	// 旧配置无 language 字段,应回退到默认语言(zh-CN 或由 Windows 界面语言决定)
	app.mu.RLock()
	cfgLang := app.config.Language
	app.mu.RUnlock()
	if cfgLang != i18n.LangZhCN && cfgLang != i18n.LangEnUS {
		t.Errorf("旧配置迁移后语言应为 zh-CN 或 en-US, 实际 %q", cfgLang)
	}
	// 其他字段应保持原值
	if app.config.AdapterName != "Eth" {
		t.Errorf("AdapterName 应为 Eth, 实际 %q", app.config.AdapterName)
	}
	if app.config.PoolStart != "192.168.1.100" {
		t.Errorf("PoolStart 应为 192.168.1.100, 实际 %q", app.config.PoolStart)
	}
}

// ---- 语言 API 校验测试 ----

// TestLanguage_PutZhCNSuccess PUT {"language":"zh-CN"} 切换成功
func TestLanguage_PutZhCNSuccess(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	// 先设置为英文,验证能切回中文
	i18n.SetLanguage(i18n.LangEnUS)
	app.mu.Lock()
	app.config.Language = i18n.LangEnUS
	app.mu.Unlock()

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"zh-CN"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT zh-CN 应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Language != i18n.LangZhCN {
		t.Errorf("响应语言应为 zh-CN, 实际 %q", resp.Language)
	}
	if i18n.GetLanguage() != i18n.LangZhCN {
		t.Errorf("全局 i18n 应为 zh-CN, 实际 %q", i18n.GetLanguage())
	}
	app.mu.RLock()
	cfgLang := app.config.Language
	app.mu.RUnlock()
	if cfgLang != i18n.LangZhCN {
		t.Errorf("内存配置语言应为 zh-CN, 实际 %q", cfgLang)
	}

	// 验证配置已持久化到磁盘
	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	if persisted.Language != i18n.LangZhCN {
		t.Errorf("磁盘配置语言应为 zh-CN, 实际 %q", persisted.Language)
	}
}

// TestLanguage_PutEnUSSuccess PUT {"language":"en-US"} 切换成功
func TestLanguage_PutEnUSSuccess(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT en-US 应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if i18n.GetLanguage() != i18n.LangEnUS {
		t.Errorf("全局 i18n 应为 en-US, 实际 %q", i18n.GetLanguage())
	}
}

// TestLanguage_PutVariantRejected PUT {"language":"zh"} 等别名被拒绝
// 语言重构: 严格只接受 zh-CN/en-US,拒绝 zh/en/english/中文 等别名
func TestLanguage_PutVariantRejected(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	// zh 是别名,应被严格拒绝
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"zh"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT zh 别名应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), errCodeInvalidLanguage) {
		t.Errorf("PUT zh 别名应返回 code=%s, body=%s", errCodeInvalidLanguage, w.Body.String())
	}
}

// TestLanguage_PutUnsupportedRejected PUT 不支持的语言返回 invalid_language
func TestLanguage_PutUnsupportedRejected(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	// 记录初始语言,验证失败后未改变
	initialLang := i18n.GetLanguage()

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"fr-FR"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT fr-FR 应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), errCodeInvalidLanguage) {
		t.Errorf("PUT fr-FR 应返回 code=%s, body=%s", errCodeInvalidLanguage, w.Body.String())
	}
	// 失败后全局语言不应改变
	if i18n.GetLanguage() != initialLang {
		t.Errorf("失败后全局语言应保持 %q, 实际 %q", initialLang, i18n.GetLanguage())
	}
}

// TestLanguage_PutEmptyRejected PUT 空语言返回 invalid_language
func TestLanguage_PutEmptyRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":""}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT 空语言应返回 400, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeInvalidLanguage) {
		t.Errorf("PUT 空语言应返回 code=%s, body=%s", errCodeInvalidLanguage, w.Body.String())
	}
}

// TestLanguage_PutUnknownFieldRejected PUT 含未知字段被严格 JSON 解码拒绝
func TestLanguage_PutUnknownFieldRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"zh-CN","extra":"x"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("含未知字段的 JSON 应返回 400, 实际 %d, body=%s", w.Code, w.Body.String())
	}
}

// TestLanguage_PutTrailingGarbageRejected PUT 尾随垃圾被拒绝
func TestLanguage_PutTrailingGarbageRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"zh-CN"}{}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("尾随垃圾应返回 400, 实际 %d", w.Code)
	}
}

// TestLanguage_PutMethodNotAllowed POST/DELETE 等方法返回 405
func TestLanguage_PutMethodNotAllowed(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch} {
		req := newLocalWriteRequest(app, method, "/api/language", `{"language":"zh-CN"}`)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/language 应返回 405, 实际 %d", method, w.Code)
		}
	}
}

// ---- 安全保护测试 ----

// TestLanguage_PutMissingCSRFRejected PUT 缺少 CSRF 令牌被拒绝(403)
func TestLanguage_PutMissingCSRFRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPut, "/api/language", strings.NewReader(`{"language":"zh-CN"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("缺少 CSRF 令牌应返回 403, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeCSRFTokenInvalid) {
		t.Errorf("缺少 CSRF 令牌应返回 code=%s", errCodeCSRFTokenInvalid)
	}
}

// TestLanguage_PutWrongContentTypeRejected PUT 错误 Content-Type 返回 415
func TestLanguage_PutWrongContentTypeRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodPut, "/api/language", strings.NewReader(`{"language":"zh-CN"}`))
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("错误 Content-Type 应返回 415, 实际 %d", w.Code)
	}
}

// TestLanguage_PutWrongHostRejected PUT 错误 Host 返回 403
func TestLanguage_PutWrongHostRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	req := httptest.NewRequest(http.MethodPut, "/api/language", strings.NewReader(`{"language":"zh-CN"}`))
	req.Host = "evil.example.com:8765"
	req.Header.Set("X-Dacat-CSRF-Token", app.csrfToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("错误 Host 应返回 403, 实际 %d", w.Code)
	}
}

// TestLanguage_PutOversizedBodyRejected PUT 超大请求体返回 413
func TestLanguage_PutOversizedBodyRejected(t *testing.T) {
	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	// 构造超过 64KB 的请求体
	bigData := strings.Repeat("a", 70*1024)
	body := `{"language":"` + bigData + `"}`
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("超大请求体应返回 413, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodePayloadTooLarge) {
		t.Errorf("超大请求体应返回 code=%s", errCodePayloadTooLarge)
	}
}

// ---- 保存失败回滚测试 ----

// TestLanguage_PutSaveFailureRollback 保存失败时内存语言和全局 i18n 状态回滚,返回 language_save_failed
// 语言新增: 通过 testSaveConfigWriter 钩子模拟写入失败,验证回滚逻辑
func TestLanguage_PutSaveFailureRollback(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	// 设置初始语言为中文,尝试切换到英文时让保存失败
	i18n.SetLanguage(i18n.LangZhCN)
	app.mu.Lock()
	app.config.Language = i18n.LangZhCN
	// 注入 writer 钩子模拟写入失败
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		return "", fmt.Errorf("simulated language writer failure")
	}
	app.mu.Unlock()

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("保存失败应返回 500, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), errCodeLanguageSaveFailed) {
		t.Errorf("保存失败应返回 code=%s, body=%s", errCodeLanguageSaveFailed, w.Body.String())
	}
	// 验证全局 i18n 状态已回滚
	if i18n.GetLanguage() != i18n.LangZhCN {
		t.Errorf("保存失败后全局 i18n 应回滚为 zh-CN, 实际 %q", i18n.GetLanguage())
	}
	// 验证内存配置已回滚
	app.mu.RLock()
	cfgLang := app.config.Language
	app.mu.RUnlock()
	if cfgLang != i18n.LangZhCN {
		t.Errorf("保存失败后内存配置应回滚为 zh-CN, 实际 %q", cfgLang)
	}
}

// TestLanguage_PutRenameFailureRollback Rename 失败时也回滚
func TestLanguage_PutRenameFailureRollback(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	i18n.SetLanguage(i18n.LangEnUS)
	app.mu.Lock()
	app.config.Language = i18n.LangEnUS
	// 注入 replacer 钩子模拟 Rename 失败
	app.testSaveConfigReplacer = func(tmpPath, configPath string) error {
		_ = os.Remove(tmpPath) // 清理临时文件
		return fmt.Errorf("simulated language replacer failure")
	}
	app.mu.Unlock()

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"zh-CN"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Rename 失败应返回 500, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeLanguageSaveFailed) {
		t.Errorf("Rename 失败应返回 code=%s", errCodeLanguageSaveFailed)
	}
	// 验证回滚
	if i18n.GetLanguage() != i18n.LangEnUS {
		t.Errorf("Rename 失败后全局 i18n 应回滚为 en-US, 实际 %q", i18n.GetLanguage())
	}
	app.mu.RLock()
	cfgLang := app.config.Language
	app.mu.RUnlock()
	if cfgLang != i18n.LangEnUS {
		t.Errorf("Rename 失败后内存配置应回滚为 en-US, 实际 %q", cfgLang)
	}
}

// ---- 运行中切换语言测试 ----

// TestLanguage_SwitchDuringDHCPRunning DHCP 运行期间允许切换语言
// 语言新增: 与配置修改不同,语言切换不依赖 DHCP 状态,运行中也应允许
func TestLanguage_SwitchDuringDHCPRunning(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newTestAppServer(t)
	// 注入 mock 连接创建函数(已在 newTestAppServer 中完成),启动 DHCP
	err := app.dhcpSrv.Start("TestAdapter",
		net.ParseIP("192.168.1.1"),
		net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"),
		net.ParseIP("192.168.1.200"),
		60,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("Start DHCP 失败: %v", err)
	}
	if !app.IsDHCPRunning() {
		t.Fatal("DHCP 应处于运行状态")
	}

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("运行中切换语言应返回 200, 实际 %d, body=%s", w.Code, w.Body.String())
	}
	if i18n.GetLanguage() != i18n.LangEnUS {
		t.Errorf("运行中切换后全局 i18n 应为 en-US, 实际 %q", i18n.GetLanguage())
	}
}

// ---- onLanguageChange 回调测试 ----

// TestLanguage_CallbackTriggered 保存成功后触发 onLanguageChange 回调
func TestLanguage_CallbackTriggered(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	var callbackCount int32
	app.SetOnLanguageChange(func() {
		atomic.AddInt32(&callbackCount, 1)
	})

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT 应返回 200, 实际 %d", w.Code)
	}
	if atomic.LoadInt32(&callbackCount) != 1 {
		t.Errorf("onLanguageChange 回调应被触发 1 次, 实际 %d", atomic.LoadInt32(&callbackCount))
	}
}

// TestLanguage_CallbackNotTriggeredOnFailure 保存失败时不触发回调
func TestLanguage_CallbackNotTriggeredOnFailure(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	var callbackCount int32
	app.SetOnLanguageChange(func() {
		atomic.AddInt32(&callbackCount, 1)
	})
	app.mu.Lock()
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		return "", fmt.Errorf("simulated language writer failure")
	}
	app.mu.Unlock()

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("保存失败应返回 500, 实际 %d", w.Code)
	}
	if atomic.LoadInt32(&callbackCount) != 0 {
		t.Errorf("保存失败不应触发回调, 实际触发 %d 次", atomic.LoadInt32(&callbackCount))
	}
}

// TestLanguage_CallbackClearedOnCloseServices ClearOnLanguageChange 后回调被清除,不再触发
func TestLanguage_CallbackClearedOnCloseServices(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	var callbackCount int32
	app.SetOnLanguageChange(func() {
		atomic.AddInt32(&callbackCount, 1)
	})

	// 模拟退出流程: ClearOnLanguageChange 后再 PUT 不应触发回调
	app.ClearOnLanguageChange()
	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT 应返回 200, 实际 %d", w.Code)
	}
	if atomic.LoadInt32(&callbackCount) != 0 {
		t.Errorf("清除回调后不应触发, 实际触发 %d 次", atomic.LoadInt32(&callbackCount))
	}
}

// TestLanguage_CallbackDuringClosing closing=true 时拒绝修改语言
func TestLanguage_CallbackDuringClosing(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	app.SetClosing()
	initialLang := i18n.GetLanguage()

	handler := app.securityMiddleware(app.buildMux())
	req := newLocalWriteRequest(app, http.MethodPut, "/api/language", `{"language":"en-US"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("closing=true 时应返回 503, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), errCodeServiceClosing) {
		t.Errorf("closing=true 时应返回 code=%s", errCodeServiceClosing)
	}
	// 语言不应改变
	if i18n.GetLanguage() != initialLang {
		t.Errorf("closing 期间语言不应改变, 初始 %q, 实际 %q", initialLang, i18n.GetLanguage())
	}
}

// ---- 首页语言 meta 注入测试 ----

// TestLanguage_IndexPageInjectsLanguageMeta 首页注入语言 meta
// 语言新增: 验证 handleIndex 将 languageMetaPlaceholder 替换为当前语言
func TestLanguage_IndexPageInjectsLanguageMeta(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	i18n.SetLanguage(i18n.LangEnUS)
	handler := app.securityMiddleware(app.buildMux())

	req := newLocalRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("首页应返回 200, 实际 %d", w.Code)
	}
	body := w.Body.String()
	// 占位符应被替换
	if strings.Contains(body, languageMetaPlaceholder) {
		t.Errorf("首页应注入真实语言, 仍包含占位符")
	}
	// 应包含当前语言 en-US
	expectedMeta := `<meta name="dacat-language" content="en-US">`
	if !strings.Contains(body, expectedMeta) {
		t.Errorf("首页应包含 %q, body=%s", expectedMeta, body)
	}
}

// ---- 语言重构新增测试 ----

// TestLanguage_NewAppServerDoesNotOverwriteDetectedLanguage 空配置时不覆盖 main.go 已检测的语言
// 语言重构: main.go 通过 Windows 界面语言检测设置全局语言后,NewAppServer 不得用空配置覆盖
// 此测试模拟 main.go 已设置 en-US,NewAppServer 读到空配置后应保持 en-US
func TestLanguage_NewAppServerDoesNotOverwriteDetectedLanguage(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	// 模拟 main.go 通过 Windows 检测确定语言为 en-US(英文 Windows 首次启动场景)
	i18n.SetLanguage(i18n.LangEnUS)

	dataDir := t.TempDir()
	// 写入不含 language 字段的配置(模拟首次运行或旧配置)
	oldConfig := `{"adapter_name":"Eth","pool_start":"192.168.1.100","pool_end":"192.168.1.200","lease_minutes":60,"web_port":8765,"gateway":"","dns_servers":[]}`
	configPath := filepath.Join(dataDir, "config.json")
	if err := os.WriteFile(configPath, []byte(oldConfig), 0644); err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}

	app, err := NewAppServer(dataDir, testSecurityWebFS())
	if err != nil {
		t.Fatalf("NewAppServer 失败: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	// 全局语言应保持 main.go 设置的 en-US,不被空配置覆盖
	if i18n.GetLanguage() != i18n.LangEnUS {
		t.Errorf("空配置不应覆盖 main.go 检测的语言 en-US, 实际 %q", i18n.GetLanguage())
	}
	// config.Language 应被写为全局标准值 en-US
	if app.config.Language != i18n.LangEnUS {
		t.Errorf("空配置时 config.Language 应填充全局值 en-US, 实际 %q", app.config.Language)
	}
}

// TestLanguage_PutRejectsAllAliases PUT 所有别名变体均被拒绝
// 语言重构: 严格只接受 zh-CN/en-US,拒绝 zh/en/english/中文/zh_cn/en_us/ZH-CN 等所有别名
func TestLanguage_PutRejectsAllAliases(t *testing.T) {
	originalLang := i18n.GetLanguage()
	defer i18n.SetLanguage(originalLang)

	app := newSecurityTestApp(t)
	handler := app.securityMiddleware(app.buildMux())

	// 所有不支持的别名变体均应被拒绝
	aliases := []string{"zh", "en", "english", "中文", "zh_cn", "en_us", "ZH-CN", "zh-Hans", "chinese"}
	for _, alias := range aliases {
		body := `{"language":"` + alias + `"}`
		req := newLocalWriteRequest(app, http.MethodPut, "/api/language", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("别名 %q 应返回 400, 实际 %d, body=%s", alias, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), errCodeInvalidLanguage) {
			t.Errorf("别名 %q 应返回 code=%s, body=%s", alias, errCodeInvalidLanguage, w.Body.String())
		}
	}
}

// TestLanguage_ProductNameFromVersion 验证产品名来自 internal/version 而非硬编码
// 语言重构: MessageBox 标题由 version.ProductName() 提供,语言表不得硬编码产品名
func TestLanguage_ProductNameFromVersion(t *testing.T) {
	// version.ProductName() 应返回非空字符串
	name := version.ProductName()
	if name == "" {
		t.Fatal("version.ProductName() 不应返回空字符串")
	}
	// 验证 i18n.T("msgbox.title") 返回 key 本身(说明已从语言表移除,标题改由 version.ProductName() 提供)
	got := i18n.T("msgbox.title")
	if got != "msgbox.title" {
		t.Errorf("msgbox.title 应已从语言表移除,T 应返回 key 本身, 实际 %q", got)
	}
}
