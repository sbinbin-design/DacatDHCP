package dhcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ServerStatus 表示 DHCP 服务状态
type ServerStatus struct {
	Running      bool   `json:"running"`
	AdapterName  string `json:"adapter_name"`
	ServerIP     string `json:"server_ip"`
	PoolStart    string `json:"pool_start"`
	PoolEnd      string `json:"pool_end"`
	LeaseMinutes int    `json:"lease_minutes"`
	PoolTotal    int    `json:"pool_total"`
	PoolUsed     int    `json:"pool_used"`
	Error        string `json:"error"`
}

// Server DHCP 服务器
type Server struct {
	mu          sync.RWMutex
	running     bool
	stopping    bool          // V1修复: Stop期间禁止再次启动
	adapterName string
	adapterIP   net.IP
	subnetMask  net.IPMask
	poolStart   net.IP
	poolEnd     net.IP
	leaseTime   time.Duration
	leases      *LeaseStore
	conn        net.PacketConn
	ctx         context.Context   // V1修复: context 管理协程生命周期
	cancel      context.CancelFunc
	wg          sync.WaitGroup    // V1修复: WaitGroup 等待全部协程退出
	logFunc     func(string, ...interface{})
	statusErr   string
}

// NewServer 创建 DHCP 服务器实例
func NewServer() *Server {
	return &Server{
		logFunc: func(s string, args ...interface{}) {}, // 默认空日志
	}
}

// SetLogFunc 设置日志记录函数
func (s *Server) SetLogFunc(f func(string, ...interface{})) {
	s.logFunc = f
}

// Start 启动 DHCP 服务
func (s *Server) Start(adapterName string, adapterIP net.IP, subnetMask net.IPMask,
	poolStart, poolEnd net.IP, leaseMinutes int) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("DHCP 服务已在运行")
	}
	// V1修复: Stop 期间禁止再次启动
	if s.stopping {
		return fmt.Errorf("DHCP 服务正在停止中，请稍候")
	}

	// 参数校验
	if adapterIP == nil || adapterIP.To4() == nil {
		return fmt.Errorf("网卡 IPv4 地址无效")
	}
	if poolStart == nil || poolStart.To4() == nil {
		return fmt.Errorf("起始地址无效")
	}
	if poolEnd == nil || poolEnd.To4() == nil {
		return fmt.Errorf("结束地址无效")
	}
	if leaseMinutes <= 0 {
		return fmt.Errorf("租约时间必须大于 0")
	}

	// 校验起止地址与网卡同网段
	if !sameSubnet(adapterIP, poolStart, subnetMask) {
		return fmt.Errorf("起始地址与网卡不在同一网段")
	}
	if !sameSubnet(adapterIP, poolEnd, subnetMask) {
		return fmt.Errorf("结束地址与网卡不在同一网段")
	}

	// 校验起始地址不大于结束地址（使用 uint32 数值比较）
	if ipToUint32(poolStart.To4()) > ipToUint32(poolEnd.To4()) {
		return fmt.Errorf("起始地址不能大于结束地址")
	}

	// 创建租约存储（含地址池校验）
	s.leaseTime = time.Duration(leaseMinutes) * time.Minute
	var err error
	s.leases, err = NewLeaseStore(poolStart, poolEnd, adapterIP, subnetMask, s.leaseTime)
	if err != nil {
		return err
	}

	// 创建 UDP 套接字，绑定到网卡 IP:67
	conn, err := s.createConn(adapterIP)
	if err != nil {
		return fmt.Errorf("绑定 UDP 67 端口失败: %v", err)
	}

	s.adapterName = adapterName
	s.adapterIP = adapterIP.To4()
	s.subnetMask = subnetMask
	s.poolStart = poolStart.To4()
	s.poolEnd = poolEnd.To4()
	s.conn = conn
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.statusErr = ""
	s.running = true

	// V1修复: 使用 WaitGroup 管理全部协程
	s.wg.Add(4)
	go s.serve()              // DHCP 包处理
	go s.expireLeases()       // 租约过期清理
	go s.expirePendingOffers() // Pending Offer 超时清理
	go s.monitorAdapter()     // 网卡状态监控

	s.logFunc("DHCP 服务已启动 - 网卡: %s (%s), 地址池: %s - %s, 租约: %d分钟",
		adapterName, adapterIP.String(), poolStart.String(), poolEnd.String(), leaseMinutes)

	return nil
}

// Stop 停止 DHCP 服务
// V1修复: 先禁止再次启动，关闭连接和 context，等待全部协程退出后再恢复可启动状态
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.stopping = true // 禁止 Start
	s.running = false
	s.mu.Unlock()

	// 取消 context（通知所有协程退出）
	s.cancel()

	// 关闭连接（使 serve() 的 ReadFrom 返回错误）
	if s.conn != nil {
		s.conn.Close()
	}

	// 等待全部协程退出
	s.wg.Wait()

	// 恢复可启动状态
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()

	s.logFunc("DHCP 服务已停止")
}

// Status 获取服务状态
func (s *Server) Status() ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := ServerStatus{
		Running:      s.running,
		AdapterName:  s.adapterName,
		LeaseMinutes: int(s.leaseTime.Minutes()),
		Error:        s.statusErr,
	}

	if s.adapterIP != nil {
		status.ServerIP = s.adapterIP.String()
	}
	if s.poolStart != nil {
		status.PoolStart = s.poolStart.String()
	}
	if s.poolEnd != nil {
		status.PoolEnd = s.poolEnd.String()
	}

	if s.leases != nil {
		status.PoolTotal, status.PoolUsed = s.leases.PoolStats()
	}

	return status
}

// Leases 获取当前租约列表
func (s *Server) Leases() []LeaseJSON {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.leases == nil {
		return nil
	}
	return s.leases.List()
}

// IsRunning 检查服务是否正在运行
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// createConn 创建绑定了 SO_BROADCAST 的 UDP 连接
func (s *Server) createConn(bindIP net.IP) (net.PacketConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return nil, fmt.Errorf("创建套接字失败: %v", err)
	}

	// 设置 SO_BROADCAST 以发送广播包
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("设置 SO_BROADCAST 失败: %v", err)
	}

	// 绑定到指定 IP:67
	sa := &syscall.SockaddrInet4{Port: 67}
	copy(sa.Addr[:], bindIP.To4())
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("绑定 %s:67 失败: %v", bindIP.String(), err)
	}

	// 转换为 net.PacketConn
	f := os.NewFile(uintptr(fd), "dhcp-udp")
	pc, err := net.FilePacketConn(f)
	f.Close()
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("创建 PacketConn 失败: %v", err)
	}

	return pc, nil
}

// serve DHCP 包处理主循环
func (s *Server) serve() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logFunc("DHCP 服务异常: %v", r)
		}
	}()

	buf := make([]byte, 1500)
	for {
		// 设置读超时，以便定期检查 ctx
		s.conn.SetReadDeadline(time.Now().Add(2 * time.Second))

		n, _, err := s.conn.ReadFrom(buf)
		if err != nil {
			// V1修复: 使用 context 检查是否正在停止
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // 读超时，继续循环检查 ctx
			}
			// 连接已关闭或其他错误，退出
			return
		}

		s.handlePacket(buf[:n])
	}
}

// handlePacket 处理收到的 DHCP 包
func (s *Server) handlePacket(data []byte) {
	pkt, err := ParsePacket(data)
	if err != nil {
		return // 无效包，忽略
	}

	msgType, ok := pkt.MessageType()
	if !ok {
		return // 没有消息类型选项
	}

	// V1修复: giaddr 非0 时记录并忽略（V1不支持 DHCP Relay）
	if pkt.GIAddr != nil && !pkt.GIAddr.IsUnspecified() {
		s.logFunc("忽略 DHCP Relay 报文 (giaddr=%s)，V1 不支持 DHCP Relay", pkt.GIAddr.String())
		return
	}

	switch msgType {
	case MsgTypeDiscover:
		s.handleDiscover(pkt)
	case MsgTypeRequest:
		s.handleRequest(pkt)
	case MsgTypeRelease:
		s.handleRelease(pkt)
	}
}

// responseTarget 根据报文确定响应目标地址
// V1修复: 根据 ciaddr 和 Broadcast Flag 选择响应目标
// - 续租阶段（ciaddr非0且无广播标志）: 单播到 ciaddr:68
// - 首次分配或有广播标志: 广播到 255.255.255.255:68
// - NAK: 始终广播
func (s *Server) responseTarget(pkt *Packet) *net.UDPAddr {
	// ciaddr 非空且非 0.0.0.0，且未设置广播标志 → 单播
	if pkt.CIAddr != nil && !pkt.CIAddr.IsUnspecified() && !pkt.BroadcastFlag() {
		return &net.UDPAddr{IP: pkt.CIAddr.To4(), Port: 68}
	}
	// 广播
	return &net.UDPAddr{IP: net.IPv4(255, 255, 255, 255), Port: 68}
}

// handleDiscover 处理 DHCP Discover
func (s *Server) handleDiscover(pkt *Packet) {
	clientID := pkt.ClientID()
	mac := pkt.CHAddr
	hostname := pkt.HostName()

	s.logFunc("DISCOVER from %s%s", mac.String(), hostSuffix(hostname))

	// DISCOVER 仅创建 Pending Offer
	offeredIP, err := s.leases.CreatePendingOffer(mac, clientID, hostname)
	if err != nil {
		if err == ErrPoolExhausted {
			s.logFunc("地址池已耗尽，无法响应 DISCOVER from %s", mac.String())
		}
		return
	}

	// 构建 Offer 包
	leaseTime := uint32(s.leaseTime.Seconds())
	response := BuildPacket(
		MsgTypeOffer,
		pkt.XID,
		pkt.Flags,
		nil,          // ciaddr
		offeredIP,    // yiaddr
		s.adapterIP,  // serverIP（仅用于 Option 54 Server Identifier）
		mac,          // chaddr
		leaseTime,    // lease time
		s.subnetMask, // subnet mask
	)

	// V1修复: 首次分配（DISCOVER阶段ciaddr为空），始终广播
	dst := &net.UDPAddr{IP: net.IPv4(255, 255, 255, 255), Port: 68}
	if _, err := s.conn.WriteTo(response, dst); err != nil {
		s.logFunc("发送 OFFER 失败: %v", err)
		return
	}

	s.logFunc("OFFER %s to %s%s", offeredIP.String(), mac.String(), hostSuffix(hostname))
}

// handleRequest 处理 DHCP Request
func (s *Server) handleRequest(pkt *Packet) {
	mac := pkt.CHAddr
	hostname := pkt.HostName()
	clientID := pkt.ClientID()

	// 获取请求的 IP
	requestedIP := pkt.RequestedIP()
	if requestedIP == nil && pkt.CIAddr != nil && !pkt.CIAddr.IsUnspecified() {
		requestedIP = pkt.CIAddr
	}

	s.logFunc("REQUEST from %s for %s%s", mac.String(), ipStr(requestedIP), hostSuffix(hostname))

	if requestedIP == nil {
		s.sendNAK(pkt, mac, "未指定请求的 IP 地址")
		return
	}

	// 检查 Server Identifier 选项（客户端已选择其他 DHCP Server 时直接忽略）
	serverID := pkt.ServerID()
	if serverID != nil && !serverID.Equal(s.adapterIP) {
		return
	}

	// 使用 ConfirmLease 确认指定 IP，不可分配则 NAK
	lease, err := s.leases.ConfirmLease(mac, clientID, hostname, requestedIP)
	if err != nil {
		reason := "请求的 IP 不可用"
		if err == ErrPoolExhausted {
			reason = "地址池已耗尽"
		}
		s.sendNAK(pkt, mac, reason)
		return
	}

	// 发送 ACK
	leaseTime := uint32(s.leaseTime.Seconds())
	response := BuildPacket(
		MsgTypeACK,
		pkt.XID,
		pkt.Flags,
		pkt.CIAddr,   // ciaddr
		lease.IP,     // yiaddr
		s.adapterIP,  // serverIP（仅用于 Option 54 Server Identifier）
		mac,          // chaddr
		leaseTime,    // lease time
		s.subnetMask, // subnet mask
	)

	// V1修复: 根据 ciaddr 和 Broadcast Flag 选择响应目标（续租阶段支持单播）
	dst := s.responseTarget(pkt)
	if _, err := s.conn.WriteTo(response, dst); err != nil {
		s.logFunc("发送 ACK 失败: %v", err)
		return
	}

	s.logFunc("ACK %s to %s%s", lease.IP.String(), mac.String(), hostSuffix(hostname))
}

// handleRelease 处理 DHCP Release
func (s *Server) handleRelease(pkt *Packet) {
	mac := pkt.CHAddr
	s.logFunc("RELEASE from %s", mac.String())
	s.leases.Release(mac)
}

// sendNAK 发送 DHCP NAK
// V1修复: NAK 始终广播
func (s *Server) sendNAK(pkt *Packet, mac net.HardwareAddr, reason string) {
	response := BuildPacket(
		MsgTypeNAK,
		pkt.XID,
		0x8000,       // NAK 始终设置广播标志
		nil,          // ciaddr
		nil,          // yiaddr
		s.adapterIP,  // serverIP（仅用于 Option 54 Server Identifier）
		mac,          // chaddr
		0,            // lease time
		nil,          // subnet mask
	)

	// NAK 始终广播
	dst := &net.UDPAddr{IP: net.IPv4(255, 255, 255, 255), Port: 68}
	if _, err := s.conn.WriteTo(response, dst); err != nil {
		s.logFunc("发送 NAK 失败: %v", err)
		return
	}

	s.logFunc("NAK to %s: %s", mac.String(), reason)
}

// expireLeases 定期清理过期租约
func (s *Server) expireLeases() {
	defer s.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.leases != nil {
				expired := s.leases.ExpireLeases()
				for _, l := range expired {
					s.logFunc("租约过期: %s (%s)", l.IP, l.MAC)
				}
			}
		}
	}
}

// expirePendingOffers 定期清理超时的 Pending Offer
func (s *Server) expirePendingOffers() {
	defer s.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.leases != nil {
				expired := s.leases.ExpirePendingOffers()
				for _, l := range expired {
					s.logFunc("Pending Offer 超时释放: %s (%s)", l.IP, l.MAC)
				}
			}
		}
	}
}

// monitorAdapter 监控网卡状态
// V1修复: 使用统一的 IsAdapterUp 判断，网卡断开/禁用/IP变化时立即安全停止
func (s *Server) monitorAdapter() {
	defer s.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			reason := s.checkAdapterChanged()
			if reason != "" {
				s.mu.Lock()
				s.statusErr = reason
				s.running = false
				s.mu.Unlock()
				s.logFunc("%s，DHCP 服务已自动停止", reason)
				// 取消 context（通知其他协程退出）
				s.cancel()
				// 关闭连接
				if s.conn != nil {
					s.conn.Close()
				}
				return
			}
		}
	}
}

// checkAdapterChanged 检查网卡状态是否变化
// V1修复: 返回变化原因字符串，空字符串表示无变化
// 使用统一的 Windows IP Helper API + net.FlagUp 降级逻辑
func (s *Server) checkAdapterChanged() string {
	s.mu.RLock()
	adapterName := s.adapterName
	originalIP := make(net.IP, len(s.adapterIP))
	copy(originalIP, s.adapterIP)
	s.mu.RUnlock()

	ifaces, err := net.Interfaces()
	if err != nil {
		return "" // 枚举失败不触发停止
	}

	for _, iface := range ifaces {
		if iface.Name != adapterName {
			continue
		}

		// V1修复: 使用统一的 isIfUpLogic 判断网卡状态
		if !isIfUpLogic(iface.Index, iface.Flags) {
			return "网卡已断开或禁用"
		}

		addrs, err := iface.Addrs()
		if err != nil {
			return "无法获取网卡地址"
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			if ip4.Equal(originalIP) {
				return "" // IP 未变化，网卡正常
			}
		}
		return "网卡 IP 地址变化"
	}

	return "网卡不存在" // 网卡消失
}

// isIfUpLogic 网卡状态判断逻辑（与 network.IsIfUp 一致，dhcp 包内部使用）
// V1修复: 优先使用 Windows IP Helper API 获取 OperStatus，失败时降级使用 net.FlagUp
func isIfUpLogic(ifIndex int, flags net.Flags) bool {
	iphlpapi := syscall.NewLazyDLL("iphlpapi.dll")
	getIfEntry := iphlpapi.NewProc("GetIfEntry")

	const rowSize = 864
	const offsetIndex = 512
	const offsetOperStatus = 544

	buf := make([]byte, rowSize)
	// 设置 dwIndex
	buf[offsetIndex] = byte(ifIndex)
	buf[offsetIndex+1] = byte(ifIndex >> 8)
	buf[offsetIndex+2] = byte(ifIndex >> 16)
	buf[offsetIndex+3] = byte(ifIndex >> 24)

	ret, _, _ := getIfEntry.Call(uintptr(unsafe.Pointer(&buf[0])))
	if ret == 0 {
		// 成功读取 OperStatus
		operStatus := uint32(buf[offsetOperStatus]) | uint32(buf[offsetOperStatus+1])<<8 |
			uint32(buf[offsetOperStatus+2])<<16 | uint32(buf[offsetOperStatus+3])<<24
		return operStatus >= 4 // IF_OPER_STATUS_CONNECTED=4, OPERATIONAL=5
	}
	// 降级：使用 net.FlagUp
	return flags&net.FlagUp != 0
}

// sameSubnet 检查两个 IP 是否在同一子网
func sameSubnet(ip1, ip2 net.IP, mask net.IPMask) bool {
	if ip1 == nil || ip2 == nil || mask == nil {
		return false
	}
	ip1 = ip1.To4()
	ip2 = ip2.To4()
	if ip1 == nil || ip2 == nil {
		return false
	}
	if len(mask) < 4 {
		return false
	}

	// 使用 uint32 进行子网比较
	return (binary.BigEndian.Uint32(ip1) & binary.BigEndian.Uint32(mask[:4])) ==
		(binary.BigEndian.Uint32(ip2) & binary.BigEndian.Uint32(mask[:4]))
}

// hostSuffix 返回主机名后缀（用于日志）
func hostSuffix(hostname string) string {
	if hostname == "" {
		return ""
	}
	return " (" + hostname + ")"
}

// ipStr 返回 IP 字符串
func ipStr(ip net.IP) string {
	if ip == nil {
		return "N/A"
	}
	return ip.String()
}
