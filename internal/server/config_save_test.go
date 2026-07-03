package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// V16重构: 配置保存测试套件,覆盖安全替换成功/失败、PUT 失败恢复内存、
// DHCP 启动成功但保存失败等关键路径。所有测试使用 t.TempDir 隔离。
// 通过 testSaveConfigWriter / testSaveConfigReplacer 测试钩子注入失败,
// 验证原配置文件不被破坏、内存配置恢复、前端收到明确错误码、临时文件得到清理。
// 注释统一描述为"安全替换/尽力保证完整性",不宣称 Windows 下绝对原子。

// ---- 默认实现测试 ----

// TestSaveConfig_SuccessAndReload 保存成功后从磁盘重新加载字段一致,临时文件已清理
func TestSaveConfig_SuccessAndReload(t *testing.T) {
	app := newTestAppServer(t)

	app.config = Config{
		AdapterName:  "Ethernet",
		PoolStart:    "192.168.1.100",
		PoolEnd:      "192.168.1.200",
		LeaseMinutes: 60,
		WebPort:      8765,
		Gateway:      "192.168.1.254",
		DNSServers:   []string{"8.8.8.8", "8.8.4.4"},
	}
	if err := app.saveConfig(); err != nil {
		t.Fatalf("saveConfig 失败: %v", err)
	}

	// V16: 验证临时文件已被清理(Rename 成功后临时文件不存在)
	// os.CreateTemp 生成的临时文件名为 config.json.*.tmp,匹配检查
	matches, _ := filepath.Glob(filepath.Join(app.configDir, "config.json.*.tmp"))
	if len(matches) > 0 {
		t.Errorf("临时文件应已被 Rename 清理,实际存在: %v", matches)
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}

	// 从磁盘重新加载,验证字段一致
	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	if loaded.AdapterName != "Ethernet" {
		t.Errorf("AdapterName 不一致: 期望 Ethernet, 实际 %s", loaded.AdapterName)
	}
	if loaded.Gateway != "192.168.1.254" {
		t.Errorf("Gateway 不一致: 期望 192.168.1.254, 实际 %s", loaded.Gateway)
	}
	if len(loaded.DNSServers) != 2 || loaded.DNSServers[0] != "8.8.8.8" || loaded.DNSServers[1] != "8.8.4.4" {
		t.Errorf("DNSServers 不一致: 期望 [8.8.8.8, 8.8.4.4], 实际 %v", loaded.DNSServers)
	}
}

// TestSaveConfig_OverwriteExistingConfig 覆盖已有 config.json 再次保存(安全替换/尽力保证完整性)
// V16新增: 验证已有 config.json 被新内容完整替换,临时文件已清理
func TestSaveConfig_OverwriteExistingConfig(t *testing.T) {
	app := newTestAppServer(t)

	// 第一次保存: 写入初始配置
	app.config = Config{
		AdapterName:  "FirstAdapter",
		PoolStart:    "10.0.0.100",
		PoolEnd:      "10.0.0.200",
		LeaseMinutes: 60,
		WebPort:      8765,
		Gateway:      "10.0.0.1",
		DNSServers:   []string{"1.1.1.1"},
	}
	if err := app.saveConfig(); err != nil {
		t.Fatalf("第一次 saveConfig 失败: %v", err)
	}

	// 第二次保存: 覆盖为新配置
	app.config = Config{
		AdapterName:  "SecondAdapter",
		PoolStart:    "192.168.1.100",
		PoolEnd:      "192.168.1.200",
		LeaseMinutes: 120,
		WebPort:      8765,
		Gateway:      "192.168.1.254",
		DNSServers:   []string{"8.8.8.8", "8.8.4.4"},
	}
	if err := app.saveConfig(); err != nil {
		t.Fatalf("第二次 saveConfig 失败: %v", err)
	}

	// 验证文件内容为新配置
	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	if loaded.AdapterName != "SecondAdapter" {
		t.Errorf("AdapterName 应为 SecondAdapter, 实际 %s", loaded.AdapterName)
	}
	if loaded.PoolStart != "192.168.1.100" {
		t.Errorf("PoolStart 应为 192.168.1.100, 实际 %s", loaded.PoolStart)
	}
	if loaded.Gateway != "192.168.1.254" {
		t.Errorf("Gateway 应为 192.168.1.254, 实际 %s", loaded.Gateway)
	}
	if len(loaded.DNSServers) != 2 {
		t.Errorf("DNSServers 应有 2 个, 实际 %d", len(loaded.DNSServers))
	}

	// 验证临时文件已清理
	matches, _ := filepath.Glob(filepath.Join(app.configDir, "config.json.*.tmp"))
	if len(matches) > 0 {
		t.Errorf("覆盖保存后临时文件应已清理,实际存在: %v", matches)
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// ---- 可注入函数失败路径测试 ----

// TestSaveConfig_TempWriteFailurePreservesOriginal 临时文件写入失败时原配置不被破坏
// V16: 通过 testSaveConfigWriter 钩子模拟 Write/Sync/Close 失败
// 验证原 config.json 内容不变、临时文件得到清理
func TestSaveConfig_TempWriteFailurePreservesOriginal(t *testing.T) {
	app := newTestAppServer(t)

	// 先写入一份有效配置作为原始内容
	originalConfig := Config{
		AdapterName:  "OriginalAdapter",
		PoolStart:    "10.0.0.100",
		PoolEnd:      "10.0.0.200",
		LeaseMinutes: 120,
		WebPort:      8765,
		Gateway:      "10.0.0.1",
		DNSServers:   []string{"1.1.1.1"},
	}
	app.config = originalConfig
	if err := app.saveConfig(); err != nil {
		t.Fatalf("首次 saveConfig 失败: %v", err)
	}

	// 读取原始文件内容,用于失败后验证未被破坏
	configPath := filepath.Join(app.configDir, "config.json")
	originalData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取原始配置失败: %v", err)
	}

	// V16: 注入 writer 钩子模拟写入失败(不创建临时文件,直接返回错误)
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		return "", fmt.Errorf("simulated temp write failure")
	}

	// 尝试保存新配置,预期失败
	app.config = Config{
		AdapterName:  "NewAdapter",
		PoolStart:    "192.168.1.100",
		PoolEnd:      "192.168.1.200",
		LeaseMinutes: 60,
		WebPort:      8765,
		Gateway:      "192.168.1.254",
		DNSServers:   []string{"8.8.8.8"},
	}
	saveErr := app.saveConfig()
	if saveErr == nil {
		t.Fatal("注入 writer 失败钩子后 saveConfig 应返回错误")
	}
	if !strings.Contains(saveErr.Error(), "simulated temp write failure") {
		t.Errorf("错误应包含注入的失败信息,实际: %v", saveErr)
	}

	// 验证原配置文件内容未被破坏
	currentData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取失败后配置文件失败: %v", err)
	}
	if string(currentData) != string(originalData) {
		t.Errorf("写入失败后原配置文件应保持不变\n期望: %s\n实际: %s", originalData, currentData)
	}

	// 验证临时文件未被创建(writer 钩子在创建临时文件前就返回)
	matches, _ := filepath.Glob(filepath.Join(app.configDir, "config.json.*.tmp"))
	if len(matches) > 0 {
		t.Errorf("writer 失败后不应有临时文件残留: %v", matches)
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}

	// 清除钩子后应可正常保存
	app.testSaveConfigWriter = nil
	if err := app.saveConfig(); err != nil {
		t.Fatalf("清除钩子后 saveConfig 应成功,实际: %v", err)
	}
}

// TestSaveConfig_SyncFailurePreservesOriginal Sync 失败时原配置不被破坏
// V17修正: 测试名称只描述实际验证的 Sync 失败,不再写 Sync/Close 混淆
// V16: 通过 testSaveConfigWriter 钩子模拟 Sync 失败,验证原 config.json 不变且无 .tmp 残留
func TestSaveConfig_SyncFailurePreservesOriginal(t *testing.T) {
	app := newTestAppServer(t)

	// 先写入一份有效配置作为原始内容
	originalConfig := Config{
		AdapterName:  "OriginalAdapter",
		PoolStart:    "10.0.0.100",
		PoolEnd:      "10.0.0.200",
		LeaseMinutes: 120,
		WebPort:      8765,
		Gateway:      "10.0.0.1",
		DNSServers:   []string{"1.1.1.1"},
	}
	app.config = originalConfig
	if err := app.saveConfig(); err != nil {
		t.Fatalf("首次 saveConfig 失败: %v", err)
	}

	configPath := filepath.Join(app.configDir, "config.json")
	originalData, _ := os.ReadFile(configPath)

	// V16: 注入 writer 钩子模拟 Sync 失败
	// 钩子创建临时文件并 Write 成功,但 Sync 失败,需清理临时文件
	var createdTmpPath string
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		f, err := os.CreateTemp(dir, "config.json.*.tmp")
		if err != nil {
			return "", err
		}
		createdTmpPath = f.Name()
		// Write 成功
		if _, err := f.Write(data); err != nil {
			f.Close()
			_ = os.Remove(createdTmpPath)
			return createdTmpPath, err
		}
		// 模拟 Sync 失败: 关闭并清理临时文件
		_ = f.Close()
		_ = os.Remove(createdTmpPath)
		return createdTmpPath, fmt.Errorf("simulated sync failure")
	}

	// 尝试保存新配置,预期失败
	app.config = Config{
		AdapterName: "NewAdapter",
		WebPort:     8765,
	}
	saveErr := app.saveConfig()
	if saveErr == nil {
		t.Fatal("Sync 失败钩子应返回错误")
	}
	if !strings.Contains(saveErr.Error(), "simulated sync failure") {
		t.Errorf("错误应包含 Sync 失败信息,实际: %v", saveErr)
	}

	// 验证原配置文件未被破坏
	currentData, _ := os.ReadFile(configPath)
	if string(currentData) != string(originalData) {
		t.Errorf("Sync 失败后原配置文件应保持不变\n期望: %s\n实际: %s", originalData, currentData)
	}

	// 验证临时文件已被清理(writer 钩子负责清理)
	if createdTmpPath != "" {
		if _, err := os.Stat(createdTmpPath); !os.IsNotExist(err) {
			t.Errorf("Sync 失败后临时文件应被清理: %s", createdTmpPath)
			_ = os.Remove(createdTmpPath)
		}
	}

	// V17: 验证目录中无任何 config.json.*.tmp 残留
	matches, _ := filepath.Glob(filepath.Join(app.configDir, "config.json.*.tmp"))
	if len(matches) > 0 {
		t.Errorf("Sync 失败后不应有 .tmp 残留: %v", matches)
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// TestSaveConfig_CloseFailurePreservesOriginal Close 失败时原配置不被破坏
// V17新增: 独立的 Close 失败测试,与 Sync 测试分离,验证原 config.json 不变且无 .tmp 残留
func TestSaveConfig_CloseFailurePreservesOriginal(t *testing.T) {
	app := newTestAppServer(t)

	// 先写入一份有效配置作为原始内容
	originalConfig := Config{
		AdapterName:  "OriginalAdapter",
		PoolStart:    "10.0.0.100",
		PoolEnd:      "10.0.0.200",
		LeaseMinutes: 120,
		WebPort:      8765,
		Gateway:      "10.0.0.1",
		DNSServers:   []string{"1.1.1.1"},
	}
	app.config = originalConfig
	if err := app.saveConfig(); err != nil {
		t.Fatalf("首次 saveConfig 失败: %v", err)
	}

	configPath := filepath.Join(app.configDir, "config.json")
	originalData, _ := os.ReadFile(configPath)

	// V17: 注入 writer 钩子模拟 Close 失败
	// 钩子创建临时文件并 Write 和 Sync 成功,但 Close 失败,需清理临时文件
	// Windows 上必须先关闭文件句柄才能删除文件
	var createdTmpPath string
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		f, err := os.CreateTemp(dir, "config.json.*.tmp")
		if err != nil {
			return "", err
		}
		createdTmpPath = f.Name()
		// Write 成功
		if _, err := f.Write(data); err != nil {
			f.Close()
			_ = os.Remove(createdTmpPath)
			return createdTmpPath, err
		}
		// Sync 成功
		if err := f.Sync(); err != nil {
			f.Close()
			_ = os.Remove(createdTmpPath)
			return createdTmpPath, err
		}
		// 模拟 Close 失败: 先关闭文件句柄再删除(Windows 上无法删除仍打开的文件)
		_ = f.Close()
		_ = os.Remove(createdTmpPath)
		return createdTmpPath, fmt.Errorf("simulated close failure")
	}

	// 尝试保存新配置,预期失败
	app.config = Config{
		AdapterName: "NewAdapter",
		WebPort:     8765,
	}
	saveErr := app.saveConfig()
	if saveErr == nil {
		t.Fatal("Close 失败钩子应返回错误")
	}
	if !strings.Contains(saveErr.Error(), "simulated close failure") {
		t.Errorf("错误应包含 Close 失败信息,实际: %v", saveErr)
	}

	// 验证原配置文件未被破坏
	currentData, _ := os.ReadFile(configPath)
	if string(currentData) != string(originalData) {
		t.Errorf("Close 失败后原配置文件应保持不变\n期望: %s\n实际: %s", originalData, currentData)
	}

	// 验证临时文件已被清理(writer 钩子负责清理)
	if createdTmpPath != "" {
		if _, err := os.Stat(createdTmpPath); !os.IsNotExist(err) {
			t.Errorf("Close 失败后临时文件应被清理: %s", createdTmpPath)
			_ = os.Remove(createdTmpPath)
		}
	}

	// V17: 验证目录中无任何 config.json.*.tmp 残留
	matches, _ := filepath.Glob(filepath.Join(app.configDir, "config.json.*.tmp"))
	if len(matches) > 0 {
		t.Errorf("Close 失败后不应有 .tmp 残留: %v", matches)
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// TestSaveConfig_RenameFailurePreservesOriginal Rename 替换失败时原配置不被破坏
// V16: 通过 testSaveConfigReplacer 钩子模拟 Rename 失败,验证临时文件得到清理
func TestSaveConfig_RenameFailurePreservesOriginal(t *testing.T) {
	app := newTestAppServer(t)

	// 先写入一份有效配置作为原始内容
	originalConfig := Config{
		AdapterName:  "OriginalAdapter",
		PoolStart:    "10.0.0.100",
		PoolEnd:      "10.0.0.200",
		LeaseMinutes: 120,
		WebPort:      8765,
		Gateway:      "10.0.0.1",
		DNSServers:   []string{"1.1.1.1"},
	}
	app.config = originalConfig
	if err := app.saveConfig(); err != nil {
		t.Fatalf("首次 saveConfig 失败: %v", err)
	}

	configPath := filepath.Join(app.configDir, "config.json")
	originalData, _ := os.ReadFile(configPath)

	// V16: 注入 replacer 钩子模拟 Rename 失败
	// 钩子不执行实际 Rename,直接返回错误,saveConfig 负责清理临时文件
	var receivedTmpPath string
	app.testSaveConfigReplacer = func(tmpPath, configPath string) error {
		receivedTmpPath = tmpPath
		return fmt.Errorf("simulated rename failure")
	}

	// 尝试保存新配置,预期失败
	app.config = Config{
		AdapterName: "NewAdapter",
		WebPort:     8765,
	}
	saveErr := app.saveConfig()
	if saveErr == nil {
		t.Fatal("Rename 失败钩子应返回错误")
	}
	if !strings.Contains(saveErr.Error(), "simulated rename failure") {
		t.Errorf("错误应包含 Rename 失败信息,实际: %v", saveErr)
	}

	// 验证原配置文件未被破坏
	currentData, _ := os.ReadFile(configPath)
	if string(currentData) != string(originalData) {
		t.Errorf("Rename 失败后原配置文件应保持不变\n期望: %s\n实际: %s", originalData, currentData)
	}

	// 验证临时文件已被 saveConfig 清理(replacer 失败后 saveConfig 执行 os.Remove)
	if receivedTmpPath != "" {
		if _, err := os.Stat(receivedTmpPath); !os.IsNotExist(err) {
			t.Errorf("Rename 失败后临时文件应被 saveConfig 清理: %s", receivedTmpPath)
			_ = os.Remove(receivedTmpPath)
		}
	}
}

// ---- HTTP API 失败路径测试 ----

// TestHandleConfig_PutSaveFailureRestoresMemory PUT 写入失败时恢复内存配置并返回 config_save_failed
// V16: 通过 testSaveConfigWriter 钩子模拟写入失败
func TestHandleConfig_PutSaveFailureRestoresMemory(t *testing.T) {
	app := newTestAppServer(t)

	// 先写入一份有效配置作为原始内容
	originalConfig := Config{
		AdapterName:  "OriginalAdapter",
		PoolStart:    "10.0.0.100",
		PoolEnd:      "10.0.0.200",
		LeaseMinutes: 120,
		WebPort:      8765,
		Gateway:      "10.0.0.1",
		DNSServers:   []string{"1.1.1.1"},
	}
	app.config = originalConfig
	if err := app.saveConfig(); err != nil {
		t.Fatalf("首次 saveConfig 失败: %v", err)
	}

	// V16: 注入 writer 钩子模拟写入失败
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		return "", fmt.Errorf("simulated write failure")
	}

	// 构造 PUT 请求体
	body := `{"adapter_name":"NewAdapter","pool_start":"192.168.1.100","pool_end":"192.168.1.200","lease_minutes":60,"web_port":8765,"gateway":"192.168.1.254","dns_servers":["8.8.8.8"]}`
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	w := httptest.NewRecorder()

	app.handleConfig(w, req)

	// 预期返回 500 和 config_save_failed
	if w.Code != http.StatusInternalServerError {
		t.Errorf("期望 500,实际 %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp["code"] != errCodeConfigSaveFailed {
		t.Errorf("期望 code=%s, 实际 %s", errCodeConfigSaveFailed, resp["code"])
	}

	// 验证内存配置已恢复为原始值(handleConfig 在 saveConfig 失败时应回滚 a.config)
	app.mu.RLock()
	memConfig := app.config
	app.mu.RUnlock()
	if memConfig.AdapterName != originalConfig.AdapterName {
		t.Errorf("内存配置应恢复为 OriginalAdapter, 实际 %s", memConfig.AdapterName)
	}
	if memConfig.Gateway != originalConfig.Gateway {
		t.Errorf("内存配置 Gateway 应恢复为 %s, 实际 %s", originalConfig.Gateway, memConfig.Gateway)
	}

	// 验证原配置文件未被破坏
	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	if loaded.AdapterName != originalConfig.AdapterName {
		t.Errorf("原配置文件应保持 OriginalAdapter, 实际 %s", loaded.AdapterName)
	}
}

// TestHandleStart_SaveFailureReturnsOkTrue DHCP 启动成功但配置保存失败时
// 必须返回 ok=true 及 config_save_fail=true,避免前端误判服务状态
// V16: 通过 testSaveConfigWriter 钩子模拟写入失败
func TestHandleStart_SaveFailureReturnsOkTrue(t *testing.T) {
	app := newTestAppServer(t)
	app.PostInit()

	// 先保存一份原始配置
	originalConfig := Config{
		AdapterName:  "",
		PoolStart:    "",
		PoolEnd:      "",
		LeaseMinutes: 60,
		WebPort:      8765,
	}
	app.config = originalConfig
	if err := app.saveConfig(); err != nil {
		t.Fatalf("首次 saveConfig 失败: %v", err)
	}

	// V15: 注入测试网卡查找函数,绕过真实网卡枚举
	app.testFindAdapterFunc = func(name string) (net.IP, net.IPMask, error) {
		return net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil
	}

	// V16: 注入 writer 钩子模拟写入失败
	app.testSaveConfigWriter = func(dir string, data []byte) (string, error) {
		return "", fmt.Errorf("simulated write failure")
	}

	// 构造启动请求
	body := `{"adapter_name":"TestAdapter","pool_start":"192.168.1.100","pool_end":"192.168.1.200","lease_minutes":60,"gateway":"","dns_servers":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/start", strings.NewReader(body))
	w := httptest.NewRecorder()

	app.handleStart(w, req)

	// DHCP 已启动,即使保存失败也返回 200
	if w.Code != http.StatusOK {
		t.Errorf("期望 200(DHCP 已启动),实际 %d, body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, w.Body.String())
	}
	// ok 必须为 true,服务确实在运行
	if resp["ok"] != true {
		t.Errorf("期望 ok=true(DHCP 已启动),实际 %v", resp["ok"])
	}
	// config_save_fail 必须为 true,告知前端保存失败
	if resp["config_save_fail"] != true {
		t.Errorf("期望 config_save_fail=true,实际 %v", resp["config_save_fail"])
	}
	// code 必须为 config_save_failed
	if resp["code"] != errCodeConfigSaveFailed {
		t.Errorf("期望 code=%s, 实际 %v", errCodeConfigSaveFailed, resp["code"])
	}

	// 验证 DHCP 服务确实在运行
	if !app.dhcpSrv.IsRunning() {
		t.Error("DHCP 服务应在运行中")
	}

	// 验证原配置文件未被破坏(启动后 saveConfig 失败不应破坏原文件)
	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	// 原配置 AdapterName 为空字符串(原始值)
	if loaded.AdapterName != "" {
		t.Errorf("原配置 AdapterName 应为空(原始值), 实际 %s", loaded.AdapterName)
	}

	// 清理: 停止 DHCP 服务
	app.dhcpSrv.Stop()
}

// TestHandleConfig_PutSuccessUpdatesMemory PUT 成功时内存配置被更新
// 此测试作为对照,验证成功路径内存配置确实更新
func TestHandleConfig_PutSuccessUpdatesMemory(t *testing.T) {
	app := newTestAppServer(t)

	body := `{"adapter_name":"NewAdapter","pool_start":"192.168.1.100","pool_end":"192.168.1.200","lease_minutes":60,"web_port":8765,"gateway":"","dns_servers":[]}`
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	w := httptest.NewRecorder()

	app.handleConfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("期望 200,实际 %d, body=%s", w.Code, w.Body.String())
	}

	// 验证内存配置已更新
	app.mu.RLock()
	memConfig := app.config
	app.mu.RUnlock()
	if memConfig.AdapterName != "NewAdapter" {
		t.Errorf("内存配置 AdapterName 应为 NewAdapter, 实际 %s", memConfig.AdapterName)
	}
	if memConfig.PoolStart != "192.168.1.100" {
		t.Errorf("内存配置 PoolStart 应为 192.168.1.100, 实际 %s", memConfig.PoolStart)
	}
}
