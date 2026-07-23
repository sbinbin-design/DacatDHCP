package dhcp

// V1.0.3新增: 固定映射(MAC-IP)在 LeaseStore 与 Server 报文链路中的行为测试
// 覆盖: AddReservedIPs 排除池内保留 IP、CreateStaticOffer/ConfirmStaticLease 绕过池校验、
// 完整 Discover/Offer/Request/ACK 命中固定映射、禁用后回退动态分配、保留 IP 不被动态分配给他人

import (
	"fmt"
	"net"
	"testing"
)

// ---- AddReservedIPs: 池内保留 IP 在动态分配时被跳过 ----

func TestAddReservedIPs_InPoolSkipped(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 将池内的 192.168.1.100 和 192.168.1.101 标记为保留
	store.AddReservedIPs([]net.IP{
		net.ParseIP("192.168.1.100"),
		net.ParseIP("192.168.1.101"),
	})

	// 连续动态分配多个 IP,均不应命中保留地址
	seen := make(map[uint32]bool)
	for i := 0; i < 20; i++ {
		ip, err := store.CreatePendingOffer(mac(fmt.Sprintf("00:11:22:33:44:%02x", i)), nil, "Client")
		if err != nil {
			t.Fatalf("第 %d 次 CreatePendingOffer 失败: %v", i, err)
		}
		v := ipToUint32(ip.To4())
		seen[v] = true
		if v == ipToUint32(net.ParseIP("192.168.1.100").To4()) || v == ipToUint32(net.ParseIP("192.168.1.101").To4()) {
			t.Errorf("动态分配不应命中保留 IP %s", ip)
		}
	}
}

// AddReservedIPs: 池外保留 IP 不影响动态分配计数
func TestAddReservedIPs_OutOfPoolNoEffect(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 192.168.1.50 不在池内,标记保留不应减少池可用数
	store.AddReservedIPs([]net.IP{net.ParseIP("192.168.1.50")})

	total, _ := store.PoolStats()
	if total != 101 {
		t.Errorf("池外保留 IP 不应影响池总量,期望 101,实际 %d", total)
	}
}

// ---- CreateStaticOffer: 池内保留 IP 可创建 Pending Offer ----

func TestCreateStaticOffer_InPool(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 192.168.1.100 在池内且已标记保留,CreateStaticOffer 应绕过 excluded 检查
	store.AddReservedIPs([]net.IP{net.ParseIP("192.168.1.100")})

	staticIP := net.ParseIP("192.168.1.100")
	ip, err := store.CreateStaticOffer(mac("00:11:22:33:44:55"), nil, "Static", staticIP)
	if err != nil {
		t.Fatalf("池内保留 IP 的 CreateStaticOffer 应成功: %v", err)
	}
	if !ip.Equal(staticIP) {
		t.Errorf("CreateStaticOffer 应返回固定 IP %s,实际 %s", staticIP, ip)
	}
}

// CreateStaticOffer: 池外 IP 也能创建 Pending Offer
func TestCreateStaticOffer_OutOfPool(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 192.168.1.50 不在池内,普通 CreatePendingOffer 会拒绝,CreateStaticOffer 应放行
	staticIP := net.ParseIP("192.168.1.50")
	ip, err := store.CreateStaticOffer(mac("00:11:22:33:44:55"), nil, "Static", staticIP)
	if err != nil {
		t.Fatalf("池外固定 IP 的 CreateStaticOffer 应成功: %v", err)
	}
	if !ip.Equal(staticIP) {
		t.Errorf("CreateStaticOffer 应返回固定 IP %s,实际 %s", staticIP, ip)
	}
}

// ---- ConfirmStaticLease: 池内保留 IP 可确认活跃租约 ----

func TestConfirmStaticLease_InPool(t *testing.T) {
	store := newTestStore(t)

	store.AddReservedIPs([]net.IP{net.ParseIP("192.168.1.100")})
	staticIP := net.ParseIP("192.168.1.100")

	lease, err := store.ConfirmStaticLease(mac("00:11:22:33:44:55"), nil, "Static", staticIP, staticIP)
	if err != nil {
		t.Fatalf("池内保留 IP 的 ConfirmStaticLease 应成功: %v", err)
	}
	if !lease.IP.Equal(staticIP) {
		t.Errorf("租约 IP 应为固定 IP %s,实际 %s", staticIP, lease.IP)
	}
	if lease.Status != LeaseActive {
		t.Errorf("租约状态应为 active,实际 %s", lease.Status)
	}
}

// ConfirmStaticLease: 池外 IP 可确认活跃租约
func TestConfirmStaticLease_OutOfPool(t *testing.T) {
	store := newTestStore(t)

	staticIP := net.ParseIP("192.168.1.50")
	lease, err := store.ConfirmStaticLease(mac("00:11:22:33:44:55"), nil, "Static", staticIP, staticIP)
	if err != nil {
		t.Fatalf("池外固定 IP 的 ConfirmStaticLease 应成功: %v", err)
	}
	if !lease.IP.Equal(staticIP) {
		t.Errorf("租约 IP 应为固定 IP %s,实际 %s", staticIP, lease.IP)
	}
}

// startFlowServerWithStatic 启动带 mock 连接和固定映射的 Server
// 固定映射必须在 Start 之前设置,Start 时复制到 runningInstance 作为不可变运行配置
func startFlowServerWithStatic(t *testing.T, gateway net.IP, dnsServers []net.IP, staticLeases []StaticLease) (*Server, *mockPacketConn, net.IP) {
	t.Helper()
	s := NewServer()
	mc := newMockPacketConn()
	s.createConnFunc = func(bindIP net.IP) (net.PacketConn, error) {
		return mc, nil
	}
	s.SetStaticLeases(staticLeases)
	adapterIP := net.ParseIP("192.168.1.1")
	if err := s.Start("TestAdapter", adapterIP, net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"), net.ParseIP("192.168.1.200"), 60, gateway, dnsServers); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
	return s, mc, adapterIP
}

// ---- 完整报文链路: 命中固定映射的客户端拿到指定 IP ----

func TestFlow_StaticLeaseMatch_GetsFixedIP(t *testing.T) {
	// 池 192.168.1.100-200, 注入固定映射 00:11:22:33:44:55 -> 192.168.1.150
	fixedIP := net.ParseIP("192.168.1.150")
	s, mc, adapterIP := startFlowServerWithStatic(t, nil, nil, []StaticLease{
		{MAC: mac("00:11:22:33:44:55"), IP: fixedIP},
	})

	// Discover → Offer(应为固定 IP 192.168.1.150)
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER,实际 %d", len(written))
	}
	offer, err := ParsePacket(written[0])
	if err != nil {
		t.Fatalf("解析 OFFER 失败: %v", err)
	}
	if !offer.YIAddr.Equal(fixedIP) {
		t.Errorf("命中固定映射时 OFFER 应为 %s,实际 %s", fixedIP, offer.YIAddr)
	}

	// Request(请求固定 IP) → ACK
	s.handlePacket(buildFlowRequestPacket(fixedIP, adapterIP))
	written = mc.getWritten()
	if len(written) != 2 {
		t.Fatalf("期望 2 个响应,实际 %d", len(written))
	}
	ack, err := ParsePacket(written[1])
	if err != nil {
		t.Fatalf("解析 ACK 失败: %v", err)
	}
	if mt, ok := ack.MessageType(); !ok || mt != MsgTypeACK {
		t.Errorf("应为 ACK,实际类型 %d", mt)
	}
	if !ack.YIAddr.Equal(fixedIP) {
		t.Errorf("ACK 的 YIAddr 应为固定 IP %s,实际 %s", fixedIP, ack.YIAddr)
	}
}

// 固定映射指向池外 IP 时,命中客户端仍能拿到该 IP
func TestFlow_StaticLeaseMatch_OutOfPoolIP(t *testing.T) {
	// 192.168.1.50 不在池 100-200 内,但固定映射允许池外
	fixedIP := net.ParseIP("192.168.1.50")
	s, mc, adapterIP := startFlowServerWithStatic(t, nil, nil, []StaticLease{
		{MAC: mac("00:11:22:33:44:55"), IP: fixedIP},
	})

	// Discover → Offer(应为池外固定 IP)
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER,实际 %d", len(written))
	}
	offer, _ := ParsePacket(written[0])
	if !offer.YIAddr.Equal(fixedIP) {
		t.Errorf("池外固定映射 OFFER 应为 %s,实际 %s", fixedIP, offer.YIAddr)
	}

	// Request → ACK
	s.handlePacket(buildFlowRequestPacket(fixedIP, adapterIP))
	written = mc.getWritten()
	ack, _ := ParsePacket(written[1])
	if mt, ok := ack.MessageType(); !ok || mt != MsgTypeACK {
		t.Errorf("池外固定映射应为 ACK,实际 %d", mt)
	}
}

// ---- 池内保留 IP 不被其他客户端动态获取 ----

func TestFlow_ReservedIP_NotGivenToOthers(t *testing.T) {
	// 固定映射占用池内 192.168.1.150,其他客户端动态分配不应拿到该 IP
	// 必须在 Start 前设置,Start 时 AddReservedIPs 将其加入排除集合
	s, mc, adapterIP := startFlowServerWithStatic(t, nil, nil, []StaticLease{
		{MAC: mac("00:11:22:33:44:55"), IP: net.ParseIP("192.168.1.150")},
	})

	// 另一个客户端(MAC 不同)Discover
	otherDiscover := buildFlowDiscoverPacket()
	// 改 MAC 为 00:11:22:33:44:66
	otherMAC := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x66}
	copy(otherDiscover[28:34], otherMAC)
	s.handlePacket(otherDiscover)

	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER,实际 %d", len(written))
	}
	offer, _ := ParsePacket(written[0])
	if offer.YIAddr.Equal(net.ParseIP("192.168.1.150")) {
		t.Error("其他客户端动态分配不应命中保留 IP 192.168.1.150")
	}
	_ = adapterIP
}

// ---- 无固定映射时回退动态分配(禁用映射的等效场景) ----

func TestFlow_NoStaticLease_FallbackDynamic(t *testing.T) {
	s, mc, adapterIP := startFlowServer(t, nil, nil)
	// 不注入任何固定映射,客户端走动态分配
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER,实际 %d", len(written))
	}
	offer, _ := ParsePacket(written[0])
	// 动态分配的 IP 应在池 100-200 范围内
	v := ipToUint32(offer.YIAddr.To4())
	if v < ipToUint32(net.ParseIP("192.168.1.100").To4()) || v > ipToUint32(net.ParseIP("192.168.1.200").To4()) {
		t.Errorf("动态分配 IP %s 不在地址池范围内", offer.YIAddr)
	}
	_ = adapterIP
}

// ---- V1.0.3修复回归: PoolStats used 不计入池外固定映射租约 ----

func TestPoolStats_OutOfPoolLease_NotCountedInUsed(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 在池外 192.168.1.50 创建一个固定映射租约
	outIP := net.ParseIP("192.168.1.50")
	_, err := store.ConfirmStaticLease(mac("00:11:22:33:44:55"), nil, "Static", outIP, outIP)
	if err != nil {
		t.Fatalf("创建池外固定租约失败: %v", err)
	}

	total, used := store.PoolStats()
	// 池外租约不应计入 used
	if used != 0 {
		t.Errorf("池外固定映射租约不应计入 used,期望 0,实际 %d", used)
	}
	// total 仍为池内总量 101
	if total != 101 {
		t.Errorf("池外租约不应影响 total,期望 101,实际 %d", total)
	}
}

// V1.0.3修复回归: 池内保留 IP 的固定映射租约不计入 used(其 IP 在 excluded 集合)
func TestPoolStats_InPoolReservedLease_NotCountedInUsed(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 在池内 192.168.1.100 创建一个固定映射租约,该 IP 已通过 AddReservedIPs 加入 excluded
	inIP := net.ParseIP("192.168.1.100")
	store.AddReservedIPs([]net.IP{inIP})
	_, err := store.ConfirmStaticLease(mac("00:11:22:33:44:55"), nil, "Static", inIP, inIP)
	if err != nil {
		t.Fatalf("创建池内固定租约失败: %v", err)
	}

	total, used := store.PoolStats()
	// 池内保留 IP 租约不计入 used(IP 在 excluded 集合中)
	if used != 0 {
		t.Errorf("池内保留 IP 的固定映射租约不应计入 used,期望 0,实际 %d", used)
	}
	// total 应扣除池内保留 IP(101-1=100)
	if total != 100 {
		t.Errorf("池内保留 IP 应从 total 扣除,期望 100,实际 %d", total)
	}
}

// V1.0.3修复回归: 池内非保留 IP 的动态租约正常计入 used
func TestPoolStats_InPoolDynamicLease_CountedInUsed(t *testing.T) {
	store := newTestStore(t) // 池 192.168.1.100-200

	// 在池内 192.168.1.101 创建一个普通动态租约(非保留 IP,不在 excluded)
	dynIP := net.ParseIP("192.168.1.101")
	_, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "Dynamic")
	if err != nil {
		t.Fatalf("创建动态 Pending Offer 失败: %v", err)
	}

	_, used := store.PoolStats()
	// 池内非保留 IP 的动态 Pending Offer 应计入 used
	if used != 1 {
		t.Errorf("池内动态 Pending Offer 应计入 used,期望 1,实际 %d", used)
	}
	_ = dynIP
}

// ---- V1.0.3修复回归: 池外固定映射命中时,绑定 MAC 拿到 IP 且 used 不增加 ----

func TestFlow_OutOfPoolStatic_UsedNotIncrease(t *testing.T) {
	// 池 192.168.1.100-200, 固定映射 00:11:22:33:44:55 -> 192.168.1.50(池外)
	fixedIP := net.ParseIP("192.168.1.50")
	s, mc, adapterIP := startFlowServerWithStatic(t, nil, nil, []StaticLease{
		{MAC: mac("00:11:22:33:44:55"), IP: fixedIP},
	})

	// 启动时 used 应为 0
	st := s.Status()
	if st.PoolUsed != 0 {
		t.Fatalf("启动时 used 应为 0,实际 %d", st.PoolUsed)
	}

	// Discover → Offer(池外固定 IP)
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	if len(written) != 1 {
		t.Fatalf("期望 1 个 OFFER,实际 %d", len(written))
	}
	offer, _ := ParsePacket(written[0])
	if !offer.YIAddr.Equal(fixedIP) {
		t.Errorf("OFFER 应为池外固定 IP %s,实际 %s", fixedIP, offer.YIAddr)
	}
	// Pending Offer 在池外,used 仍应为 0
	st = s.Status()
	if st.PoolUsed != 0 {
		t.Errorf("池外 Pending Offer 不应增加 used,期望 0,实际 %d", st.PoolUsed)
	}

	// Request → ACK
	s.handlePacket(buildFlowRequestPacket(fixedIP, adapterIP))
	written = mc.getWritten()
	ack, _ := ParsePacket(written[1])
	if mt, ok := ack.MessageType(); !ok || mt != MsgTypeACK {
		t.Errorf("应为 ACK,实际 %d", mt)
	}
	// ACK 后形成活跃租约,但 IP 在池外,used 仍应为 0
	st = s.Status()
	if st.PoolUsed != 0 {
		t.Errorf("池外活跃租约不应增加 used,期望 0,实际 %d", st.PoolUsed)
	}
}

// ---- V1.0.3修复回归: 池内固定映射命中时,绑定 MAC 拿到 IP,租约列表显示但 used 不增加 ----

func TestFlow_InPoolStatic_LeaseShownButUsedNotIncrease(t *testing.T) {
	// 池 192.168.1.100-200, 固定映射 00:11:22:33:44:55 -> 192.168.1.150(池内)
	fixedIP := net.ParseIP("192.168.1.150")
	s, mc, adapterIP := startFlowServerWithStatic(t, nil, nil, []StaticLease{
		{MAC: mac("00:11:22:33:44:55"), IP: fixedIP},
	})

	// 启动时 used 应为 0,total 应扣除池内保留 IP(101-1=100)
	st := s.Status()
	if st.PoolUsed != 0 {
		t.Fatalf("启动时 used 应为 0,实际 %d", st.PoolUsed)
	}
	if st.PoolTotal != 100 {
		t.Fatalf("启动时 total 应为 100(池内保留 IP 扣除),实际 %d", st.PoolTotal)
	}

	// Discover → Offer(池内固定 IP)
	s.handlePacket(buildFlowDiscoverPacket())
	written := mc.getWritten()
	offer, _ := ParsePacket(written[0])
	if !offer.YIAddr.Equal(fixedIP) {
		t.Errorf("OFFER 应为池内固定 IP %s,实际 %s", fixedIP, offer.YIAddr)
	}
	// 池内保留 IP 的 Pending Offer 不计入 used
	st = s.Status()
	if st.PoolUsed != 0 {
		t.Errorf("池内保留 IP 的 Pending Offer 不应增加 used,期望 0,实际 %d", st.PoolUsed)
	}

	// Request → ACK
	s.handlePacket(buildFlowRequestPacket(fixedIP, adapterIP))
	written = mc.getWritten()
	ack, _ := ParsePacket(written[1])
	if mt, ok := ack.MessageType(); !ok || mt != MsgTypeACK {
		t.Errorf("应为 ACK,实际 %d", mt)
	}
	// ACK 后形成活跃租约,租约列表应显示该租约
	leases := s.Leases()
	found := false
	for _, l := range leases {
		if l.IP == fixedIP.String() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("租约列表应包含池内固定映射租约 %s", fixedIP)
	}
	// 但池内保留 IP 的活跃租约不计入 used
	st = s.Status()
	if st.PoolUsed != 0 {
		t.Errorf("池内保留 IP 的活跃租约不应增加 used,期望 0,实际 %d", st.PoolUsed)
	}
}
