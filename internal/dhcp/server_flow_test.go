package dhcp

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// V2.1新增: 真实 Server 报文链路测试
// 使用 mock PacketConn 捕获 WriteTo 响应，验证 Discover/Offer、Request/ACK、NAK 完整链路
// 所有测试调用真实生产函数 handlePacket / BuildPacket / ParsePacket，禁止复制实现

// mockTimeout 实现 net.Error 接口，Timeout()=true（serve 主循环据此 continue）
type mockTimeout struct{}

func (mockTimeout) Error() string   { return "timeout" }
func (mockTimeout) Timeout() bool   { return true }
func (mockTimeout) Temporary() bool { return true }

// mockPacketConn 捕获 WriteTo 响应字节的测试用连接
type mockPacketConn struct {
	mu      sync.Mutex
	written [][]byte
	closeCh chan struct{}
	closed  bool
}

func newMockPacketConn() *mockPacketConn {
	return &mockPacketConn{closeCh: make(chan struct{})}
}

func (m *mockPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-m.closeCh:
		return 0, &net.UDPAddr{}, &net.OpError{Op: "read", Err: fmt.Errorf("connection closed")}
	case <-time.After(2 * time.Second):
		return 0, &net.UDPAddr{}, mockTimeout{}
	}
}

func (m *mockPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, &net.OpError{Op: "write", Err: fmt.Errorf("connection closed")}
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	m.written = append(m.written, cp)
	return len(p), nil
}

func (m *mockPacketConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.closeCh)
	}
	return nil
}

func (m *mockPacketConn) getWritten() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([][]byte, len(m.written))
	copy(result, m.written)
	return result
}

func (m *mockPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 67}
}
func (m *mockPacketConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// buildFlowDiscoverPacket 构建 Discover 报文（广播标志，无 CIAddr）
func buildFlowDiscoverPacket() []byte {
	buf := make([]byte, 576)
	for i := range buf {
		buf[i] = 0
	}
	buf[0] = OpRequest
	buf[1] = HwTypeEthernet
	buf[2] = 6
	binary.BigEndian.PutUint32(buf[4:8], 0x12345678) // XID
	binary.BigEndian.PutUint16(buf[10:12], 0x8000)   // broadcast flag
	mac := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	copy(buf[28:28+len(mac)], mac)
	binary.BigEndian.PutUint32(buf[236:240], MagicCookie)
	offset := 240
	buf[offset] = OptMsgType
	offset++
	buf[offset] = 1
	offset++
	buf[offset] = MsgTypeDiscover
	offset++
	buf[offset] = OptEnd
	return buf[:offset+1]
}

// buildFlowRequestPacket 构建 Request 报文（含 RequestedIP 和可选 ServerID）
func buildFlowRequestPacket(requestedIP, serverID net.IP) []byte {
	buf := make([]byte, 576)
	for i := range buf {
		buf[i] = 0
	}
	buf[0] = OpRequest
	buf[1] = HwTypeEthernet
	buf[2] = 6
	binary.BigEndian.PutUint32(buf[4:8], 0x12345678) // XID
	binary.BigEndian.PutUint16(buf[10:12], 0x8000)   // broadcast flag
	mac := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	copy(buf[28:28+len(mac)], mac)
	binary.BigEndian.PutUint32(buf[236:240], MagicCookie)
	offset := 240
	// Option 53: Message Type = Request
	buf[offset] = OptMsgType
	offset++
	buf[offset] = 1
	offset++
	buf[offset] = MsgTypeRequest
	offset++
	// Option 50: Requested IP
	if requestedIP != nil {
		buf[offset] = OptRequestedIP
		offset++
		buf[offset] = 4
		offset++
		copy(buf[offset:offset+4], requestedIP.To4())
		offset += 4
	}
	// Option 54: Server ID
	if serverID != nil {
		buf[offset] = OptServerID
		offset++
		buf[offset] = 4
		offset++
		copy(buf[offset:offset+4], serverID.To4())
		offset += 4
	}
	buf[offset] = OptEnd
	return buf[:offset+1]
}

// startFlowServer 启动带 mock 连接的 Server，返回 Server、mock 连接和适配器 IP
func startFlowServer(t *testing.T, gateway net.IP, dnsServers []net.IP) (*Server, *mockPacketConn, net.IP) {
	t.Helper()
	s := NewServer()
	mc := newMockPacketConn()
	s.createConnFunc = func(bindIP net.IP) (net.PacketConn, error) {
		return mc, nil
	}
	adapterIP := net.ParseIP("192.168.1.1")
	if err := s.Start("TestAdapter", adapterIP, net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"), 60, gateway, dnsServers); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() {
		s.Stop()
	})
	return s, mc, adapterIP
}

// TestFlow_OfferACK_WithGatewayDNS 带 网关和 DNS 的真实 Discover/Offer、Request/ACK 链路
func TestFlow_OfferACK_WithGatewayDNS(t *testing.T) {
	gw := net.ParseIP("192.168.1.1")
	dns := []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("8.8.4.4")}
	s, mc, adapterIP := startFlowServer(t, gw, dns)

	// 1. Discover → Offer
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER 响应，实际: %d", len(written))
	}
	offer, err := ParsePacket(written[0])
	if err != nil {
		t.Fatalf("解析 OFFER 失败: %v", err)
	}
	if mt, ok := offer.MessageType(); !ok || mt != MsgTypeOffer {
		t.Fatalf("OFFER 报文类型错误: %d (ok=%v)", mt, ok)
	}
	offeredIP := offer.YIAddr
	if offeredIP == nil || offeredIP.IsUnspecified() {
		t.Fatal("OFFER 的 YIAddr 不应为空")
	}
	// 验证 OFFER 包含 Option 3 (Router) = 192.168.1.1
	routerVal, ok := offer.Options[OptRouter]
	if !ok {
		t.Error("OFFER 应包含 Option 3 (Router)")
	} else if !net.IP(routerVal).Equal(gw.To4()) {
		t.Errorf("OFFER Option 3 应为 %s，实际: %s", gw, net.IP(routerVal))
	}
	// 验证 OFFER 包含 Option 6 (DNS) = 8.8.8.8, 8.8.4.4（顺序正确）
	dnsVal, ok := offer.Options[OptDNSServer]
	if !ok {
		t.Error("OFFER 应包含 Option 6 (DNS)")
	} else {
		if len(dnsVal) != 8 {
			t.Errorf("OFFER Option 6 长度应为 8（2个DNS），实际: %d", len(dnsVal))
		}
		expected := []byte{8, 8, 8, 8, 8, 8, 4, 4}
		for i, b := range expected {
			if dnsVal[i] != b {
				t.Errorf("OFFER Option 6 第 %d 字节应为 %d，实际 %d", i, b, dnsVal[i])
			}
		}
	}

	// 2. Request → ACK（使用 OFFER 分配的 IP 和适配器 IP 作为 ServerID）
	s.handlePacket(buildFlowRequestPacket(offeredIP, adapterIP))
	written = mc.getWritten()
	if len(written) != 2 {
		t.Fatalf("期望 2 个响应（OFFER+ACK），实际: %d", len(written))
	}
	ack, err := ParsePacket(written[1])
	if err != nil {
		t.Fatalf("解析 ACK 失败: %v", err)
	}
	if mt, ok := ack.MessageType(); !ok || mt != MsgTypeACK {
		t.Fatalf("ACK 报文类型错误: %d (ok=%v)", mt, ok)
	}
	// 验证 ACK 包含正确的网关
	if routerVal, ok := ack.Options[OptRouter]; !ok {
		t.Error("ACK 应包含 Option 3 (Router)")
	} else if !net.IP(routerVal).Equal(gw.To4()) {
		t.Errorf("ACK Option 3 应为 %s，实际: %s", gw, net.IP(routerVal))
	}
	// 验证 ACK 包含正确的 DNS（顺序正确）
	if dnsVal, ok := ack.Options[OptDNSServer]; !ok {
		t.Error("ACK 应包含 Option 6 (DNS)")
	} else {
		expected := []byte{8, 8, 8, 8, 8, 8, 4, 4}
		for i, b := range expected {
			if dnsVal[i] != b {
				t.Errorf("ACK Option 6 第 %d 字节应为 %d，实际 %d", i, b, dnsVal[i])
			}
		}
	}
}

// TestFlow_EmptyConfig_NoRouterDNS 空配置（无网关无 DNS）时 OFFER 和 ACK 不含 Option 3/6
func TestFlow_EmptyConfig_NoRouterDNS(t *testing.T) {
	s, mc, adapterIP := startFlowServer(t, nil, nil)

	// Discover → Offer
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER 响应，实际: %d", len(written))
	}
	offer, err := ParsePacket(written[0])
	if err != nil {
		t.Fatalf("解析 OFFER 失败: %v", err)
	}
	if _, ok := offer.Options[OptRouter]; ok {
		t.Error("空配置 OFFER 不应包含 Option 3 (Router)")
	}
	if _, ok := offer.Options[OptDNSServer]; ok {
		t.Error("空配置 OFFER 不应包含 Option 6 (DNS)")
	}

	// Request → ACK
	offeredIP := offer.YIAddr
	s.handlePacket(buildFlowRequestPacket(offeredIP, adapterIP))
	written = mc.getWritten()
	if len(written) != 2 {
		t.Fatalf("期望 2 个响应，实际: %d", len(written))
	}
	ack, err := ParsePacket(written[1])
	if err != nil {
		t.Fatalf("解析 ACK 失败: %v", err)
	}
	if _, ok := ack.Options[OptRouter]; ok {
		t.Error("空配置 ACK 不应包含 Option 3 (Router)")
	}
	if _, ok := ack.Options[OptDNSServer]; ok {
		t.Error("空配置 ACK 不应包含 Option 6 (DNS)")
	}
}

// TestFlow_NAK_NoRouterDNS NAK 不包含网关和 DNS
func TestFlow_NAK_NoRouterDNS(t *testing.T) {
	gw := net.ParseIP("192.168.1.1")
	dns := []net.IP{net.ParseIP("8.8.8.8")}
	s, mc, adapterIP := startFlowServer(t, gw, dns)

	// 先发送 Discover 创建 Pending Offer
	s.handlePacket(buildFlowDiscoverPacket())

	// 发送 Request 请求一个不在地址池内的 IP → ConfirmLease 失败 → NAK
	badIP := net.ParseIP("192.168.1.50") // 不在地址池 100-200 内
	s.handlePacket(buildFlowRequestPacket(badIP, adapterIP))

	written := mc.getWritten()
	// 第一条是 OFFER，第二条应为 NAK
	nakFound := false
	for i, w := range written {
		parsed, err := ParsePacket(w)
		if err != nil {
			continue
		}
		if mt, ok := parsed.MessageType(); ok && mt == MsgTypeNAK {
			nakFound = true
			if _, ok := parsed.Options[OptRouter]; ok {
				t.Error("NAK 不应包含 Option 3 (Router)")
			}
			if _, ok := parsed.Options[OptDNSServer]; ok {
				t.Error("NAK 不应包含 Option 6 (DNS)")
			}
			if _, ok := parsed.Options[OptLeaseTime]; ok {
				t.Error("NAK 不应包含 Option 51 (Lease Time)")
			}
			// NAK 应在第二个位置（OFFER 之后）
			if i != 1 {
				t.Errorf("NAK 应为第 2 个响应，实际为第 %d 个", i+1)
			}
		}
	}
	if !nakFound {
		// 输出所有响应类型用于调试
		for i, w := range written {
			parsed, _ := ParsePacket(w)
			mt, _ := parsed.MessageType()
			t.Logf("响应 %d: 类型=%d", i, mt)
		}
		t.Fatal("未找到 NAK 响应")
	}
}

// TestFlow_SingleDNS_OnlyOption6 仅填写一个 DNS 时只下发 Option 6
func TestFlow_SingleDNS_OnlyOption6(t *testing.T) {
	dns := []net.IP{net.ParseIP("8.8.8.8")}
	s, mc, _ := startFlowServer(t, nil, dns) // 无网关，仅 1 个 DNS

	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER 响应，实际: %d", len(written))
	}
	offer, err := ParsePacket(written[0])
	if err != nil {
		t.Fatalf("解析 OFFER 失败: %v", err)
	}
	// 无网关 → 不含 Option 3
	if _, ok := offer.Options[OptRouter]; ok {
		t.Error("无网关时 OFFER 不应包含 Option 3")
	}
	// 有 1 个 DNS → 含 Option 6，长度 4
	dnsVal, ok := offer.Options[OptDNSServer]
	if !ok {
		t.Fatal("有 DNS 时 OFFER 应包含 Option 6")
	}
	if len(dnsVal) != 4 {
		t.Errorf("单个 DNS Option 6 长度应为 4，实际: %d", len(dnsVal))
	}
	expected := []byte{8, 8, 8, 8}
	for i, b := range expected {
		if dnsVal[i] != b {
			t.Errorf("DNS 第 %d 字节应为 %d，实际 %d", i, b, dnsVal[i])
		}
	}
}

// TestFlow_OfferedIPInPool OFFER 分配的 IP 必须在地址池范围内
func TestFlow_OfferedIPInPool(t *testing.T) {
	s, mc, _ := startFlowServer(t, nil, nil)

	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER 响应，实际: %d", len(written))
	}
	offer, _ := ParsePacket(written[0])
	offeredIP := offer.YIAddr.To4()
	if offeredIP == nil {
		t.Fatal("OFFER 的 YIAddr 无效")
	}
	// 地址池 192.168.1.100 - 192.168.1.200
	poolStart := binary.BigEndian.Uint32(net.ParseIP("192.168.1.100").To4())
	poolEnd := binary.BigEndian.Uint32(net.ParseIP("192.168.1.200").To4())
	offeredVal := binary.BigEndian.Uint32(offeredIP)
	if offeredVal < poolStart || offeredVal > poolEnd {
		t.Errorf("OFFER 分配的 IP %s 不在地址池范围内", offeredIP)
	}
}

// TestFlow_WrongServerID_Ignored Request 中 ServerID 不匹配适配器 IP 时静默忽略
func TestFlow_WrongServerID_Ignored(t *testing.T) {
	s, mc, _ := startFlowServer(t, nil, nil)

	// 先 Discover 创建 Pending Offer
	s.handlePacket(buildFlowDiscoverPacket())
	offerCount := len(mc.getWritten())

	// 发送 ServerID 不匹配的 Request（不应产生任何响应）
	wrongServerID := net.ParseIP("192.168.1.99")
	s.handlePacket(buildFlowRequestPacket(net.ParseIP("192.168.1.100"), wrongServerID))

	written := mc.getWritten()
	if len(written) != offerCount {
		t.Errorf("ServerID 不匹配时不应产生新响应，期望 %d，实际: %d", offerCount, len(written))
	}
}

// TestFlow_ReasonInLog 确保处理链路日志无异常（简单冒烟检查）
func TestFlow_ReasonInLog(t *testing.T) {
	var logs []string
	s := NewServer()
	mc := newMockPacketConn()
	s.createConnFunc = func(bindIP net.IP) (net.PacketConn, error) {
		return mc, nil
	}
	s.logFunc = func(format string, args ...interface{}) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	if err := s.Start("TestAdapter", net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"), 60, nil, nil); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { s.Stop() })

	s.handlePacket(buildFlowDiscoverPacket())

	// 验证日志包含 OFFER
	foundOffer := false
	for _, log := range logs {
		if strings.Contains(log, "OFFER") {
			foundOffer = true
			break
		}
	}
	if !foundOffer {
		t.Error("日志中应包含 OFFER 记录")
	}
}
