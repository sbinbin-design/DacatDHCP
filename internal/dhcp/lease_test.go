package dhcp

import (
	"net"
	"testing"
	"time"
)

// 辅助函数：创建测试用的 LeaseStore
func newTestStore(t *testing.T) *LeaseStore {
	t.Helper()
	// 地址池: 192.168.1.100 - 192.168.1.200
	// 服务端 IP: 192.168.1.1（不在池内）
	// 子网掩码: 255.255.255.0
	poolStart := net.ParseIP("192.168.1.100")
	poolEnd := net.ParseIP("192.168.1.200")
	serverIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)

	store, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err != nil {
		t.Fatalf("创建 LeaseStore 失败: %v", err)
	}
	return store
}

// 辅助函数：创建 MAC 地址
func mac(s string) net.HardwareAddr {
	m, err := net.ParseMAC(s)
	if err != nil {
		panic(err)
	}
	return m
}

// ---- 测试 1: 请求已占用 IP 不得 ACK 其他 IP ----

func TestConfirmLease_OccupiedIP_ReturnNAK(t *testing.T) {
	store := newTestStore(t)

	// 客户端 A: DISCOVER → Pending Offer → REQUEST → ACK（获得 192.168.1.100）
	ipA, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "ClientA")
	if err != nil {
		t.Fatalf("ClientA CreatePendingOffer 失败: %v", err)
	}

	leaseA, err := store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "ClientA", ipA)
	if err != nil {
		t.Fatalf("ClientA ConfirmLease 失败: %v", err)
	}

	// 客户端 B: 尝试请求客户端 A 已占用的 IP → 应返回错误（NAK）
	_, err = store.ConfirmLease(mac("00:11:22:33:44:66"), nil, "ClientB", leaseA.IP)
	if err != ErrIPNotAvailable {
		t.Errorf("请求已占用 IP 应返回 ErrIPNotAvailable，实际: %v", err)
	}
}

// ---- 测试 2: Pending Offer 转租约及超时释放 ----

func TestPendingOffer_ConvertToLease(t *testing.T) {
	store := newTestStore(t)

	// DISCOVER → Pending Offer
	offeredIP, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "TestClient")
	if err != nil {
		t.Fatalf("CreatePendingOffer 失败: %v", err)
	}

	// 验证 Pending Offer 存在（不在活跃租约列表中）
	leases := store.List()
	if len(leases) != 0 {
		t.Errorf("DISCOVER 后不应有活跃租约，实际: %d", len(leases))
	}

	// REQUEST → ConfirmLease → Active Lease
	lease, err := store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "TestClient", offeredIP)
	if err != nil {
		t.Fatalf("ConfirmLease 失败: %v", err)
	}
	if !lease.IP.Equal(offeredIP) {
		t.Errorf("ConfirmLease 返回的 IP 不匹配，期望 %s，实际 %s", offeredIP, lease.IP)
	}
	if lease.Status != LeaseActive {
		t.Errorf("租约状态应为 active，实际: %s", lease.Status)
	}

	// 验证活跃租约存在
	leases = store.List()
	if len(leases) != 1 {
		t.Errorf("ConfirmLease 后应有 1 个活跃租约，实际: %d", len(leases))
	}
}

func TestPendingOffer_Timeout(t *testing.T) {
	store := newTestStore(t)

	// 创建 Pending Offer
	_, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "TestClient")
	if err != nil {
		t.Fatalf("CreatePendingOffer 失败: %v", err)
	}

	// 验证 PoolStats 包含 Pending Offer
	_, used := store.PoolStats()
	if used != 1 {
		t.Errorf("Pending Offer 后 used 应为 1，实际: %d", used)
	}

	// 手动设置超时（模拟60秒后）
	store.mu.Lock()
	for _, offer := range store.offers {
		offer.ExpiresAt = time.Now().Add(-1 * time.Second) // 已过期
	}
	store.mu.Unlock()

	// 清理超时 Pending Offer
	expired := store.ExpirePendingOffers()
	if len(expired) != 1 {
		t.Errorf("应释放 1 个超时 Pending Offer，实际: %d", len(expired))
	}

	// 验证释放后 used 归零
	_, used = store.PoolStats()
	if used != 0 {
		t.Errorf("超时释放后 used 应为 0，实际: %d", used)
	}

	// 验证 IP 可再次分配
	ip, err := store.CreatePendingOffer(mac("00:11:22:33:44:66"), nil, "NewClient")
	if err != nil {
		t.Fatalf("超时释放后应能重新分配，错误: %v", err)
	}
	if ip == nil {
		t.Error("超时释放后应能分配到 IP")
	}
}

// ---- 测试 3: 多客户端不重复分配 ----

func TestMultipleClients_NoDuplicateIP(t *testing.T) {
	store := newTestStore(t)

	mac1 := mac("00:11:22:33:44:55")
	mac2 := mac("00:11:22:33:44:66")
	mac3 := mac("00:11:22:33:44:77")

	// 三个客户端分别 DISCOVER
	ip1, err := store.CreatePendingOffer(mac1, nil, "Client1")
	if err != nil {
		t.Fatalf("Client1 CreatePendingOffer 失败: %v", err)
	}
	ip2, err := store.CreatePendingOffer(mac2, nil, "Client2")
	if err != nil {
		t.Fatalf("Client2 CreatePendingOffer 失败: %v", err)
	}
	ip3, err := store.CreatePendingOffer(mac3, nil, "Client3")
	if err != nil {
		t.Fatalf("Client3 CreatePendingOffer 失败: %v", err)
	}

	// 验证三个 IP 互不相同
	if ip1.Equal(ip2) || ip1.Equal(ip3) || ip2.Equal(ip3) {
		t.Errorf("多客户端分配了重复 IP: %s, %s, %s", ip1, ip2, ip3)
	}

	// 三个客户端分别 REQUEST
	_, err = store.ConfirmLease(mac1, nil, "Client1", ip1)
	if err != nil {
		t.Fatalf("Client1 ConfirmLease 失败: %v", err)
	}
	_, err = store.ConfirmLease(mac2, nil, "Client2", ip2)
	if err != nil {
		t.Fatalf("Client2 ConfirmLease 失败: %v", err)
	}
	_, err = store.ConfirmLease(mac3, nil, "Client3", ip3)
	if err != nil {
		t.Fatalf("Client3 ConfirmLease 失败: %v", err)
	}

	// 验证有 3 个活跃租约
	leases := store.List()
	if len(leases) != 3 {
		t.Errorf("应有 3 个活跃租约，实际: %d", len(leases))
	}
}

// ---- 测试 4: 网络和广播地址不可分配 ----

func TestNetworkAndBroadcastAddress_NotInPool(t *testing.T) {
	// 网络 0 和广播 255 不在池内，服务端 IP 也不在池内，应创建成功
	poolStart := net.ParseIP("192.168.1.100")
	poolEnd := net.ParseIP("192.168.1.200")
	serverIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)

	_, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err != nil {
		t.Fatalf("创建 LeaseStore 失败: %v", err)
	}
}

func TestNetworkAddress_InPool_Rejected(t *testing.T) {
	// 地址池包含网络地址 192.168.1.0
	poolStart := net.ParseIP("192.168.1.0")
	poolEnd := net.ParseIP("192.168.1.254")
	serverIP := net.ParseIP("192.168.1.100")
	mask := net.IPv4Mask(255, 255, 255, 0)

	_, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err == nil {
		t.Error("地址池包含网络地址应返回错误")
	}
}

func TestBroadcastAddress_InPool_Rejected(t *testing.T) {
	// 地址池包含广播地址 192.168.1.255
	poolStart := net.ParseIP("192.168.1.1")
	poolEnd := net.ParseIP("192.168.1.255")
	serverIP := net.ParseIP("192.168.1.100")
	mask := net.IPv4Mask(255, 255, 255, 0)

	_, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err == nil {
		t.Error("地址池包含广播地址应返回错误")
	}
}

func TestServerIP_InPool_Rejected(t *testing.T) {
	// 地址池包含服务端 IP
	poolStart := net.ParseIP("192.168.1.1")
	poolEnd := net.ParseIP("192.168.1.254")
	serverIP := net.ParseIP("192.168.1.100")
	mask := net.IPv4Mask(255, 255, 255, 0)

	_, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err == nil {
		t.Error("地址池包含服务端 IP 应返回错误")
	}
}

func TestNon24Subnet_NetworkBroadcast(t *testing.T) {
	// /23 子网测试
	// 网络: 10.0.2.0, 广播: 10.0.3.255, 服务端: 10.0.2.1
	poolStart := net.ParseIP("10.0.2.1")
	poolEnd := net.ParseIP("10.0.3.254")
	serverIP := net.ParseIP("10.0.2.1")
	mask := net.IPv4Mask(255, 255, 254, 0)

	_, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err == nil {
		t.Error("/23 子网中服务端 IP 在池内应返回错误")
	}

	// 服务端 IP 不在池内
	poolStart2 := net.ParseIP("10.0.2.10")
	poolEnd2 := net.ParseIP("10.0.3.200")
	store, err := NewLeaseStore(poolStart2, poolEnd2, serverIP, mask, 60*time.Minute)
	if err != nil {
		t.Fatalf("/23 子网合法地址池应成功: %v", err)
	}

	// 验证 PoolStats 正确
	total, _ := store.PoolStats()
	// 池内地址 = 10.0.2.10 到 10.0.3.200 = 447（uint32 差值 + 1）
	if total != 447 {
		t.Errorf("/23 子网池总量应为 447，实际: %d", total)
	}
}

// ---- 测试 5: 租约过期释放 ----

func TestLeaseExpiry(t *testing.T) {
	// 使用短租约时间
	poolStart := net.ParseIP("192.168.1.100")
	poolEnd := net.ParseIP("192.168.1.200")
	serverIP := net.ParseIP("192.168.1.1")
	mask := net.IPv4Mask(255, 255, 255, 0)

	store, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 1*time.Second)
	if err != nil {
		t.Fatalf("创建 LeaseStore 失败: %v", err)
	}

	// 分配租约
	ip, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "TestClient")
	if err != nil {
		t.Fatalf("CreatePendingOffer 失败: %v", err)
	}
	_, err = store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "TestClient", ip)
	if err != nil {
		t.Fatalf("ConfirmLease 失败: %v", err)
	}

	// 验证租约存在
	leases := store.List()
	if len(leases) != 1 {
		t.Errorf("应有 1 个活跃租约，实际: %d", len(leases))
	}

	// 等待租约过期
	time.Sleep(2 * time.Second)

	// 清理过期租约
	expired := store.ExpireLeases()
	if len(expired) != 1 {
		t.Errorf("应释放 1 个过期租约，实际: %d", len(expired))
	}

	// 验证租约列表为空
	leases = store.List()
	if len(leases) != 0 {
		t.Errorf("过期释放后不应有活跃租约，实际: %d", len(leases))
	}

	// 验证 IP 可再次分配
	_, err = store.CreatePendingOffer(mac("00:11:22:33:44:66"), nil, "NewClient")
	if err != nil {
		t.Fatalf("过期释放后应能重新分配，错误: %v", err)
	}
}

// ---- 测试 6: Option 3/6 默认不存在，siaddr 为 0 ----

func TestBuildPacket_NoRouterDNS(t *testing.T) {
	pkt := BuildPacket(
		MsgTypeACK,
		0x12345678,   // xid
		0x0000,       // flags
		nil,          // ciaddr
		net.ParseIP("192.168.1.100"), // yiaddr
		net.ParseIP("192.168.1.1"),   // serverIP
		mac("00:11:22:33:44:55"),     // chaddr
		3600,         // leaseTime
		net.IPv4Mask(255, 255, 255, 0), // subnetMask
	)

	// 验证 siaddr (buf[20:24]) 为 0.0.0.0
	siaddr := pkt[20:24]
	for _, b := range siaddr {
		if b != 0 {
			t.Errorf("siaddr 应为 0.0.0.0，实际: %d.%d.%d.%d", siaddr[0], siaddr[1], siaddr[2], siaddr[3])
			break
		}
	}

	// 解析选项，验证不包含 Router (3) 和 DNS (6)
	parsed, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析 BuildPacket 输出失败: %v", err)
	}

	if _, ok := parsed.Options[OptRouter]; ok {
		t.Error("报文不应包含 Option 3 (Router)")
	}
	if _, ok := parsed.Options[OptDNSServer]; ok {
		t.Error("报文不应包含 Option 6 (DNS)")
	}

	// 验证包含 Option 54 (Server Identifier)
	sid, ok := parsed.Options[OptServerID]
	if !ok {
		t.Error("报文应包含 Option 54 (Server Identifier)")
	} else {
		sidIP := net.IP(sid)
		expected := net.ParseIP("192.168.1.1").To4()
		if !sidIP.Equal(expected) {
			t.Errorf("Option 54 应为 192.168.1.1，实际: %s", sidIP)
		}
	}

	// 验证包含 Option 1 (Subnet Mask)
	if _, ok := parsed.Options[OptSubnetMask]; !ok {
		t.Error("报文应包含 Option 1 (Subnet Mask)")
	}
}

// ---- 测试 7: 相同客户端优先原 IP ----

func TestSameClient_ReuseOriginalIP(t *testing.T) {
	store := newTestStore(t)

	// 客户端 A 第一次 DISCOVER → REQUEST → ACK
	ip1, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "ClientA")
	if err != nil {
		t.Fatalf("首次 CreatePendingOffer 失败: %v", err)
	}
	_, err = store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "ClientA", ip1)
	if err != nil {
		t.Fatalf("首次 ConfirmLease 失败: %v", err)
	}

	// 客户端 A 再次 DISCOVER（续约场景）→ 应复用原 IP
	ip2, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "ClientA")
	if err != nil {
		t.Fatalf("再次 CreatePendingOffer 失败: %v", err)
	}
	if !ip1.Equal(ip2) {
		t.Errorf("同一客户端再次 DISCOVER 应复用原 IP %s，实际: %s", ip1, ip2)
	}

	// 客户端 A 再次 REQUEST → 续约
	lease, err := store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "ClientA", ip2)
	if err != nil {
		t.Fatalf("再次 ConfirmLease 失败: %v", err)
	}
	if !lease.IP.Equal(ip1) {
		t.Errorf("续约后 IP 应不变，期望 %s，实际 %s", ip1, lease.IP)
	}
}

func TestSameClient_ReusePendingOffer(t *testing.T) {
	store := newTestStore(t)

	// 客户端 A 第一次 DISCOVER → Pending Offer
	ip1, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "ClientA")
	if err != nil {
		t.Fatalf("首次 CreatePendingOffer 失败: %v", err)
	}

	// 客户端 A 再次 DISCOVER（未 REQUEST 就重发）→ 应复用同一个 Pending Offer
	ip2, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "ClientA")
	if err != nil {
		t.Fatalf("再次 CreatePendingOffer 失败: %v", err)
	}
	if !ip1.Equal(ip2) {
		t.Errorf("重复 DISCOVER 应复用 Pending Offer IP %s，实际: %s", ip1, ip2)
	}

	// 不应重复分配（used 仍为 1）
	_, used := store.PoolStats()
	if used != 1 {
		t.Errorf("重复 DISCOVER 后 used 仍应为 1，实际: %d", used)
	}
}

// ---- 测试: 地址池最大 4096 限制 ----

func TestPoolSize_Max4096(t *testing.T) {
	// 超过 4096 的地址池
	poolStart := net.ParseIP("10.0.0.1")
	poolEnd := net.ParseIP("10.0.31.254") // 约 8190 个地址
	serverIP := net.ParseIP("10.0.0.0")   // 不在池内但也不在池内...
	mask := net.IPv4Mask(255, 255, 0, 0)

	_, err := NewLeaseStore(poolStart, poolEnd, serverIP, mask, 60*time.Minute)
	if err == nil {
		t.Error("超过 4096 的地址池应返回错误")
	}
}

// ---- 测试: PoolStats 使用差值计算 ----

func TestPoolStats_DifferenceCalculation(t *testing.T) {
	store := newTestStore(t)

	total, used := store.PoolStats()
	// 192.168.1.100 - 192.168.1.200 = 101 个地址
	if total != 101 {
		t.Errorf("池总量应为 101，实际: %d", total)
	}
	if used != 0 {
		t.Errorf("初始 used 应为 0，实际: %d", used)
	}

	// 分配一个 Pending Offer
	store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "Test")
	_, used = store.PoolStats()
	if used != 1 {
		t.Errorf("1 个 Pending Offer 后 used 应为 1，实际: %d", used)
	}
}

// ---- 测试: DISCOVER 不产生活跃租约 ----

func TestDiscover_NoActiveLease(t *testing.T) {
	store := newTestStore(t)

	_, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "TestClient")
	if err != nil {
		t.Fatalf("CreatePendingOffer 失败: %v", err)
	}

	// 租约列表不应有记录
	leases := store.List()
	if len(leases) != 0 {
		t.Errorf("DISCOVER 后租约列表应为空，实际: %d", len(leases))
	}
}

// ---- 测试: REQUEST 地址冲突时返回 NAK ----

func TestRequest_ConflictNAK(t *testing.T) {
	store := newTestStore(t)

	// 客户端 A 获取 IP
	ipA, _ := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "A")
	leaseA, _ := store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "A", ipA)

	// 客户端 B 请求客户端 A 的 IP → NAK
	_, err := store.ConfirmLease(mac("00:11:22:33:44:66"), nil, "B", leaseA.IP)
	if err != ErrIPNotAvailable {
		t.Errorf("请求冲突 IP 应返回 ErrIPNotAvailable，实际: %v", err)
	}
}

// ---- 测试: 完整 Discover→Offer→Request→ACK 流程 ----

func TestFullDHCPFlow(t *testing.T) {
	store := newTestStore(t)

	// 1. DISCOVER
	offeredIP, err := store.CreatePendingOffer(mac("00:11:22:33:44:55"), nil, "Client1")
	if err != nil {
		t.Fatalf("DISCOVER 失败: %v", err)
	}

	// 验证：租约列表为空（只有 Pending Offer）
	if len(store.List()) != 0 {
		t.Error("DISCOVER 后不应有活跃租约")
	}

	// 2. REQUEST
	lease, err := store.ConfirmLease(mac("00:11:22:33:44:55"), nil, "Client1", offeredIP)
	if err != nil {
		t.Fatalf("REQUEST 失败: %v", err)
	}

	// 3. ACK（验证租约已创建）
	if lease.Status != LeaseActive {
		t.Errorf("ACK 后租约状态应为 active，实际: %s", lease.Status)
	}
	if !lease.IP.Equal(offeredIP) {
		t.Errorf("租约 IP 应与 Offered IP 一致")
	}

	// 4. 验证租约列表包含该客户端
	leases := store.List()
	if len(leases) != 1 {
		t.Errorf("ACK 后应有 1 个活跃租约，实际: %d", len(leases))
	}
}
