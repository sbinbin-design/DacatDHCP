package dhcp

import (
	"net"
	"testing"
)

// V2新增: 网关(Option 3)和DNS(Option 6)可选下发测试
// 所有测试调用真实生产函数 BuildPacket + ParsePacket，禁止复制实现

// 辅助函数：构建 ACK 报文并解析，返回解析后的选项
func buildAndParse(t *testing.T, msgType byte, opts ReplyOptions) *Packet {
	t.Helper()
	pkt := BuildPacket(
		msgType,
		0x12345678,
		0x0000,
		nil,
		net.ParseIP("192.168.1.100"),
		net.ParseIP("192.168.1.1"),
		mac("00:11:22:33:44:55"),
		opts,
	)
	parsed, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析 BuildPacket 输出失败: %v", err)
	}
	return parsed
}

// TestReplyOptions_Empty_NoRouterDNS 网关和DNS均为空时不下发 Option 3/6
func TestReplyOptions_Empty_NoRouterDNS(t *testing.T) {
	parsed := buildAndParse(t, MsgTypeACK, ReplyOptions{
		LeaseTime:  3600,
		SubnetMask: net.IPv4Mask(255, 255, 255, 0),
	})
	if _, ok := parsed.Options[OptRouter]; ok {
		t.Error("空网关不应包含 Option 3 (Router)")
	}
	if _, ok := parsed.Options[OptDNSServer]; ok {
		t.Error("空 DNS 不应包含 Option 6 (DNS)")
	}
}

// TestReplyOptions_OnlyRouter 仅填写网关时只存在 Option 3，不含 Option 6
func TestReplyOptions_OnlyRouter(t *testing.T) {
	parsed := buildAndParse(t, MsgTypeACK, ReplyOptions{
		LeaseTime:  3600,
		SubnetMask: net.IPv4Mask(255, 255, 255, 0),
		Router:     net.ParseIP("192.168.1.1"),
		DNSServers: nil,
	})
	routerVal, ok := parsed.Options[OptRouter]
	if !ok {
		t.Fatal("填写网关应包含 Option 3 (Router)")
	}
	expected := net.ParseIP("192.168.1.1").To4()
	if !net.IP(routerVal).Equal(expected) {
		t.Errorf("Option 3 值应为 192.168.1.1，实际: %s", net.IP(routerVal))
	}
	if _, ok := parsed.Options[OptDNSServer]; ok {
		t.Error("空 DNS 不应包含 Option 6")
	}
}

// TestReplyOptions_OnlyOneDNS 仅填写一个 DNS 时只存在 Option 6，不含 Option 3
func TestReplyOptions_OnlyOneDNS(t *testing.T) {
	parsed := buildAndParse(t, MsgTypeACK, ReplyOptions{
		LeaseTime:  3600,
		SubnetMask: net.IPv4Mask(255, 255, 255, 0),
		Router:     nil,
		DNSServers: []net.IP{net.ParseIP("8.8.8.8")},
	})
	dnsVal, ok := parsed.Options[OptDNSServer]
	if !ok {
		t.Fatal("填写 DNS 应包含 Option 6")
	}
	if len(dnsVal) != 4 {
		t.Errorf("单个 DNS Option 6 长度应为 4，实际: %d", len(dnsVal))
	}
	expected := net.ParseIP("8.8.8.8").To4()
	if !net.IP(dnsVal).Equal(expected) {
		t.Errorf("Option 6 值应为 8.8.8.8，实际: %s", net.IP(dnsVal))
	}
	if _, ok := parsed.Options[OptRouter]; ok {
		t.Error("空网关不应包含 Option 3")
	}
}

// TestReplyOptions_MultipleDNS 多个 DNS 编码顺序和长度正确
func TestReplyOptions_MultipleDNS(t *testing.T) {
	dnsList := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("8.8.4.4"),
		net.ParseIP("1.1.1.1"),
	}
	parsed := buildAndParse(t, MsgTypeACK, ReplyOptions{
		LeaseTime:  3600,
		SubnetMask: net.IPv4Mask(255, 255, 255, 0),
		DNSServers: dnsList,
	})
	dnsVal, ok := parsed.Options[OptDNSServer]
	if !ok {
		t.Fatal("填写多个 DNS 应包含 Option 6")
	}
	// 3 个 DNS = 12 字节
	if len(dnsVal) != 12 {
		t.Fatalf("3 个 DNS Option 6 长度应为 12，实际: %d", len(dnsVal))
	}
	// 验证顺序：8.8.8.8 | 8.8.4.4 | 1.1.1.1
	expected := []byte{8, 8, 8, 8, 8, 8, 4, 4, 1, 1, 1, 1}
	for i, b := range expected {
		if dnsVal[i] != b {
			t.Errorf("Option 6 第 %d 字节应为 %d，实际 %d", i, b, dnsVal[i])
		}
	}
}

// TestReplyOptions_OfferContainsGatewayDNS OFFER 报文包含正确网关和 DNS
func TestReplyOptions_OfferContainsGatewayDNS(t *testing.T) {
	parsed := buildAndParse(t, MsgTypeOffer, ReplyOptions{
		LeaseTime:  3600,
		SubnetMask: net.IPv4Mask(255, 255, 255, 0),
		Router:     net.ParseIP("192.168.1.1"),
		DNSServers: []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("8.8.4.4")},
	})
	if _, ok := parsed.Options[OptRouter]; !ok {
		t.Error("OFFER 应包含 Option 3 (Router)")
	}
	if _, ok := parsed.Options[OptDNSServer]; !ok {
		t.Error("OFFER 应包含 Option 6 (DNS)")
	}
	// 验证报文类型为 OFFER
	mt, ok := parsed.MessageType()
	if !ok || mt != MsgTypeOffer {
		t.Errorf("报文类型应为 OFFER(%d)，实际: %d (ok=%v)", MsgTypeOffer, mt, ok)
	}
}

// TestReplyOptions_ACKContainsGatewayDNS ACK 报文包含正确网关和 DNS
func TestReplyOptions_ACKContainsGatewayDNS(t *testing.T) {
	parsed := buildAndParse(t, MsgTypeACK, ReplyOptions{
		LeaseTime:  3600,
		SubnetMask: net.IPv4Mask(255, 255, 255, 0),
		Router:     net.ParseIP("192.168.1.1"),
		DNSServers: []net.IP{net.ParseIP("8.8.8.8")},
	})
	if _, ok := parsed.Options[OptRouter]; !ok {
		t.Error("ACK 应包含 Option 3 (Router)")
	}
	if _, ok := parsed.Options[OptDNSServer]; !ok {
		t.Error("ACK 应包含 Option 6 (DNS)")
	}
	mt, ok := parsed.MessageType()
	if !ok || mt != MsgTypeACK {
		t.Errorf("报文类型应为 ACK(%d)，实际: %d (ok=%v)", MsgTypeACK, mt, ok)
	}
}

// TestReplyOptions_NAK_NoRouterDNS NAK 不包含网关、DNS、租约时间
func TestReplyOptions_NAK_NoRouterDNS(t *testing.T) {
	// NAK 使用空 ReplyOptions（生产逻辑：sendNAK 传入零值）
	parsed := buildAndParse(t, MsgTypeNAK, ReplyOptions{})
	if _, ok := parsed.Options[OptRouter]; ok {
		t.Error("NAK 不应包含 Option 3 (Router)")
	}
	if _, ok := parsed.Options[OptDNSServer]; ok {
		t.Error("NAK 不应包含 Option 6 (DNS)")
	}
	if _, ok := parsed.Options[OptLeaseTime]; ok {
		t.Error("NAK 不应包含 Option 51 (Lease Time)")
	}
	if _, ok := parsed.Options[OptSubnetMask]; ok {
		t.Error("NAK 不应包含 Option 1 (Subnet Mask)")
	}
	if mt, ok := parsed.MessageType(); !ok || mt != MsgTypeNAK {
		t.Errorf("报文类型应为 NAK(%d)，实际: %d (ok=%v)", MsgTypeNAK, mt, ok)
	}
}

// TestReplyOptions_NAKSiaddrZero NAK 报文 siaddr 保持 0.0.0.0
func TestReplyOptions_NAKSiaddrZero(t *testing.T) {
	pkt := BuildPacket(
		MsgTypeNAK,
		0x12345678,
		0x8000,
		nil,
		nil,
		net.ParseIP("192.168.1.1"),
		mac("00:11:22:33:44:55"),
		ReplyOptions{},
	)
	siaddr := pkt[20:24]
	for _, b := range siaddr {
		if b != 0 {
			t.Errorf("NAK 的 siaddr 应为 0.0.0.0，实际: %d.%d.%d.%d", siaddr[0], siaddr[1], siaddr[2], siaddr[3])
			break
		}
	}
}
