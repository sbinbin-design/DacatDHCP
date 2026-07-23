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

	"DacatDHCP/internal/network"
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
	StartedAt    string `json:"started_at"` // V10新增: 服务启动时间(RFC3339),前端据此计算真实运行时间,停止时为空
}

// StopType 标识停止类型（V9: 替代文本比较，避免国际化/文案变化破坏判断）
type StopType int

const (
	StopManual   StopType = iota // 用户手动点击停止
	StopShutdown                 // 程序退出引发的停止
	StopAuto                     // 网卡异常等非预期自动停止
)

// stopRequest 携带停止原因和类型（V9: 替代旧的纯字符串通道）
type stopRequest struct {
	reason   string
	stopType StopType
}

// runningInstance 表示一次独立运行实例
// V1修复: 每次Start创建独立实例，所有协程绑定本次实例资源
// V2修复: stopReqCh/stopDoneCh移入实例，Stop只操作捕获到的实例通道
// V3修复: 增加stopped标志防止doStop被重复调用
// V9修复: stopReqCh改为chan stopRequest，携带明确的停止类型
// V2新增: gateway/dnsServers 作为不可变运行配置，运行中不得读取共享配置
// V1.0.3新增: staticLeases 作为不可变运行配置，仅含 enabled=true 的映射
type runningInstance struct {
	ctx          context.Context
	cancel       context.CancelFunc
	conn         net.PacketConn
	wg           sync.WaitGroup
	adapterName  string
	adapterIP    net.IP
	gateway      net.IP           // V2新增: 启动时校验后的网关（不可变，空表示不下发）
	dnsServers   []net.IP         // V2新增: 启动时校验后的DNS（不可变，空表示不下发）
	staticLeases []StaticLease    // V1.0.3新增: 启用的固定映射（不可变，运行中不读取共享配置）
	stopReason   string           // 停止原因（正常停止或网卡异常）
	stopReqCh    chan stopRequest // 停止请求通道（V9: 改为 stopRequest 携带停止类型）
	stopDoneCh   chan struct{}    // 停止完成信号（V2: 从Server移入实例）
	stopped      bool             // V3: 防止doStop对同一实例重复执行
}

// Server DHCP 服务器
type Server struct {
	mu          sync.RWMutex
	instance    *runningInstance // 当前运行实例
	stopping    bool             // V1修复: Stop期间禁止再次Start
	adapterName string           // 配置参数（用于 Status）
	adapterIP   net.IP
	subnetMask  net.IPMask
	poolStart   net.IP
	poolEnd     net.IP
	leaseTime   time.Duration
	leases      *LeaseStore
	logFunc     func(string, ...interface{})
	statusErr   string
	startedAt   time.Time // V10新增: 服务启动时间,供 Status 返回真实运行时间,禁止前端伪造

	// V1.0.3新增: 待传入运行实例的固定映射,由 AppServer.handleStart 调用 SetStaticLeases 设置
	// Start 时复制到 runningInstance 作为不可变运行配置
	pendingStaticLeases []StaticLease

	// V2修复: stopReqCh/stopDoneCh已移入runningInstance，旧实例不得关闭新实例通道

	// V1修复: 可注入的连接创建函数（生命周期测试用）
	createConnFunc func(bindIP net.IP) (net.PacketConn, error)

	// V3: DHCP自动停止时的回调（用于通知托盘更新状态）
	// V8: 回调接收停止原因，供测试断言
	onAutoStop func(reason string)
}

// StaticLease 固定 MAC-IP 映射（V1.0.3新增）
// 由 server 包从配置转换而来,存入 runningInstance 作为不可变运行配置
type StaticLease struct {
	MAC net.HardwareAddr
	IP  net.IP
}

// NewServer 创建 DHCP 服务器实例
func NewServer() *Server {
	return &Server{
		logFunc: func(s string, _ ...interface{}) {},
	}
}

// SetLogFunc 设置日志记录函数
func (s *Server) SetLogFunc(f func(string, ...interface{})) {
	s.logFunc = f
}

// SetOnAutoStop 设置自动停止回调（V3: 通知托盘更新状态）
// V8: 回调接收停止原因，供测试断言
func (s *Server) SetOnAutoStop(f func(reason string)) {
	s.onAutoStop = f
}

// SetCreateConnFunc 设置连接创建函数（V6: 供 AppServer 测试注入）
func (s *Server) SetCreateConnFunc(f func(bindIP net.IP) (net.PacketConn, error)) {
	s.createConnFunc = f
}

// SetStaticLeases 设置固定映射列表（V1.0.3新增）
// 必须在 Start 之前调用;Start 时复制到 runningInstance 作为不可变运行配置
// 仅含 enabled=true 的映射,由 server.buildDHCPStaticLeases 过滤
func (s *Server) SetStaticLeases(leases []StaticLease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingStaticLeases = leases
}

// Start 启动 DHCP 服务
// V2新增: gateway/dnsServers 作为不可变运行配置存入实例；为空则不下发 Option 3/6
// gateway 为空表示不下发网关；dnsServers 为空表示不下发 DNS
func (s *Server) Start(adapterName string, adapterIP net.IP, subnetMask net.IPMask,
	poolStart, poolEnd net.IP, leaseMinutes int,
	gateway net.IP, dnsServers []net.IP) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	// V1修复: 检查当前状态
	if s.instance != nil {
		return fmt.Errorf("DHCP 服务已在运行")
	}
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

	// 校验起始地址不大于结束地址
	if ipToUint32(poolStart.To4()) > ipToUint32(poolEnd.To4()) {
		return fmt.Errorf("起始地址不能大于结束地址")
	}

	// V2新增: 规范化网关（非空时转为 4 字节表示）
	var gw net.IP
	if gateway != nil {
		gw = gateway.To4()
		if gw == nil {
			return fmt.Errorf("网关地址无效")
		}
	}

	// V2新增: 规范化 DNS（过滤 nil，转为 4 字节表示）
	var dnsList []net.IP
	for _, d := range dnsServers {
		if d == nil {
			continue
		}
		d4 := d.To4()
		if d4 == nil {
			return fmt.Errorf("DNS 地址无效")
		}
		dnsList = append(dnsList, d4)
	}

	// 创建租约存储
	s.leaseTime = time.Duration(leaseMinutes) * time.Minute
	var err error
	s.leases, err = NewLeaseStore(poolStart, poolEnd, adapterIP, subnetMask, s.leaseTime)
	if err != nil {
		return err
	}

	// V1.0.3新增: 将固定映射中处于动态池内的 IP 加入排除集合
	// 动态分配时跳过这些保留 IP,避免与固定映射冲突;池外的固定 IP 不影响动态分配
	var reservedIPs []net.IP
	for _, sl := range s.pendingStaticLeases {
		if sl.IP != nil {
			reservedIPs = append(reservedIPs, sl.IP)
		}
	}
	s.leases.AddReservedIPs(reservedIPs)

	// 创建 UDP 套接字
	// V1修复: 优先使用注入的 createConnFunc（测试用），否则使用默认实现
	var conn net.PacketConn
	if s.createConnFunc != nil {
		conn, err = s.createConnFunc(adapterIP)
	} else {
		conn, err = s.createConn(adapterIP)
	}
	if err != nil {
		return fmt.Errorf("绑定 UDP 67 端口失败: %v", err)
	}

	// V1修复: 创建独立运行实例
	// V2新增: gateway/dnsServers 存入实例作为不可变运行配置
	// V1.0.3新增: staticLeases 存入实例作为不可变运行配置
	ctx, cancel := context.WithCancel(context.Background())
	inst := &runningInstance{
		ctx:          ctx,
		cancel:       cancel,
		conn:         conn,
		adapterName:  adapterName,
		adapterIP:    adapterIP.To4(),
		gateway:      gw,
		dnsServers:   dnsList,
		staticLeases: s.pendingStaticLeases,
	}
	s.instance = inst

	// V2修复: 协调通道移入实例，每次Start创建独立通道
	// V9修复: stopReqCh 改为 chan stopRequest
	inst.stopReqCh = make(chan stopRequest, 1)
	inst.stopDoneCh = make(chan struct{})

	// 保存配置参数（用于 Status）
	s.adapterName = adapterName
	s.adapterIP = adapterIP.To4()
	s.subnetMask = subnetMask
	s.poolStart = poolStart.To4()
	s.poolEnd = poolEnd.To4()
	s.statusErr = ""
	s.startedAt = time.Now() // V10新增: 记录真实启动时间,供前端计算运行时间

	// V1修复: 启动协程，全部绑定本次实例
	inst.wg.Add(5)
	go s.serve(inst)               // DHCP 包处理
	go s.expireLeases(inst)        // 租约过期清理
	go s.expirePendingOffers(inst) // Pending Offer 超时清理
	go s.monitorAdapter(inst)      // 网卡状态监控
	go s.stopCoordinator(inst)     // 停止协调器

	s.logFunc("DHCP 服务已启动 - 网卡: %s (%s), 地址池: %s - %s, 租约: %d分钟",
		adapterName, adapterIP.String(), poolStart.String(), poolEnd.String(), leaseMinutes)

	return nil
}

// Stop 停止 DHCP 服务（用户手动停止）
// V2修复: 捕获实例引用后只操作该实例的通道，旧实例不得等待新实例通道
// V9修复: 使用 stopRequest 携带 StopManual 类型，不再依赖文本比较判断停止类型
func (s *Server) Stop() {
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	if inst == nil {
		return
	}

	// V9: 发送手动停止请求（status.error 会被清空）
	select {
	case inst.stopReqCh <- stopRequest{reason: "服务已停止", stopType: StopManual}:
	default:
		// 已有停止请求在队列中
	}

	// 等待捕获实例的停止完成信号
	<-inst.stopDoneCh
}

// StopForShutdown 程序退出引发的停止（V9: 托盘退出/系统关机/应用关闭调用）
// 与 Stop 行为一致：清空 status.error，不残留错误提示，仅日志记录不同原因
func (s *Server) StopForShutdown() {
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	if inst == nil {
		return
	}

	// V9: 发送退出停止请求（status.error 会被清空）
	select {
	case inst.stopReqCh <- stopRequest{reason: "程序退出，DHCP 服务已停止", stopType: StopShutdown}:
	default:
		// 已有停止请求在队列中
	}

	// 等待捕获实例的停止完成信号
	<-inst.stopDoneCh
}

// doStop 统一停止流程（锁内部分）
// V1修复: 正常停止和网卡异常停止均走此流程
// V3修复: 增加 stopped 标志，同一实例只允许执行一次完整停止
// V4修复: 锁内仅完成实例清理和状态更新，日志和回调在锁外执行
// V9修复: 接收 stopType 参数，依据类型决定 status.error（manual/shutdown 清空，auto 写入原因）
// 调用前必须持有 mu.Lock，调用方在 mu.Unlock 后执行返回的回调
func (s *Server) doStop(inst *runningInstance, reason string, stopType StopType) (postLockFunc func()) {
	if inst == nil || inst.stopped {
		return nil
	}
	inst.stopped = true

	// 设置 stopping 标志，阻止再次 Start
	s.stopping = true

	// 取消 context（通知所有协程退出）
	inst.cancel()

	// 关闭连接（使 serve() 的 ReadFrom 返回错误）
	if inst.conn != nil {
		inst.conn.Close()
	}

	// 释放锁，等待协程退出（避免死锁）
	s.mu.Unlock()
	inst.wg.Wait()
	s.mu.Lock()

	// 设置停止原因
	inst.stopReason = reason
	// V9: 仅 auto 类型停止写入 status.error；manual 和 shutdown 清空，避免正常停止显示为红色错误
	if stopType == StopAuto {
		s.statusErr = reason
	} else {
		s.statusErr = ""
	}

	// 清理实例
	s.instance = nil
	s.stopping = false
	s.startedAt = time.Time{} // V10新增: 清空启动时间,避免停止后残留

	// V5: stopDoneCh 已移入 postLockFn，在 log 和 callback 之后关闭

	// V5: 捕获回调引用，在锁外执行
	logFunc := s.logFunc
	onAutoStop := s.onAutoStop
	// V9: 使用 stopType 判断是否自动停止，替代旧的文本比较
	isAutoStop := stopType == StopAuto
	stopDoneCh := inst.stopDoneCh

	return func() {
		logFunc(reason)
		if isAutoStop && onAutoStop != nil {
			onAutoStop(reason) // V8: 传递停止原因
		}
		// V5: 最后关闭 stopDoneCh，Stop() 调用方此时可见完整状态
		if stopDoneCh != nil {
			select {
			case <-stopDoneCh:
				// 已关闭
			default:
				close(stopDoneCh)
			}
		}
	}
}

// stopCoordinator 停止协调器
// V1修复: 接收网卡监控的停止请求，执行完整停止流程
// 避免网卡监控协程直接等待包含自身的 WaitGroup
// 注意：执行 doStop 前必须先 wg.Done()，否则 doStop 中的 wg.Wait() 会死锁
// V4修复: doStop 返回的回调在 mu.Unlock 后执行，避免持锁调外部函数
func (s *Server) stopCoordinator(inst *runningInstance) {
	select {
	case <-inst.ctx.Done():
		// 正常停止（Stop() 调用 cancel）
		inst.wg.Done()
		return
	case req := <-inst.stopReqCh:
		// 收到停止请求（网卡异常、正常停止或程序退出停止）
		// V1修复: 先 Done() 退出 wg，再执行 doStop（doStop 内部会 wg.Wait()）
		// V9修复: 传递 stopType，替代旧的文本比较
		inst.wg.Done()
		s.mu.Lock()
		var postLockFn func()
		if s.instance == inst {
			postLockFn = s.doStop(inst, req.reason, req.stopType)
		}
		s.mu.Unlock()
		// V4: 在锁外执行日志记录和自动停止回调
		if postLockFn != nil {
			postLockFn()
		}
		return
	}
}

// Status 获取服务状态
func (s *Server) Status() ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := ServerStatus{
		Running:      s.instance != nil,
		AdapterName:  s.adapterName,
		LeaseMinutes: int(s.leaseTime.Minutes()),
		Error:        s.statusErr,
	}

	// V10新增: 仅运行中返回启动时间,停止时为空字符串
	if s.instance != nil && !s.startedAt.IsZero() {
		status.StartedAt = s.startedAt.Format(time.RFC3339)
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
	return s.instance != nil
}

// createConn 创建绑定了 SO_BROADCAST 的 UDP 连接
func (s *Server) createConn(bindIP net.IP) (net.PacketConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return nil, fmt.Errorf("创建套接字失败: %v", err)
	}

	// 设置 SO_BROADCAST
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
// V1修复: 绑定到本次实例，使用实例的 conn 和 ctx
func (s *Server) serve(inst *runningInstance) {
	defer inst.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logFunc("DHCP 服务异常: %v", r)
		}
	}()

	buf := make([]byte, 1500)
	for {
		// 设置读超时
		inst.conn.SetReadDeadline(time.Now().Add(2 * time.Second))

		n, _, err := inst.conn.ReadFrom(buf)
		if err != nil {
			select {
			case <-inst.ctx.Done():
				return
			default:
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		s.handlePacket(buf[:n])
	}
}

// handlePacket 处理收到的 DHCP 包
func (s *Server) handlePacket(data []byte) {
	pkt, err := ParsePacket(data)
	if err != nil {
		return
	}

	msgType, ok := pkt.MessageType()
	if !ok {
		return
	}

	// V1修复: giaddr 非0 时记录并忽略
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

// responseTarget 根据报文确定响应目标
func (s *Server) responseTarget(pkt *Packet) *net.UDPAddr {
	if pkt.CIAddr != nil && !pkt.CIAddr.IsUnspecified() && !pkt.BroadcastFlag() {
		return &net.UDPAddr{IP: pkt.CIAddr.To4(), Port: 68}
	}
	return &net.UDPAddr{IP: net.IPv4(255, 255, 255, 255), Port: 68}
}

// findStaticLease 按客户端 MAC 查找启用的固定映射（V1.0.3新增）
// 返回匹配的固定 IP;未命中返回 nil
// inst.staticLeases 为不可变运行配置,无需加锁
func (s *Server) findStaticLease(inst *runningInstance, mac net.HardwareAddr) net.IP {
	if mac == nil || len(mac) == 0 {
		return nil
	}
	macStr := mac.String() // net.HardwareAddr.String() 返回小写冒号格式
	for _, sl := range inst.staticLeases {
		if sl.MAC.String() == macStr {
			return sl.IP
		}
	}
	return nil
}

// handleDiscover 处理 DHCP Discover
// V1.0.3新增: 先按客户端 MAC 查找 enabled 固定映射;命中则优先分配绑定 IP,未命中走原逻辑
func (s *Server) handleDiscover(pkt *Packet) {
	clientID := pkt.ClientID()
	mac := pkt.CHAddr
	hostname := pkt.HostName()

	s.logFunc("DISCOVER from %s%s", mac.String(), hostSuffix(hostname))

	// 先捕获实例,读取不可变运行配置（网关/DNS/固定映射）
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	if inst == nil || inst.conn == nil {
		return
	}

	var offeredIP net.IP
	var err error

	// V1.0.3: 优先匹配固定映射
	if staticIP := s.findStaticLease(inst, mac); staticIP != nil {
		offeredIP, err = s.leases.CreateStaticOffer(mac, clientID, hostname, staticIP)
		if err != nil {
			s.logFunc("固定映射 IP 分配失败: %s (%s)", staticIP.String(), mac.String())
			return
		}
		s.logFunc("Static lease matched: %s -> %s", mac.String(), offeredIP.String())
	} else {
		// 未命中固定映射,走原逻辑从地址池分配
		offeredIP, err = s.leases.CreatePendingOffer(mac, clientID, hostname)
		if err != nil {
			if err == ErrPoolExhausted {
				s.logFunc("地址池已耗尽，无法响应 DISCOVER from %s", mac.String())
			}
			return
		}
	}

	leaseTime := uint32(s.leaseTime.Seconds())
	response := BuildPacket(
		MsgTypeOffer,
		pkt.XID,
		pkt.Flags,
		nil,
		offeredIP,
		s.adapterIP,
		mac,
		ReplyOptions{
			LeaseTime:  leaseTime,
			SubnetMask: s.subnetMask,
			Router:     inst.gateway,    // V2: 从不可变实例读取
			DNSServers: inst.dnsServers, // V2: 从不可变实例读取
		},
	)

	dst := &net.UDPAddr{IP: net.IPv4(255, 255, 255, 255), Port: 68}
	if _, err := inst.conn.WriteTo(response, dst); err != nil {
		s.logFunc("发送 OFFER 失败: %v", err)
		return
	}

	s.logFunc("OFFER %s to %s%s", offeredIP.String(), mac.String(), hostSuffix(hostname))
}

// handleRequest 处理 DHCP Request
// V1.0.3新增: 先按客户端 MAC 查找固定映射;命中则只接受请求固定 IP,否则走原逻辑
func (s *Server) handleRequest(pkt *Packet) {
	mac := pkt.CHAddr
	hostname := pkt.HostName()
	clientID := pkt.ClientID()

	requestedIP := pkt.RequestedIP()
	if requestedIP == nil && pkt.CIAddr != nil && !pkt.CIAddr.IsUnspecified() {
		requestedIP = pkt.CIAddr
	}

	s.logFunc("REQUEST from %s for %s%s", mac.String(), ipStr(requestedIP), hostSuffix(hostname))

	if requestedIP == nil {
		s.sendNAK(pkt, mac, "未指定请求的 IP 地址")
		return
	}

	serverID := pkt.ServerID()
	if serverID != nil && !serverID.Equal(s.adapterIP) {
		return
	}

	// 先捕获实例,读取不可变运行配置（网关/DNS/固定映射）
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	if inst == nil || inst.conn == nil {
		return
	}

	var lease *Lease
	var err error

	// V1.0.3: 优先匹配固定映射
	if staticIP := s.findStaticLease(inst, mac); staticIP != nil {
		// 固定映射命中: 请求的 IP 必须等于固定 IP,否则 NAK
		if !requestedIP.Equal(staticIP) {
			s.sendNAK(pkt, mac, "固定映射要求请求指定 IP")
			return
		}
		lease, err = s.leases.ConfirmStaticLease(mac, clientID, hostname, requestedIP, staticIP)
		if err != nil {
			s.sendNAK(pkt, mac, "固定映射 IP 不可用")
			return
		}
		s.logFunc("Static lease matched: %s -> %s", mac.String(), lease.IP.String())
	} else {
		// 未命中固定映射,走原逻辑
		lease, err = s.leases.ConfirmLease(mac, clientID, hostname, requestedIP)
		if err != nil {
			reason := "请求的 IP 不可用"
			if err == ErrPoolExhausted {
				reason = "地址池已耗尽"
			}
			s.sendNAK(pkt, mac, reason)
			return
		}
	}

	leaseTime := uint32(s.leaseTime.Seconds())

	response := BuildPacket(
		MsgTypeACK,
		pkt.XID,
		pkt.Flags,
		pkt.CIAddr,
		lease.IP,
		s.adapterIP,
		mac,
		ReplyOptions{
			LeaseTime:  leaseTime,
			SubnetMask: s.subnetMask,
			Router:     inst.gateway,    // V2: 从不可变实例读取
			DNSServers: inst.dnsServers, // V2: 从不可变实例读取
		},
	)

	dst := s.responseTarget(pkt)
	if _, err := inst.conn.WriteTo(response, dst); err != nil {
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
// V2新增: NAK 使用空 ReplyOptions，不下发租约时间、子网掩码、网关、DNS
func (s *Server) sendNAK(pkt *Packet, mac net.HardwareAddr, reason string) {
	response := BuildPacket(
		MsgTypeNAK,
		pkt.XID,
		0x8000,
		nil,
		nil,
		s.adapterIP,
		mac,
		ReplyOptions{}, // NAK 不包含 Option 3/6/51 等成功租约选项
	)

	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	if inst == nil || inst.conn == nil {
		return
	}

	dst := &net.UDPAddr{IP: net.IPv4(255, 255, 255, 255), Port: 68}
	if _, err := inst.conn.WriteTo(response, dst); err != nil {
		s.logFunc("发送 NAK 失败: %v", err)
		return
	}

	s.logFunc("NAK to %s: %s", mac.String(), reason)
}

// expireLeases 定期清理过期租约
func (s *Server) expireLeases(inst *runningInstance) {
	defer inst.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-inst.ctx.Done():
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
func (s *Server) expirePendingOffers(inst *runningInstance) {
	defer inst.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-inst.ctx.Done():
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
// V1修复: 使用 network.IsAdapterUp 公共方法，检测异常时发送停止请求
func (s *Server) monitorAdapter(inst *runningInstance) {
	defer inst.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-inst.ctx.Done():
			return
		case <-ticker.C:
			reason := s.checkAdapterChanged(inst)
			if reason != "" {
				// V1修复: 发送停止请求，由协调器执行停止（避免死锁）
				// V9修复: 使用 StopAuto 类型，使 status.error 记录异常原因
				select {
				case inst.stopReqCh <- stopRequest{reason: reason, stopType: StopAuto}:
				default:
					// 已有停止请求
				}
				return
			}
		}
	}
}

// checkAdapterChanged 检查网卡状态是否变化
// V1修复: 使用 network.IsAdapterUp 和 network.GetAdapterIPByName 公共方法
func (s *Server) checkAdapterChanged(inst *runningInstance) string {
	adapterName := inst.adapterName
	originalIP := inst.adapterIP

	// V1修复: 使用统一的 network 公共方法检查网卡状态
	if !network.IsAdapterUp(adapterName) {
		return "网卡已断开或禁用，DHCP 服务已自动停止"
	}

	// 检查 IP 是否变化
	ip, _, err := network.GetAdapterIPByName(adapterName)
	if err != nil {
		return "无法获取网卡地址，DHCP 服务已自动停止"
	}
	if !ip.Equal(originalIP) {
		return "网卡 IP 地址变化，DHCP 服务已自动停止"
	}

	return ""
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

	return (binary.BigEndian.Uint32(ip1) & binary.BigEndian.Uint32(mask[:4])) ==
		(binary.BigEndian.Uint32(ip2) & binary.BigEndian.Uint32(mask[:4]))
}

// hostSuffix 返回主机名后缀
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
