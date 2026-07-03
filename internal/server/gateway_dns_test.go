package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// V2新增: 网关和 DNS 校验函数测试
// 所有测试调用真实生产函数 validateGateway / normalizeDNSServers，禁止复制实现
// V2.1扩展: 网关与地址池冲突边界测试

// ---- validateGateway 基础测试（无地址池）----

// TestValidateGateway_EmptyValid 空网关合法，返回 nil 不下发 Option 3
func TestValidateGateway_EmptyValid(t *testing.T) {
	gw, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "", nil, nil)
	if err != nil {
		t.Errorf("空网关应合法，错误: %v", err)
	}
	if gw != nil {
		t.Errorf("空网关应返回 nil，实际: %s", gw)
	}
}

// TestValidateGateway_ValidSameSubnet 合法同子网网关
func TestValidateGateway_ValidSameSubnet(t *testing.T) {
	gw, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.254", nil, nil)
	if err != nil {
		t.Errorf("合法网关应通过校验，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.254").To4()) {
		t.Errorf("网关值应为 192.168.1.254，实际: %s", gw)
	}
}

// TestValidateGateway_EqualsAdapterIP 网关等于网卡 IP 合法
func TestValidateGateway_EqualsAdapterIP(t *testing.T) {
	gw, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.1", nil, nil)
	if err != nil {
		t.Errorf("网关等于网卡 IP 应合法，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.1").To4()) {
		t.Errorf("网关值应为 192.168.1.1，实际: %s", gw)
	}
}

// TestValidateGateway_InvalidIPv4 非法 IPv4 被拒绝
func TestValidateGateway_InvalidIPv4(t *testing.T) {
	_, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "999.999.999.999", nil, nil)
	if err == nil {
		t.Error("非法 IPv4 网关应被拒绝")
	}
}

// TestValidateGateway_CrossSubnet 跨网段网关被拒绝
func TestValidateGateway_CrossSubnet(t *testing.T) {
	_, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "10.0.0.1", nil, nil)
	if err == nil {
		t.Error("跨网段网关应被拒绝")
	}
}

// TestValidateGateway_NetworkAddress 网络地址被拒绝
func TestValidateGateway_NetworkAddress(t *testing.T) {
	_, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.0", nil, nil)
	if err == nil {
		t.Error("网络地址作为网关应被拒绝")
	}
}

// TestValidateGateway_BroadcastAddress 广播地址被拒绝
func TestValidateGateway_BroadcastAddress(t *testing.T) {
	_, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.255", nil, nil)
	if err == nil {
		t.Error("广播地址作为网关应被拒绝")
	}
}

// TestValidateGateway_TrimWhitespace 网关首尾空格被去除
func TestValidateGateway_TrimWhitespace(t *testing.T) {
	gw, err := validateGateway(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "  192.168.1.254  ", nil, nil)
	if err != nil {
		t.Errorf("带空格的合法网关应通过校验，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.254").To4()) {
		t.Errorf("网关值应为 192.168.1.254，实际: %s", gw)
	}
}

// TestValidateGateway_NilAdapterOnlyIPv4Check 网卡信息不足时仅校验 IPv4 格式
func TestValidateGateway_NilAdapterOnlyIPv4Check(t *testing.T) {
	// adapterIP/subnetMask 为 nil，仅校验 IPv4 格式（用于配置保存时网卡未选定）
	gw, err := validateGateway(nil, nil, "192.168.1.254", nil, nil)
	if err != nil {
		t.Errorf("网卡信息不足时合法 IPv4 应通过校验，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.254").To4()) {
		t.Errorf("网关值应为 192.168.1.254，实际: %s", gw)
	}
}

// ---- validateGateway 地址池冲突边界测试（V2.1新增）----
// 地址池 192.168.1.100 - 192.168.1.200，服务端 IP 192.168.1.1

// TestValidateGateway_BelowPoolAllowed 网关低于地址池允许
func TestValidateGateway_BelowPoolAllowed(t *testing.T) {
	gw, err := validateGateway(
		net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.2",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"),
	)
	if err != nil {
		t.Errorf("网关低于地址池应允许，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.2").To4()) {
		t.Errorf("网关值应为 192.168.1.2，实际: %s", gw)
	}
}

// TestValidateGateway_AbovePoolAllowed 网关高于地址池允许
func TestValidateGateway_AbovePoolAllowed(t *testing.T) {
	gw, err := validateGateway(
		net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.254",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"),
	)
	if err != nil {
		t.Errorf("网关高于地址池应允许，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.254").To4()) {
		t.Errorf("网关值应为 192.168.1.254，实际: %s", gw)
	}
}

// TestValidateGateway_EqualsPoolStartRejected 网关等于地址池起始 IP 拒绝
func TestValidateGateway_EqualsPoolStartRejected(t *testing.T) {
	_, err := validateGateway(
		net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.100",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"),
	)
	if err == nil {
		t.Error("网关等于地址池起始 IP 应被拒绝")
	}
}

// TestValidateGateway_EqualsPoolEndRejected 网关等于地址池结束 IP 拒绝
func TestValidateGateway_EqualsPoolEndRejected(t *testing.T) {
	_, err := validateGateway(
		net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.200",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"),
	)
	if err == nil {
		t.Error("网关等于地址池结束 IP 应被拒绝")
	}
}

// TestValidateGateway_InsidePoolRejected 网关位于地址池中间拒绝
func TestValidateGateway_InsidePoolRejected(t *testing.T) {
	_, err := validateGateway(
		net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.150",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"),
	)
	if err == nil {
		t.Error("网关位于地址池中间应被拒绝")
	}
}

// TestValidateGateway_EqualsServerIPNotInPoolAllowed 网关等于服务端 IP 且服务端不在池内允许
func TestValidateGateway_EqualsServerIPNotInPoolAllowed(t *testing.T) {
	gw, err := validateGateway(
		net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.1",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"),
	)
	if err != nil {
		t.Errorf("网关等于服务端 IP 且服务端不在池内应允许，错误: %v", err)
	}
	if !gw.Equal(net.ParseIP("192.168.1.1").To4()) {
		t.Errorf("网关值应为 192.168.1.1，实际: %s", gw)
	}
}

// ---- normalizeDNSServers 测试 ----

// TestNormalizeDNS_EmptyValid 空 DNS 合法，返回 nil 不下发 Option 6
func TestNormalizeDNS_EmptyValid(t *testing.T) {
	ips, err := normalizeDNSServers(nil)
	if err != nil {
		t.Errorf("空 DNS 应合法，错误: %v", err)
	}
	if ips != nil {
		t.Errorf("空 DNS 应返回 nil，实际: %v", ips)
	}
}

// TestNormalizeDNS_SingleValid 单个合法 DNS
func TestNormalizeDNS_SingleValid(t *testing.T) {
	ips, err := normalizeDNSServers([]string{"8.8.8.8"})
	if err != nil {
		t.Fatalf("单个 DNS 应合法，错误: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("8.8.8.8").To4()) {
		t.Errorf("应返回单个 DNS 8.8.8.8，实际: %v", ips)
	}
}

// TestNormalizeDNS_MultipleValid 多个合法 DNS
func TestNormalizeDNS_MultipleValid(t *testing.T) {
	ips, err := normalizeDNSServers([]string{"8.8.8.8", "8.8.4.4", "1.1.1.1"})
	if err != nil {
		t.Fatalf("多个 DNS 应合法，错误: %v", err)
	}
	if len(ips) != 3 {
		t.Fatalf("应返回 3 个 DNS，实际: %d", len(ips))
	}
}

// TestNormalizeDNS_DedupePreserveOrder 去重并保持用户输入顺序
func TestNormalizeDNS_DedupePreserveOrder(t *testing.T) {
	ips, err := normalizeDNSServers([]string{"8.8.8.8", "1.1.1.1", "8.8.8.8", "8.8.4.4", "1.1.1.1"})
	if err != nil {
		t.Fatalf("去重应合法，错误: %v", err)
	}
	if len(ips) != 3 {
		t.Fatalf("去重后应剩 3 个，实际: %d", len(ips))
	}
	// 顺序保持首次出现：8.8.8.8, 1.1.1.1, 8.8.4.4
	expected := []string{"8.8.8.8", "1.1.1.1", "8.8.4.4"}
	for i, want := range expected {
		if !ips[i].Equal(net.ParseIP(want).To4()) {
			t.Errorf("第 %d 个 DNS 应为 %s，实际: %s", i, want, ips[i])
		}
	}
}

// TestNormalizeDNS_MaxThree 最多 3 个 DNS 合法
func TestNormalizeDNS_MaxThree(t *testing.T) {
	ips, err := normalizeDNSServers([]string{"8.8.8.8", "8.8.4.4", "1.1.1.1"})
	if err != nil {
		t.Errorf("3 个 DNS 应合法，错误: %v", err)
	}
	if len(ips) != 3 {
		t.Errorf("应返回 3 个，实际: %d", len(ips))
	}
}

// TestNormalizeDNS_OverThree 超过 3 个 DNS 被拒绝
func TestNormalizeDNS_OverThree(t *testing.T) {
	_, err := normalizeDNSServers([]string{"8.8.8.8", "8.8.4.4", "1.1.1.1", "9.9.9.9"})
	if err == nil {
		t.Error("超过 3 个 DNS 应被拒绝")
	}
}

// TestNormalizeDNS_InvalidIPv4 非法 DNS 被拒绝并指出错误值
func TestNormalizeDNS_InvalidIPv4(t *testing.T) {
	_, err := normalizeDNSServers([]string{"8.8.8.8", "invalid"})
	if err == nil {
		t.Error("非法 DNS 应被拒绝")
	}
}

// TestNormalizeDNS_TrimAndDropEmpty 去除空格和空项
func TestNormalizeDNS_TrimAndDropEmpty(t *testing.T) {
	ips, err := normalizeDNSServers([]string{"  8.8.8.8  ", "", "  ", "8.8.4.4"})
	if err != nil {
		t.Fatalf("含空格和空项应合法，错误: %v", err)
	}
	if len(ips) != 2 {
		t.Fatalf("去空后应剩 2 个，实际: %d", len(ips))
	}
	if !ips[0].Equal(net.ParseIP("8.8.8.8").To4()) {
		t.Errorf("第 1 个 DNS 应为 8.8.8.8，实际: %s", ips[0])
	}
	if !ips[1].Equal(net.ParseIP("8.8.4.4").To4()) {
		t.Errorf("第 2 个 DNS 应为 8.8.4.4，实际: %s", ips[1])
	}
}

// ---- Config 兼容性与持久化测试 ----

// TestConfig_OldFormatCompatible 旧配置缺少 gateway/dns_servers 字段时正常加载为空
func TestConfig_OldFormatCompatible(t *testing.T) {
	// 模拟旧版 config.json（无 gateway 和 dns_servers 字段）
	oldJSON := `{
		"adapter_name": "Ethernet",
		"pool_start": "192.168.1.100",
		"pool_end": "192.168.1.200",
		"lease_minutes": 60,
		"web_port": 8765
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatalf("旧格式配置应正常加载，错误: %v", err)
	}
	if cfg.Gateway != "" {
		t.Errorf("旧配置加载后 Gateway 应为空，实际: %q", cfg.Gateway)
	}
	if cfg.DNSServers != nil {
		t.Errorf("旧配置加载后 DNSServers 应为 nil，实际: %v", cfg.DNSServers)
	}
}

// TestConfig_SaveReloadGatewayDNS 保存并重新加载后网关、DNS 一致
func TestConfig_SaveReloadGatewayDNS(t *testing.T) {
	app := newTestAppServer(t)

	// 设置含网关和 DNS 的配置
	app.config = Config{
		AdapterName:  "Ethernet",
		PoolStart:    "192.168.1.100",
		PoolEnd:      "192.168.1.200",
		LeaseMinutes: 60,
		WebPort:      8765,
		Gateway:      "192.168.1.254",
		DNSServers:   []string{"8.8.8.8", "8.8.4.4"},
	}
	app.saveConfig()

	// 从磁盘重新读取配置文件，验证字段一致
	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	if loaded.Gateway != "192.168.1.254" {
		t.Errorf("重新加载后 Gateway 应为 192.168.1.254，实际: %q", loaded.Gateway)
	}
	if len(loaded.DNSServers) != 2 {
		t.Fatalf("重新加载后应有 2 个 DNS，实际: %d", len(loaded.DNSServers))
	}
	if loaded.DNSServers[0] != "8.8.8.8" || loaded.DNSServers[1] != "8.8.4.4" {
		t.Errorf("重新加载后 DNS 不一致: %v", loaded.DNSServers)
	}
}

// TestConfig_EmptyGatewayDNS 保存空网关和空 DNS 后重新加载仍为空
func TestConfig_EmptyGatewayDNS(t *testing.T) {
	app := newTestAppServer(t)

	app.config = Config{
		AdapterName:  "Ethernet",
		PoolStart:    "192.168.1.100",
		PoolEnd:      "192.168.1.200",
		LeaseMinutes: 60,
		WebPort:      8765,
		Gateway:      "",
		DNSServers:   nil,
	}
	app.saveConfig()

	configPath := filepath.Join(app.configDir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("解析配置文件失败: %v", err)
	}
	if loaded.Gateway != "" {
		t.Errorf("空网关保存后应为空，实际: %q", loaded.Gateway)
	}
	// nil 切片序列化为 null 或不存在，反序列化后可能为 nil 或空
	if len(loaded.DNSServers) != 0 {
		t.Errorf("空 DNS 保存后应为空，实际: %v", loaded.DNSServers)
	}
}
