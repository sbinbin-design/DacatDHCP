package dhcp

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// LeaseState 表示租约状态
const (
	LeaseActive  = "active"
	LeaseExpired = "expired"
)

// PendingOfferTimeout Pending Offer 默认保留时间（V1修复: 60秒超时自动释放）
const PendingOfferTimeout = 60 * time.Second

// MaxPoolSize 地址池最大容量（V1修复: 限制最大4096个地址）
const MaxPoolSize = 4096

// Lease 表示一个 DHCP 租约（Active Lease）
type Lease struct {
	IP         net.IP           `json:"ip"`
	MAC        net.HardwareAddr `json:"mac"`
	Hostname   string           `json:"hostname"`
	ClientID   []byte           `json:"-"`
	AssignedAt time.Time        `json:"assigned_at"`
	ExpiresAt  time.Time        `json:"expires_at"`
	Status     string           `json:"status"`
}

// PendingOffer 表示一个待确认的 Offer（V1修复: DISCOVER 仅创建 Pending Offer，REQUEST 后转为 Active Lease）
type PendingOffer struct {
	IP        net.IP
	MAC       net.HardwareAddr
	ClientID  []byte
	Hostname  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// LeaseJSON 用于 JSON 序列化（MAC 格式化）
type LeaseJSON struct {
	IP         string `json:"ip"`
	MAC        string `json:"mac"`
	Hostname   string `json:"hostname"`
	AssignedAt string `json:"assigned_at"`
	ExpiresAt  string `json:"expires_at"`
	Status     string `json:"status"`
}

// ToJSON 转换为 JSON 友好格式
func (l *Lease) ToJSON() LeaseJSON {
	return LeaseJSON{
		IP:         l.IP.String(),
		MAC:        l.MAC.String(),
		Hostname:   l.Hostname,
		AssignedAt: l.AssignedAt.Format("2006-01-02 15:04:05"),
		ExpiresAt:  l.ExpiresAt.Format("2006-01-02 15:04:05"),
		Status:     l.Status,
	}
}

// LeaseStore 管理所有 DHCP 租约和 Pending Offer
type LeaseStore struct {
	mu             sync.RWMutex
	leases         map[string]*Lease        // IP -> Lease（仅 Active）
	macIndex       map[string]*Lease        // MAC -> Lease
	clientIDIndex  map[string]*Lease        // ClientID -> Lease
	offers         map[string]*PendingOffer // IP -> PendingOffer
	offerMAC       map[string]*PendingOffer // MAC -> PendingOffer
	offerClientID  map[string]*PendingOffer // ClientID -> PendingOffer
	pool           [2]uint32                // 地址池起止
	excluded       map[uint32]bool          // 排除的地址（如服务端 IP、网络地址、广播地址）
	excludedInPool int                      // 排除地址在池内的数量（PoolStats 用）
	leaseDur       time.Duration            // 租约时长
}

// NewLeaseStore 创建租约存储
// V1修复: 增加 subnetMask 参数，自动排除网络地址和广播地址，校验地址池大小
func NewLeaseStore(poolStart, poolEnd net.IP, serverIP net.IP, subnetMask net.IPMask, leaseDur time.Duration) (*LeaseStore, error) {
	startVal := ipToUint32(poolStart.To4())
	endVal := ipToUint32(poolEnd.To4())

	// 优先校验起始地址不能大于结束地址，避免 uint32 无符号减法回绕后误报
	if startVal > endVal {
		return nil, fmt.Errorf("地址池起始地址不能大于结束地址")
	}

	// 校验地址池大小
	poolSize := int(endVal - startVal + 1)
	if poolSize <= 0 {
		return nil, fmt.Errorf("地址池起止地址无效")
	}
	if poolSize > MaxPoolSize {
		return nil, fmt.Errorf("地址池最大支持 %d 个地址，当前 %d 个", MaxPoolSize, poolSize)
	}

	// V1修复: 计算网络地址和广播地址，使用 uint32 数值计算
	var networkAddr, broadcastAddr uint32
	if len(subnetMask) >= net.IPv4len {
		maskVal := binary.BigEndian.Uint32(subnetMask[:4])
		if serverIP != nil {
			ipVal := ipToUint32(serverIP.To4())
			networkAddr = ipVal & maskVal
			broadcastAddr = networkAddr | ^maskVal
		}
	}

	excluded := make(map[uint32]bool)

	// V1修复: 禁止地址池包含服务端 IP
	if serverIP != nil {
		srvVal := ipToUint32(serverIP.To4())
		if srvVal >= startVal && srvVal <= endVal {
			return nil, fmt.Errorf("地址池不能包含服务端 IP (%s)", serverIP.String())
		}
		excluded[srvVal] = true
	}

	// V1修复: 禁止地址池包含网络地址
	if networkAddr != 0 {
		if networkAddr >= startVal && networkAddr <= endVal {
			return nil, fmt.Errorf("地址池不能包含网络地址 (%s)", uint32ToIP(networkAddr).String())
		}
		excluded[networkAddr] = true
	}

	// V1修复: 禁止地址池包含广播地址
	if broadcastAddr != 0 {
		if broadcastAddr >= startVal && broadcastAddr <= endVal {
			return nil, fmt.Errorf("地址池不能包含广播地址 (%s)", uint32ToIP(broadcastAddr).String())
		}
		excluded[broadcastAddr] = true
	}

	// 计算池内排除地址数量（用于 PoolStats）
	excludedInPool := 0
	for ipVal := range excluded {
		if ipVal >= startVal && ipVal <= endVal {
			excludedInPool++
		}
	}

	return &LeaseStore{
		leases:         make(map[string]*Lease),
		macIndex:       make(map[string]*Lease),
		clientIDIndex:  make(map[string]*Lease),
		offers:         make(map[string]*PendingOffer),
		offerMAC:       make(map[string]*PendingOffer),
		offerClientID:  make(map[string]*PendingOffer),
		pool:           [2]uint32{startVal, endVal},
		excluded:       excluded,
		excludedInPool: excludedInPool,
		leaseDur:       leaseDur,
	}, nil
}

// AddReservedIPs 将 IP 加入排除集合（V1.0.3新增）
// 用于将池内的固定映射 IP 排除出动态分配;池外 IP 不影响 excludedInPool 计数
// 必须在 NewLeaseStore 之后、Start 协程启动之前调用,避免并发访问
func (s *LeaseStore) AddReservedIPs(ips []net.IP) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ip := range ips {
		if ip == nil {
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil {
			continue
		}
		ipVal := ipToUint32(ip4)
		// 仅在尚未排除时增加计数,避免重复计数
		if !s.excluded[ipVal] {
			s.excluded[ipVal] = true
			if s.isInPool(ipVal) {
				s.excludedInPool++
			}
		}
	}
}

// CreateStaticOffer 为固定映射客户端创建 Pending Offer（DISCOVER 阶段，V1.0.3新增）
// 用于固定映射命中时,绕过 isInPool/isExcluded 检查直接创建 Pending Offer
// staticIP 为固定映射的 IP,可能位于动态池内（已加入 excluded）或池外
// 同一客户端重复 DISCOVER 优先复用已有的 Pending Offer 或有效租约
func (s *LeaseStore) CreateStaticOffer(mac net.HardwareAddr, clientID []byte, hostname string, staticIP net.IP) (net.IP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if staticIP == nil || staticIP.To4() == nil {
		return nil, ErrIPNotAvailable
	}
	staticIP4 := staticIP.To4()
	macKey := mac.String()
	ipKey := staticIP4.String()

	// 1. 优先复用该 MAC 的有效租约（续约场景）
	if existing, ok := s.macIndex[macKey]; ok && existing.Status == LeaseActive {
		if existing.IP.Equal(staticIP4) {
			return existing.IP, nil
		}
		// 已有租约但 IP 不匹配（不应发生,理论上同一 MAC 只有一条固定映射）→ 清理旧租约
		delete(s.leases, existing.IP.String())
		delete(s.macIndex, macKey)
		if len(existing.ClientID) > 0 {
			delete(s.clientIDIndex, string(existing.ClientID))
		}
	}

	// 2. 通过 ClientID 查找有效租约
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if existing, ok := s.clientIDIndex[cidKey]; ok && existing.Status == LeaseActive {
			if existing.IP.Equal(staticIP4) {
				return existing.IP, nil
			}
			delete(s.leases, existing.IP.String())
			delete(s.macIndex, existing.MAC.String())
			delete(s.clientIDIndex, cidKey)
		}
	}

	// 3. 复用该 MAC 已有的 Pending Offer（刷新过期时间）
	if offer, ok := s.offerMAC[macKey]; ok {
		if offer.IP.Equal(staticIP4) {
			offer.ExpiresAt = time.Now().Add(PendingOfferTimeout)
			if hostname != "" {
				offer.Hostname = hostname
			}
			return offer.IP, nil
		}
		// 旧 offer 的 IP 不匹配（不应发生）→ 清理旧 offer
		delete(s.offers, offer.IP.String())
		delete(s.offerMAC, macKey)
		if len(offer.ClientID) > 0 {
			delete(s.offerClientID, string(offer.ClientID))
		}
	}

	// 4. 通过 ClientID 查找 Pending Offer
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if offer, ok := s.offerClientID[cidKey]; ok {
			if offer.IP.Equal(staticIP4) {
				offer.ExpiresAt = time.Now().Add(PendingOfferTimeout)
				if hostname != "" {
					offer.Hostname = hostname
				}
				return offer.IP, nil
			}
			delete(s.offers, offer.IP.String())
			delete(s.offerMAC, offer.MAC.String())
			delete(s.offerClientID, cidKey)
		}
	}

	// 5. 检查 staticIP 是否被其他客户端占用（活跃租约）
	if existing, ok := s.leases[ipKey]; ok && existing.Status == LeaseActive {
		if existing.MAC.String() != macKey {
			// 被其他客户端占用（不应发生,因为该 IP 已被排除）
			return nil, ErrIPNotAvailable
		}
	}

	// 6. 检查 staticIP 是否被其他客户端的 Pending Offer 占用
	if offer, ok := s.offers[ipKey]; ok {
		if offer.MAC.String() != macKey {
			return nil, ErrIPNotAvailable
		}
		// 同一客户端的 offer,上面 3/4 已处理,此处保险转换
	}

	// 7. 创建 Pending Offer
	offer := &PendingOffer{
		IP:        staticIP4,
		MAC:       mac,
		ClientID:  clientID,
		Hostname:  hostname,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(PendingOfferTimeout),
	}
	s.offers[ipKey] = offer
	s.offerMAC[macKey] = offer
	if len(clientID) > 0 {
		s.offerClientID[string(clientID)] = offer
	}
	return staticIP4, nil
}

// ConfirmStaticLease 确认固定映射租约（REQUEST 阶段，V1.0.3新增）
// 用于固定映射命中时,绕过 isInPool/isExcluded 检查直接确认租约
// requestedIP 必须等于 staticIP,调用方负责校验;不匹配返回 ErrIPNotAvailable
func (s *LeaseStore) ConfirmStaticLease(mac net.HardwareAddr, clientID []byte, hostname string, requestedIP net.IP, staticIP net.IP) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if requestedIP == nil || staticIP == nil {
		return nil, ErrIPNotAvailable
	}
	reqIP4 := requestedIP.To4()
	staticIP4 := staticIP.To4()
	if reqIP4 == nil || staticIP4 == nil {
		return nil, ErrIPNotAvailable
	}

	// 请求的 IP 必须等于固定映射 IP
	if !reqIP4.Equal(staticIP4) {
		return nil, ErrIPNotAvailable
	}

	macKey := mac.String()
	ipKey := reqIP4.String()

	// 1. 该 MAC 是否已有该 IP 的活跃租约 → 续约
	if existing, ok := s.macIndex[macKey]; ok && existing.Status == LeaseActive {
		if existing.IP.Equal(reqIP4) {
			existing.AssignedAt = time.Now()
			existing.ExpiresAt = time.Now().Add(s.leaseDur)
			if hostname != "" {
				existing.Hostname = hostname
			}
			return existing, nil
		}
	}

	// 2. 通过 ClientID 查找活跃租约
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if existing, ok := s.clientIDIndex[cidKey]; ok && existing.Status == LeaseActive {
			if existing.IP.Equal(reqIP4) {
				existing.AssignedAt = time.Now()
				existing.ExpiresAt = time.Now().Add(s.leaseDur)
				if hostname != "" {
					existing.Hostname = hostname
				}
				return existing, nil
			}
		}
	}

	// 3. 该 MAC 是否有该 IP 的 Pending Offer → 转为活跃租约
	if offer, ok := s.offerMAC[macKey]; ok && offer.IP.Equal(reqIP4) {
		return s.convertOfferToLease(offer, mac, clientID, hostname), nil
	}

	// 4. 通过 ClientID 查找 Pending Offer
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if offer, ok := s.offerClientID[cidKey]; ok && offer.IP.Equal(reqIP4) {
			return s.convertOfferToLease(offer, mac, clientID, hostname), nil
		}
	}

	// 5. 检查该 IP 是否被其他客户端占用（活跃租约）→ NAK
	if existing, ok := s.leases[ipKey]; ok && existing.Status == LeaseActive {
		if existing.MAC.String() != macKey {
			return nil, ErrIPNotAvailable
		}
	}

	// 6. 检查该 IP 是否被其他客户端的 Pending Offer 占用
	if offer, ok := s.offers[ipKey]; ok {
		if offer.MAC.String() != macKey {
			return nil, ErrIPNotAvailable
		}
		// 同一客户端的 offer（保险起见转换）
		return s.convertOfferToLease(offer, mac, clientID, hostname), nil
	}

	// 7. IP 空闲,直接创建活跃租约（绕过 isInPool/isExcluded 检查）
	lease := s.createLease(reqIP4, mac, clientID, hostname)
	return lease, nil
}

// CreatePendingOffer 为客户端创建 Pending Offer（DISCOVER 阶段）
// V1修复: DISCOVER 仅创建 Pending Offer，不直接创建活跃租约
// 同一客户端重复 DISCOVER 优先复用已有的 Pending Offer 或有效租约
func (s *LeaseStore) CreatePendingOffer(mac net.HardwareAddr, clientID []byte, hostname string) (net.IP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	macKey := mac.String()

	// 1. 优先复用该 MAC 的有效租约（续约场景：客户端已有租约，发 DISCOVER 是在寻找可续约的服务器）
	if existing, ok := s.macIndex[macKey]; ok && existing.Status == LeaseActive {
		return existing.IP, nil
	}

	// 2. 通过 ClientID 查找有效租约
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if existing, ok := s.clientIDIndex[cidKey]; ok && existing.Status == LeaseActive {
			return existing.IP, nil
		}
	}

	// 3. V1修复: 复用已有的 Pending Offer（同一客户端重复 DISCOVER 不会重复分配）
	if offer, ok := s.offerMAC[macKey]; ok {
		offer.ExpiresAt = time.Now().Add(PendingOfferTimeout)
		if hostname != "" {
			offer.Hostname = hostname
		}
		return offer.IP, nil
	}
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if offer, ok := s.offerClientID[cidKey]; ok {
			offer.ExpiresAt = time.Now().Add(PendingOfferTimeout)
			if hostname != "" {
				offer.Hostname = hostname
			}
			return offer.IP, nil
		}
	}

	// 4. 分配新 IP，创建 Pending Offer
	for ipVal := s.pool[0]; ipVal <= s.pool[1]; ipVal++ {
		if s.isExcluded(ipVal) {
			continue
		}
		ip := uint32ToIP(ipVal)
		ipKey := ip.String()

		// 跳过已有活跃租约的 IP
		if _, ok := s.leases[ipKey]; ok {
			continue
		}
		// 跳过已有 Pending Offer 的 IP（其他客户端）
		if _, ok := s.offers[ipKey]; ok {
			continue
		}

		// 创建 Pending Offer
		offer := &PendingOffer{
			IP:        ip,
			MAC:       mac,
			ClientID:  clientID,
			Hostname:  hostname,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(PendingOfferTimeout),
		}
		s.offers[ipKey] = offer
		s.offerMAC[macKey] = offer
		if len(clientID) > 0 {
			s.offerClientID[string(clientID)] = offer
		}
		return ip, nil
	}

	// 地址池耗尽
	return nil, ErrPoolExhausted
}

// ConfirmLease 确认租约（REQUEST 阶段）
// V1修复: 只能确认请求的指定 IP，不可分配则返回错误用于 NAK，禁止自动更换其他 IP 后 ACK
func (s *LeaseStore) ConfirmLease(mac net.HardwareAddr, clientID []byte, hostname string, requestedIP net.IP) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if requestedIP == nil {
		return nil, ErrIPNotAvailable
	}

	macKey := mac.String()
	reqVal := ipToUint32(requestedIP.To4())
	ipKey := requestedIP.To4().String()

	// 检查请求的 IP 是否在地址池内
	if !s.isInPool(reqVal) {
		return nil, ErrIPNotAvailable
	}

	// 检查请求的 IP 是否被排除（网络地址、广播地址、服务端 IP）
	if s.isExcluded(reqVal) {
		return nil, ErrIPNotAvailable
	}

	// 1. 该 MAC 是否已有该 IP 的活跃租约 → 续约
	if existing, ok := s.macIndex[macKey]; ok && existing.Status == LeaseActive {
		if existing.IP.Equal(requestedIP) {
			existing.AssignedAt = time.Now()
			existing.ExpiresAt = time.Now().Add(s.leaseDur)
			if hostname != "" {
				existing.Hostname = hostname
			}
			return existing, nil
		}
	}

	// 2. 通过 ClientID 查找活跃租约
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if existing, ok := s.clientIDIndex[cidKey]; ok && existing.Status == LeaseActive {
			if existing.IP.Equal(requestedIP) {
				existing.AssignedAt = time.Now()
				existing.ExpiresAt = time.Now().Add(s.leaseDur)
				if hostname != "" {
					existing.Hostname = hostname
				}
				return existing, nil
			}
		}
	}

	// 3. 该 MAC 是否有该 IP 的 Pending Offer → 转为活跃租约
	if offer, ok := s.offerMAC[macKey]; ok && offer.IP.Equal(requestedIP) {
		return s.convertOfferToLease(offer, mac, clientID, hostname), nil
	}

	// 4. 通过 ClientID 查找 Pending Offer
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if offer, ok := s.offerClientID[cidKey]; ok && offer.IP.Equal(requestedIP) {
			return s.convertOfferToLease(offer, mac, clientID, hostname), nil
		}
	}

	// 5. 检查该 IP 是否被其他客户端占用（活跃租约）→ V1修复: 请求已占用 IP 不得 ACK 其他 IP，只能 NAK
	if existing, ok := s.leases[ipKey]; ok && existing.Status == LeaseActive {
		return nil, ErrIPNotAvailable
	}

	// 6. 检查该 IP 是否被其他客户端的 Pending Offer 占用
	if offer, ok := s.offers[ipKey]; ok {
		if offer.MAC.String() != macKey {
			// IP 正在被其他客户端 Offer → NAK
			return nil, ErrIPNotAvailable
		}
		// 同一客户端的 offer（保险起见转换）
		return s.convertOfferToLease(offer, mac, clientID, hostname), nil
	}

	// 7. IP 空闲且在池内（INIT-REBOOT 场景）→ 直接创建活跃租约
	lease := s.createLease(requestedIP.To4(), mac, clientID, hostname)
	return lease, nil
}

// convertOfferToLease 将 Pending Offer 转换为活跃租约
func (s *LeaseStore) convertOfferToLease(offer *PendingOffer, mac net.HardwareAddr, clientID []byte, hostname string) *Lease {
	// 清理该 offer 的索引
	ipKey := offer.IP.String()
	macKey := offer.MAC.String()
	delete(s.offers, ipKey)
	delete(s.offerMAC, macKey)
	if len(offer.ClientID) > 0 {
		delete(s.offerClientID, string(offer.ClientID))
	}

	// 创建活跃租约
	lease := s.createLease(offer.IP, mac, clientID, hostname)
	return lease
}

// createLease 创建新租约并加入索引
func (s *LeaseStore) createLease(ip net.IP, mac net.HardwareAddr, clientID []byte, hostname string) *Lease {
	lease := &Lease{
		IP:         ip,
		MAC:        mac,
		ClientID:   clientID,
		Hostname:   hostname,
		AssignedAt: time.Now(),
		ExpiresAt:  time.Now().Add(s.leaseDur),
		Status:     LeaseActive,
	}

	ipKey := ip.String()
	macKey := mac.String()

	// 清除该 IP 的旧租约索引
	if old, ok := s.leases[ipKey]; ok {
		delete(s.macIndex, old.MAC.String())
		if len(old.ClientID) > 0 {
			delete(s.clientIDIndex, string(old.ClientID))
		}
	}

	// 清除该 MAC 的旧租约
	if old, ok := s.macIndex[macKey]; ok {
		delete(s.leases, old.IP.String())
		if len(old.ClientID) > 0 {
			delete(s.clientIDIndex, string(old.ClientID))
		}
	}

	// 清除该 MAC 的 Pending Offer
	if offer, ok := s.offerMAC[macKey]; ok {
		delete(s.offers, offer.IP.String())
		delete(s.offerMAC, macKey)
		if len(offer.ClientID) > 0 {
			delete(s.offerClientID, string(offer.ClientID))
		}
	}

	// 清除该 ClientID 的旧租约
	if len(clientID) > 0 {
		cidKey := string(clientID)
		if old, ok := s.clientIDIndex[cidKey]; ok {
			delete(s.leases, old.IP.String())
			delete(s.macIndex, old.MAC.String())
		}
		// 清除该 ClientID 的 Pending Offer
		if offer, ok := s.offerClientID[cidKey]; ok {
			delete(s.offers, offer.IP.String())
			delete(s.offerMAC, offer.MAC.String())
			delete(s.offerClientID, cidKey)
		}
	}

	s.leases[ipKey] = lease
	s.macIndex[macKey] = lease
	if len(clientID) > 0 {
		s.clientIDIndex[string(clientID)] = lease
	}

	return lease
}

// Release 释放客户端的租约
func (s *LeaseStore) Release(mac net.HardwareAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	macKey := mac.String()

	// 释放活跃租约
	if lease, ok := s.macIndex[macKey]; ok {
		lease.Status = LeaseExpired
		delete(s.leases, lease.IP.String())
		delete(s.macIndex, macKey)
		if len(lease.ClientID) > 0 {
			delete(s.clientIDIndex, string(lease.ClientID))
		}
	}

	// 释放 Pending Offer
	if offer, ok := s.offerMAC[macKey]; ok {
		delete(s.offers, offer.IP.String())
		delete(s.offerMAC, macKey)
		if len(offer.ClientID) > 0 {
			delete(s.offerClientID, string(offer.ClientID))
		}
	}
}

// LookupByMAC 通过 MAC 查找租约
func (s *LeaseStore) LookupByMAC(mac net.HardwareAddr) *Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lease, ok := s.macIndex[mac.String()]
	if !ok {
		return nil
	}
	return lease
}

// IsIPAllocated 检查 IP 是否已被分配
func (s *LeaseStore) IsIPAllocated(ip net.IP) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lease, ok := s.leases[ip.To4().String()]
	if !ok {
		return false
	}
	return lease.Status == LeaseActive
}

// ExpireLeases 清理过期租约
func (s *LeaseStore) ExpireLeases() []LeaseJSON {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []LeaseJSON
	now := time.Now()

	for ipKey, lease := range s.leases {
		if lease.Status == LeaseActive && now.After(lease.ExpiresAt) {
			lease.Status = LeaseExpired
			delete(s.macIndex, lease.MAC.String())
			if len(lease.ClientID) > 0 {
				delete(s.clientIDIndex, string(lease.ClientID))
			}
			expired = append(expired, lease.ToJSON())
			delete(s.leases, ipKey)
		}
	}

	return expired
}

// ExpirePendingOrders 清理超时的 Pending Offer
// V1修复: Pending Offer 默认保留60秒，超时自动释放
func (s *LeaseStore) ExpirePendingOffers() []LeaseJSON {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []LeaseJSON
	now := time.Now()

	for ipKey, offer := range s.offers {
		if now.After(offer.ExpiresAt) {
			expired = append(expired, LeaseJSON{
				IP:     offer.IP.String(),
				MAC:    offer.MAC.String(),
				Status: "pending_expired",
			})
			delete(s.offerMAC, offer.MAC.String())
			if len(offer.ClientID) > 0 {
				delete(s.offerClientID, string(offer.ClientID))
			}
			delete(s.offers, ipKey)
		}
	}

	return expired
}

// List 返回所有活跃租约（V1修复: 不含 Pending Offer，租约列表不显示未完成 REQUEST 的客户端）
func (s *LeaseStore) List() []LeaseJSON {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []LeaseJSON
	for _, lease := range s.leases {
		if lease.Status == LeaseActive {
			result = append(result, lease.ToJSON())
		}
	}
	return result
}

// PoolStats 返回地址池统计信息
// V1修复: 使用起止地址差值计算总量，仅统计实际租约和 Pending Offer，不得逐个遍历巨大地址池
// V1.0.3修复: used 只统计 IP 在动态地址池范围内、且不是 reserved/excluded 的动态租约和 Pending Offer;
//
//	池外固定映射租约仍保留在 leases/offers 中供列表展示,但不计入动态池已用数;
//	池内固定映射租约(其 IP 已加入 excluded 集合)同样不计入 used,避免统计不一致
func (s *LeaseStore) PoolStats() (total int, used int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 总量 = 起止差值 + 1 - 池内排除地址数量(含池内保留 IP)
	total = int(s.pool[1]-s.pool[0]+1) - s.excludedInPool

	// 已用 = 池内且非 excluded 的活跃租约 + 池内且非 excluded 的 Pending Offer
	// V1.0.3修复: 池内保留 IP 的租约虽在 leases/offers 中,但其 IP 在 excluded 集合,不计入 used
	used = 0
	for _, l := range s.leases {
		if l == nil {
			continue
		}
		ipVal := ipToUint32(l.IP.To4())
		if s.isInPool(ipVal) && !s.isExcluded(ipVal) {
			used++
		}
	}
	for _, o := range s.offers {
		if o == nil {
			continue
		}
		ipVal := ipToUint32(o.IP.To4())
		if s.isInPool(ipVal) && !s.isExcluded(ipVal) {
			used++
		}
	}

	return
}

// isInPool 检查 IP 是否在地址池范围内（使用 uint32 数值比较）
func (s *LeaseStore) isInPool(ipVal uint32) bool {
	return ipVal >= s.pool[0] && ipVal <= s.pool[1]
}

// isExcluded 检查 IP 是否被排除
func (s *LeaseStore) isExcluded(ipVal uint32) bool {
	return s.excluded[ipVal]
}

// ipToUint32 将 IPv4 转换为 uint32
func ipToUint32(ip net.IP) uint32 {
	if ip == nil {
		return 0
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip4)
}

// uint32ToIP 将 uint32 转换为 IPv4
func uint32ToIP(val uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, val)
	return ip
}

// 错误定义
var ErrPoolExhausted = errPoolExhausted()

type errPoolExhaustedType struct{}

func (errPoolExhaustedType) Error() string { return "dhcp: address pool exhausted" }

func errPoolExhausted() error { return errPoolExhaustedType{} }

// ErrIPNotAvailable 请求的 IP 不可用（V1修复: 用于 NAK 响应）
var ErrIPNotAvailable = errIPNotAvailable()

type errIPNotAvailableType struct{}

func (errIPNotAvailableType) Error() string { return "dhcp: requested IP not available" }

func errIPNotAvailable() error { return errIPNotAvailableType{} }
