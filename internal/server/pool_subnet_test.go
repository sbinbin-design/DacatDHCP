package server

import (
	"net"
	"testing"
)

// V1.0.2新增: validatePoolSubnet 地址池/网关同子网校验测试
// 所有测试调用真实生产函数 validatePoolSubnet,禁止复制实现
// 覆盖场景: 同网段允许、起始/结束/网关跨网段拒绝、不同掩码按真实子网计算

// ---- 同子网合法场景 ----

// TestValidatePoolSubnet_SameSubnetValid 同子网地址池和网关合法
func TestValidatePoolSubnet_SameSubnetValid(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)
	code, err := validatePoolSubnet(adapterIP, mask, "192.168.1.254",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"))
	if err != nil {
		t.Errorf("同子网地址池和网关应合法，错误: %v", err)
	}
	if code != "" {
		t.Errorf("合法配置应返回空错误码，实际: %s", code)
	}
}

// TestValidatePoolSubnet_EmptyGatewayValid 空网关合法
func TestValidatePoolSubnet_EmptyGatewayValid(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)
	code, err := validatePoolSubnet(adapterIP, mask, "",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"))
	if err != nil {
		t.Errorf("空网关应合法，错误: %v", err)
	}
	if code != "" {
		t.Errorf("合法配置应返回空错误码，实际: %s", code)
	}
}

// TestValidatePoolSubnet_NilAdapterSkipsCheck 网卡信息不足时跳过子网校验
// 用于配置保存时网卡未选定的场景
func TestValidatePoolSubnet_NilAdapterSkipsCheck(t *testing.T) {
	code, err := validatePoolSubnet(nil, nil, "10.0.0.1",
		net.ParseIP("10.0.0.100"), net.ParseIP("10.0.0.200"))
	if err != nil {
		t.Errorf("网卡信息不足时应跳过子网校验，错误: %v", err)
	}
	if code != "" {
		t.Errorf("跳过校验应返回空错误码，实际: %s", code)
	}
}

// TestValidatePoolSubnet_GatewayEqualsAdapterIPAllowed 网关等于网卡 IP 合法
func TestValidatePoolSubnet_GatewayEqualsAdapterIPAllowed(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)
	code, err := validatePoolSubnet(adapterIP, mask, "192.168.1.1",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"))
	if err != nil {
		t.Errorf("网关等于网卡 IP 应合法，错误: %v", err)
	}
	if code != "" {
		t.Errorf("合法配置应返回空错误码，实际: %s", code)
	}
}

// ---- 跨网段拒绝场景 ----

// TestValidatePoolSubnet_PoolStartCrossSubnet 起始 IP 跨网段拒绝
func TestValidatePoolSubnet_PoolStartCrossSubnet(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)
	code, err := validatePoolSubnet(adapterIP, mask, "",
		net.ParseIP("10.0.0.100"), net.ParseIP("192.168.1.200"))
	if err == nil {
		t.Error("起始 IP 跨网段应被拒绝")
	}
	if code != errCodePoolSubnetMismatch {
		t.Errorf("起始 IP 跨网段应返回 pool_subnet_mismatch，实际: %s", code)
	}
}

// TestValidatePoolSubnet_PoolEndCrossSubnet 结束 IP 跨网段拒绝
func TestValidatePoolSubnet_PoolEndCrossSubnet(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)
	code, err := validatePoolSubnet(adapterIP, mask, "",
		net.ParseIP("192.168.1.100"), net.ParseIP("10.0.0.200"))
	if err == nil {
		t.Error("结束 IP 跨网段应被拒绝")
	}
	if code != errCodePoolSubnetMismatch {
		t.Errorf("结束 IP 跨网段应返回 pool_subnet_mismatch，实际: %s", code)
	}
}

// TestValidatePoolSubnet_GatewayCrossSubnet 网关跨网段拒绝
func TestValidatePoolSubnet_GatewayCrossSubnet(t *testing.T) {
	adapterIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)
	code, err := validatePoolSubnet(adapterIP, mask, "10.0.0.1",
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"))
	if err == nil {
		t.Error("网关跨网段应被拒绝")
	}
	if code != errCodeGatewaySubnetMismatch {
		t.Errorf("网关跨网段应返回 gateway_subnet_mismatch，实际: %s", code)
	}
}

// ---- 不同掩码场景按真实子网计算 ----

// TestValidatePoolSubnet_Non24SubnetValid /16 子网同网段合法
func TestValidatePoolSubnet_Non24SubnetValid(t *testing.T) {
	adapterIP := net.ParseIP("10.0.0.1")
	mask := net.IPv4Mask(255, 255, 0, 0)
	// /16 子网下 10.0.5.100、10.0.5.200、10.0.5.254 都在同子网
	code, err := validatePoolSubnet(adapterIP, mask, "10.0.5.254",
		net.ParseIP("10.0.5.100"), net.ParseIP("10.0.5.200"))
	if err != nil {
		t.Errorf("/16 子网同网段应合法，错误: %v", err)
	}
	if code != "" {
		t.Errorf("合法配置应返回空错误码，实际: %s", code)
	}
}

// TestValidatePoolSubnet_Non24SubnetCrossSubnet /16 子网跨网段拒绝
func TestValidatePoolSubnet_Non24SubnetCrossSubnet(t *testing.T) {
	adapterIP := net.ParseIP("10.0.0.1")
	mask := net.IPv4Mask(255, 255, 0, 0)
	// /16 子网下 10.1.0.100 属于不同子网（10.1.0.0/16）
	code, err := validatePoolSubnet(adapterIP, mask, "",
		net.ParseIP("10.1.0.100"), net.ParseIP("10.0.0.200"))
	if err == nil {
		t.Error("/16 子网起始 IP 跨网段应被拒绝")
	}
	if code != errCodePoolSubnetMismatch {
		t.Errorf("应返回 pool_subnet_mismatch，实际: %s", code)
	}
}

// TestValidatePoolSubnet_20SubnetValid /20 子网同网段合法
func TestValidatePoolSubnet_20SubnetValid(t *testing.T) {
	adapterIP := net.ParseIP("172.16.16.1")
	mask := net.IPv4Mask(255, 255, 240, 0)
	// /20 子网范围: 172.16.16.0 - 172.16.31.255
	// 172.16.20.100、172.16.20.200、172.16.20.254 均在同子网
	code, err := validatePoolSubnet(adapterIP, mask, "172.16.20.254",
		net.ParseIP("172.16.20.100"), net.ParseIP("172.16.20.200"))
	if err != nil {
		t.Errorf("/20 子网同网段应合法，错误: %v", err)
	}
	if code != "" {
		t.Errorf("合法配置应返回空错误码，实际: %s", code)
	}
}

// TestValidatePoolSubnet_20SubnetCrossSubnet /20 子网跨网段拒绝
// 验证使用按位计算而非字符串前缀判断: 172.16.32.100 与 172.16.16.1 前两字节相同但属不同 /20 子网
func TestValidatePoolSubnet_20SubnetCrossSubnet(t *testing.T) {
	adapterIP := net.ParseIP("172.16.16.1")
	mask := net.IPv4Mask(255, 255, 240, 0)
	// /20 子网范围: 172.16.16.0 - 172.16.31.255
	// 172.16.32.100 属于 172.16.32.0/20，前两字节相同但第三字节按位与结果不同
	code, err := validatePoolSubnet(adapterIP, mask, "",
		net.ParseIP("172.16.32.100"), net.ParseIP("172.16.20.200"))
	if err == nil {
		t.Error("/20 子网跨网段应被拒绝（验证按位计算而非字符串前缀）")
	}
	if code != errCodePoolSubnetMismatch {
		t.Errorf("应返回 pool_subnet_mismatch，实际: %s", code)
	}
}

// TestValidatePoolSubnet_20SubnetGatewayCrossSubnet /20 子网网关跨网段拒绝
func TestValidatePoolSubnet_20SubnetGatewayCrossSubnet(t *testing.T) {
	adapterIP := net.ParseIP("172.16.16.1")
	mask := net.IPv4Mask(255, 255, 240, 0)
	// /20 子网范围: 172.16.16.0 - 172.16.31.255
	// 172.16.32.1 属于不同 /20 子网
	code, err := validatePoolSubnet(adapterIP, mask, "172.16.32.1",
		net.ParseIP("172.16.20.100"), net.ParseIP("172.16.20.200"))
	if err == nil {
		t.Error("/20 子网网关跨网段应被拒绝")
	}
	if code != errCodeGatewaySubnetMismatch {
		t.Errorf("应返回 gateway_subnet_mismatch，实际: %s", code)
	}
}
