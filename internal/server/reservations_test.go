package server

// V1.0.3新增: 固定映射(MAC-IP)校验与转换函数测试
// 覆盖: normalizeMAC 规范化、isValidIPv4、processReservations 重复/子网/冲突校验、
// validateReservationsForStart 启动前校验、buildDHCPStaticLeases 启用过滤

import (
	"net"
	"strings"
	"testing"
)

// ---- normalizeMAC ----

func TestNormalizeMAC_ColonFormat(t *testing.T) {
	got, ok := normalizeMAC("00:11:22:33:44:55")
	if !ok {
		t.Fatal("冒号格式应为合法")
	}
	if got != "00:11:22:33:44:55" {
		t.Errorf("规范化后应为 00:11:22:33:44:55,实际 %s", got)
	}
}

func TestNormalizeMAC_HyphenFormat(t *testing.T) {
	got, ok := normalizeMAC("00-11-22-33-44-55")
	if !ok {
		t.Fatal("连字符格式应为合法")
	}
	if got != "00:11:22:33:44:55" {
		t.Errorf("连字符应规范化为冒号大写格式,实际 %s", got)
	}
}

func TestNormalizeMAC_LowercaseToUppercase(t *testing.T) {
	got, ok := normalizeMAC("aa:bb:cc:dd:ee:ff")
	if !ok {
		t.Fatal("小写 MAC 应合法")
	}
	if got != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("小写应规范化为大写,实际 %s", got)
	}
}

func TestNormalizeMAC_Invalid(t *testing.T) {
	invalid := []string{"", "001122334455", "00:11:22:33:44", "ZZ:11:22:33:44:55", "00:11:22:33:44:55:66"}
	for _, s := range invalid {
		if _, ok := normalizeMAC(s); ok {
			t.Errorf("非法 MAC %q 不应通过校验", s)
		}
	}
}

// ---- isValidIPv4 ----

func TestIsValidIPv4(t *testing.T) {
	valid := []string{"192.168.1.1", "0.0.0.0", "255.255.255.255", "10.0.0.1"}
	for _, s := range valid {
		if !isValidIPv4(s) {
			t.Errorf("合法 IPv4 %q 应通过校验", s)
		}
	}
	invalid := []string{"", "999.1.1.1", "192.168.1", "abc", "::1"}
	for _, s := range invalid {
		if isValidIPv4(s) {
			t.Errorf("非法 IPv4 %q 不应通过校验", s)
		}
	}
}

// ---- processReservations: MAC 重复 ----

func TestProcessReservations_MACDuplicate(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.101", Enabled: true},
		{MAC: "00-11-22-33-44-55", IP: "192.168.1.102", Enabled: true}, // 规范化后与上一条重复
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("MAC 重复应返回错误")
	}
	if code != errCodeResvMACDuplicate {
		t.Errorf("错误码应为 resv_mac_duplicate,实际 %s", code)
	}
}

// ---- processReservations: IP 重复 ----

func TestProcessReservations_IPDuplicate(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.101", Enabled: true},
		{MAC: "00:11:22:33:44:66", IP: "192.168.1.101", Enabled: true}, // IP 重复
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("IP 重复应返回错误")
	}
	if code != errCodeResvIPDuplicate {
		t.Errorf("错误码应为 resv_ip_duplicate,实际 %s", code)
	}
}

// ---- processReservations: 跨网段 ----

// V1.0.3修复: enabled=true 的跨网段映射保存应失败
func TestProcessReservations_SubnetMismatch_Enabled(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: true}, // 不在 192.168.1.0/24
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("启用跨网段应返回错误")
	}
	if code != errCodeResvSubnetMismatch {
		t.Errorf("错误码应为 resv_subnet_mismatch,实际 %s", code)
	}
}

// V1.0.3修复: enabled=false 的跨网段映射应允许保存(不参与阻断性校验)
func TestProcessReservations_SubnetMismatch_Disabled_OK(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: false}, // 跨网段但禁用
	}
	result, _, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err != nil {
		t.Fatalf("禁用的跨网段映射应允许保存: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("应返回 1 条结果,实际 %d", len(result))
	}
	if result[0].IP != "10.0.0.100" {
		t.Errorf("IP 应保留为 10.0.0.100,实际 %s", result[0].IP)
	}
}

// V1.0.3修复: enabled=false 与网卡冲突的映射应允许保存
func TestProcessReservations_ConflictAdapter_Disabled_OK(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.1", Enabled: false}, // 等于网卡 IP 但禁用
	}
	result, _, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err != nil {
		t.Fatalf("禁用的冲突映射应允许保存: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("应返回 1 条结果,实际 %d", len(result))
	}
}

// V1.0.3修复: 混合列表中 enabled=false 跨网段 + enabled=true 正常,应允许保存
func TestProcessReservations_MixedEnabledDisabled_OK(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: false},   // 跨网段但禁用
		{MAC: "00:11:22:33:44:66", IP: "192.168.1.101", Enabled: true}, // 同网段且启用
	}
	result, _, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err != nil {
		t.Fatalf("混合列表应允许保存: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("应返回 2 条结果,实际 %d", len(result))
	}
}

// ---- processReservations: 等于网卡 IP ----

func TestProcessReservations_ConflictAdapter(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.1", Enabled: true}, // 等于网卡 IP
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("等于网卡 IP 应返回错误")
	}
	if code != errCodeResvConflictAdapter {
		t.Errorf("错误码应为 resv_conflict_adapter,实际 %s", code)
	}
}

// ---- processReservations: 等于网关 IP ----

func TestProcessReservations_ConflictGateway(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.254", Enabled: true}, // 等于网关
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "192.168.1.254")
	if err == nil {
		t.Fatal("等于网关 IP 应返回错误")
	}
	if code != errCodeResvConflictGateway {
		t.Errorf("错误码应为 resv_conflict_gateway,实际 %s", code)
	}
}

// ---- processReservations: 等于网络地址 ----

func TestProcessReservations_ConflictNetwork(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.0", Enabled: true}, // 网络地址
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("等于网络地址应返回错误")
	}
	if code != errCodeResvConflictNetwork {
		t.Errorf("错误码应为 resv_conflict_network,实际 %s", code)
	}
}

// ---- processReservations: 等于广播地址 ----

func TestProcessReservations_ConflictBroadcast(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.255", Enabled: true}, // 广播地址
	}
	_, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("等于广播地址应返回错误")
	}
	if code != errCodeResvConflictBroadcast {
		t.Errorf("错误码应为 resv_conflict_broadcast,实际 %s", code)
	}
}

// ---- processReservations: 合法映射通过校验并规范化 MAC ----

func TestProcessReservations_Valid(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "aa-bb-cc-dd-ee-ff", IP: "192.168.1.100", Remark: "测试", Enabled: true},
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.101", Enabled: false},
	}
	result, code, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err != nil {
		t.Fatalf("合法映射应通过校验,错误 %v (code=%s)", err, code)
	}
	if len(result) != 2 {
		t.Fatalf("应返回 2 条结果,实际 %d", len(result))
	}
	// 第一条 MAC 应规范化为大写冒号
	if result[0].MAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("第一条 MAC 应规范化为 AA:BB:CC:DD:EE:FF,实际 %s", result[0].MAC)
	}
	if result[0].Remark != "测试" {
		t.Errorf("备注应保留,实际 %q", result[0].Remark)
	}
	// 时间戳应被填充
	if result[0].CreatedAt == "" || result[0].UpdatedAt == "" {
		t.Error("新增映射应填充 created_at 和 updated_at")
	}
}

// ---- processReservations: 保留已有 created_at,变更时更新 updated_at ----

func TestProcessReservations_TimestampPreservedOnNoChange(t *testing.T) {
	existing := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Remark: "旧备注", Enabled: true,
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}
	// 相同内容再次提交,时间戳应保留
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Remark: "旧备注", Enabled: true},
	}
	result, _, err := processReservations(input, existing, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err != nil {
		t.Fatalf("应通过校验: %v", err)
	}
	if result[0].CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at 应保留,实际 %s", result[0].CreatedAt)
	}
	if result[0].UpdatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("无变更时 updated_at 应保留,实际 %s", result[0].UpdatedAt)
	}
}

func TestProcessReservations_TimestampUpdatedOnChange(t *testing.T) {
	existing := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Remark: "旧备注", Enabled: true,
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}
	// IP 变更,created_at 应保留,updated_at 应更新
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.101", Remark: "旧备注", Enabled: true},
	}
	result, _, err := processReservations(input, existing, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err != nil {
		t.Fatalf("应通过校验: %v", err)
	}
	if result[0].CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at 应保留,实际 %s", result[0].CreatedAt)
	}
	if result[0].UpdatedAt == "2026-01-01T00:00:00Z" {
		t.Error("IP 变更时 updated_at 应更新")
	}
}

// ---- processReservations: 网卡信息缺失时跳过子网校验 ----

func TestProcessReservations_NoAdapterInfo_SkipsSubnetCheck(t *testing.T) {
	// adapterIP/subnetMask 为 nil,跨网段 IP 应通过(校验在启动时补做)
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: true},
	}
	result, _, err := processReservations(input, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("网卡信息缺失时应跳过子网校验: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("应返回 1 条结果,实际 %d", len(result))
	}
}

// ---- validateReservationsForStart ----

func TestValidateReservationsForStart_EmptyOK(t *testing.T) {
	_, err := validateReservationsForStart(nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil)
	if err != nil {
		t.Errorf("空列表应通过校验: %v", err)
	}
}

func TestValidateReservationsForStart_MACDuplicate(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Enabled: true},
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.101", Enabled: true},
	}
	code, err := validateReservationsForStart(leases, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil)
	if err == nil {
		t.Fatal("MAC 重复应阻止启动")
	}
	if code != errCodeResvMACDuplicate {
		t.Errorf("错误码应为 resv_mac_duplicate,实际 %s", code)
	}
}

// V1.0.3修复: enabled=true 的跨网段映射应阻止启动
func TestValidateReservationsForStart_SubnetMismatch_Enabled(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: true}, // 跨网段且启用
	}
	code, err := validateReservationsForStart(leases, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil)
	if err == nil {
		t.Fatal("启用的跨网段固定 IP 应阻止启动")
	}
	if code != errCodeResvSubnetMismatch {
		t.Errorf("错误码应为 resv_subnet_mismatch,实际 %s", code)
	}
}

// V1.0.3修复: enabled=false 的跨网段映射不应阻止启动
func TestValidateReservationsForStart_SubnetMismatch_Disabled_OK(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: false}, // 跨网段但禁用
	}
	_, err := validateReservationsForStart(leases, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil)
	if err != nil {
		t.Errorf("禁用的跨网段映射不应阻止启动: %v", err)
	}
}

// V1.0.3修复: enabled=false 与网卡冲突的映射不应阻止启动
func TestValidateReservationsForStart_ConflictAdapter_Disabled_OK(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.1", Enabled: false}, // 等于网卡 IP 但禁用
	}
	_, err := validateReservationsForStart(leases, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil)
	if err != nil {
		t.Errorf("禁用的冲突映射不应阻止启动: %v", err)
	}
}

func TestValidateReservationsForStart_Valid(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Enabled: true},
		{MAC: "00:11:22:33:44:66", IP: "192.168.1.50", Enabled: false}, // 池外但同网段,禁用不参与阻断校验
	}
	_, err := validateReservationsForStart(leases, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil)
	if err != nil {
		t.Errorf("合法映射应通过启动校验: %v", err)
	}
}

// ---- buildDHCPStaticLeases ----

func TestBuildDHCPStaticLeases_EnabledFilter(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Enabled: true},
		{MAC: "00:11:22:33:44:66", IP: "192.168.1.101", Enabled: false}, // 禁用,不应出现
		{MAC: "00:11:22:33:44:77", IP: "192.168.1.102", Enabled: true},
	}
	result := buildDHCPStaticLeases(leases)
	if len(result) != 2 {
		t.Fatalf("仅 enabled=true 的映射应返回,期望 2,实际 %d", len(result))
	}
	// 验证 MAC 和 IP 正确转换
	if result[0].MAC.String() != "00:11:22:33:44:55" {
		t.Errorf("第一条 MAC 应为 00:11:22:33:44:55,实际 %s", result[0].MAC)
	}
	if !result[0].IP.Equal(net.ParseIP("192.168.1.100")) {
		t.Errorf("第一条 IP 应为 192.168.1.100,实际 %s", result[0].IP)
	}
}

func TestBuildDHCPStaticLeases_InvalidSkipped(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "invalid-mac", IP: "192.168.1.100", Enabled: true},         // 非法 MAC,跳过
		{MAC: "00:11:22:33:44:55", IP: "999.999.999.999", Enabled: true}, // 非法 IP,跳过
		{MAC: "00:11:22:33:44:66", IP: "192.168.1.101", Enabled: true},   // 合法
	}
	result := buildDHCPStaticLeases(leases)
	if len(result) != 1 {
		t.Fatalf("非法条目应被跳过,期望 1,实际 %d", len(result))
	}
	if result[0].MAC.String() != "00:11:22:33:44:66" {
		t.Errorf("合法条目 MAC 应为 00:11:22:33:44:66,实际 %s", result[0].MAC)
	}
}

func TestBuildDHCPStaticLeases_AllDisabled(t *testing.T) {
	leases := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "192.168.1.100", Enabled: false},
		{MAC: "00:11:22:33:44:66", IP: "192.168.1.101", Enabled: false},
	}
	result := buildDHCPStaticLeases(leases)
	if len(result) != 0 {
		t.Errorf("全部禁用应返回空列表,实际 %d", len(result))
	}
}

// ---- 确保错误信息包含上下文(用于日志和页面提示) ----

func TestProcessReservations_ErrorContainsContext(t *testing.T) {
	input := []StaticLeaseConfig{
		{MAC: "00:11:22:33:44:55", IP: "10.0.0.100", Enabled: true},
	}
	_, _, err := processReservations(input, nil, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), "")
	if err == nil {
		t.Fatal("跨网段应返回错误")
	}
	// 错误信息应包含具体 IP 和网卡 IP,便于日志和页面展示
	if !strings.Contains(err.Error(), "10.0.0.100") {
		t.Errorf("错误信息应包含冲突 IP,实际: %v", err)
	}
}
