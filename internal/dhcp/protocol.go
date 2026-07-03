package dhcp

import (
	"encoding/binary"
	"errors"
	"net"
)

// DHCP 消息类型
const (
	MsgTypeDiscover = 1
	MsgTypeOffer    = 2
	MsgTypeRequest  = 3
	MsgTypeDecline  = 4
	MsgTypeACK      = 5
	MsgTypeNAK      = 6
	MsgTypeRelease  = 7
	MsgTypeInform   = 8
)

// DHCP 操作码
const (
	OpRequest = 1 // 客户端到服务器
	OpReply   = 2 // 服务器到客户端
)

// DHCP 选项代码
const (
	OptPad           = 0
	OptSubnetMask    = 1
	OptRouter        = 3
	OptDNSServer     = 6
	OptHostName      = 12
	OptDomainName    = 15
	OptBroadcastAddr = 28
	OptRequestedIP   = 50
	OptLeaseTime     = 51
	OptMsgType       = 53
	OptServerID      = 54
	OptParameterList = 55
	OptRenewalTime   = 58
	OptRebindingTime = 59
	OptClientID      = 61
	OptEnd           = 255
)

// DHCP 硬件类型
const (
	HwTypeEthernet = 1
)

// DHCP 包固定头部大小
const (
	HeaderLen    = 236
	MinPacketLen = 240 // 236 + 4 (magic cookie)
	MaxPacketLen = 576 // DHCP 最小 MTU
	MagicCookie  = 0x63825363
)

// Packet 表示一个 DHCP 数据包
type Packet struct {
	Op         byte
	HType      byte
	HLen       byte
	Hops       byte
	XID        uint32
	Secs       uint16
	Flags      uint16
	CIAddr     net.IP
	YIAddr     net.IP
	SIAddr     net.IP
	GIAddr     net.IP
	CHAddr     net.HardwareAddr
	SName      string
	File       string
	Options    map[byte][]byte
	RawOptions []byte // 原始选项字节，用于未识别选项
}

// BroadcastFlag 检查是否设置了广播标志
func (p *Packet) BroadcastFlag() bool {
	return p.Flags&0x8000 != 0
}

// MessageType 获取 DHCP 消息类型选项
func (p *Packet) MessageType() (byte, bool) {
	val, ok := p.Options[OptMsgType]
	if !ok || len(val) == 0 {
		return 0, false
	}
	return val[0], true
}

// RequestedIP 获取请求的 IP 地址选项
func (p *Packet) RequestedIP() net.IP {
	val, ok := p.Options[OptRequestedIP]
	if !ok || len(val) < 4 {
		return nil
	}
	return net.IP(val[:4])
}

// ServerID 获取服务器标识选项
func (p *Packet) ServerID() net.IP {
	val, ok := p.Options[OptServerID]
	if !ok || len(val) < 4 {
		return nil
	}
	return net.IP(val[:4])
}

// ClientID 获取客户端标识选项
func (p *Packet) ClientID() []byte {
	val, ok := p.Options[OptClientID]
	if !ok {
		return nil
	}
	return val
}

// HostName 获取主机名选项
func (p *Packet) HostName() string {
	val, ok := p.Options[OptHostName]
	if !ok {
		return ""
	}
	return string(val)
}

// LeaseTime 获取请求的租约时间选项
func (p *Packet) LeaseTime() uint32 {
	val, ok := p.Options[OptLeaseTime]
	if !ok || len(val) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(val[:4])
}

// ParsePacket 从字节切片解析 DHCP 包
func ParsePacket(data []byte) (*Packet, error) {
	if len(data) < MinPacketLen {
		return nil, errors.New("dhcp: packet too short")
	}

	p := &Packet{
		Op:     data[0],
		HType:  data[1],
		HLen:   data[2],
		Hops:   data[3],
		XID:    binary.BigEndian.Uint32(data[4:8]),
		Secs:   binary.BigEndian.Uint16(data[8:10]),
		Flags:  binary.BigEndian.Uint16(data[10:12]),
		CIAddr: net.IP(data[12:16]),
		YIAddr: net.IP(data[16:20]),
		SIAddr: net.IP(data[20:24]),
		GIAddr: net.IP(data[24:28]),
		SName:  string(trimNull(data[44:108])),
		File:   string(trimNull(data[108:236])),
	}

	// 解析硬件地址（通常为 MAC 地址，6 字节）
	hwLen := int(p.HLen)
	if hwLen > 16 {
		hwLen = 16
	}
	mac := make(net.HardwareAddr, hwLen)
	copy(mac, data[28:28+hwLen])
	p.CHAddr = mac

	// 验证 Magic Cookie
	cookie := binary.BigEndian.Uint32(data[236:240])
	if cookie != MagicCookie {
		return nil, errors.New("dhcp: invalid magic cookie")
	}

	// 解析选项
	p.Options = make(map[byte][]byte)
	opts := data[240:]
	i := 0
	for i < len(opts) {
		code := opts[i]
		i++
		if code == OptPad {
			continue
		}
		if code == OptEnd {
			break
		}
		if i >= len(opts) {
			break
		}
		length := int(opts[i])
		i++
		if i+length > len(opts) {
			break
		}
		val := make([]byte, length)
		copy(val, opts[i:i+length])
		p.Options[code] = val
		i += length
	}

	return p, nil
}

// ReplyOptions 封装 OFFER/ACK 报文可选下发参数（V2新增: 支持可选网关和DNS）
// 网关和DNS为空时保持原 V1 行为（不下发 Option 3/6）
type ReplyOptions struct {
	LeaseTime  uint32     // 租约时间（秒），0 表示不下发租约相关选项
	SubnetMask net.IPMask // 子网掩码（Option 1），nil 表示不下发
	Router     net.IP     // 网关（Option 3），nil 表示不下发
	DNSServers []net.IP   // DNS 服务器（Option 6），nil/空 表示不下发；多个按顺序拼接
}

// BuildPacket 构建 DHCP 响应包
// V1修复: siaddr 保持 0.0.0.0，仅通过 Option 54 声明 Server Identifier
// V2新增: 通过 ReplyOptions 携带可选网关(Option 3)和DNS(Option 6)，非空才下发
// NAK 报文应传入零值 ReplyOptions（不下发租约时间、子网掩码、网关、DNS）
func BuildPacket(msgType byte, xid uint32, flags uint16,
	clientIP, yourIP, serverIP net.IP,
	clientMAC net.HardwareAddr,
	opts ReplyOptions) []byte {

	buf := make([]byte, 576) // DHCP 最大包长度
	for i := range buf {
		buf[i] = 0
	}

	// 固定头部
	buf[0] = OpReply
	buf[1] = HwTypeEthernet
	buf[2] = 6 // MAC 地址长度
	buf[3] = 0 // Hops
	binary.BigEndian.PutUint32(buf[4:8], xid)
	binary.BigEndian.PutUint16(buf[8:10], 0) // Secs
	binary.BigEndian.PutUint16(buf[10:12], flags)

	// 地址字段：统一使用 To4 结果判断，nil 和 IPv6 地址不会错误复制到 IPv4 字段
	if ip4 := clientIP.To4(); ip4 != nil {
		copy(buf[12:16], ip4)
	}
	if ip4 := yourIP.To4(); ip4 != nil {
		copy(buf[16:20], ip4)
	}
	// siaddr (buf[20:24]) 保持 0.0.0.0，不设置
	// GIAddr 保持为 0

	// 客户端硬件地址
	if clientMAC != nil {
		copy(buf[28:28+len(clientMAC)], clientMAC)
	}

	// Magic Cookie
	binary.BigEndian.PutUint32(buf[236:240], MagicCookie)

	// 选项
	offset := 240

	// DHCP Message Type
	offset = appendOption(buf, offset, OptMsgType, []byte{msgType})

	// Server Identifier (Option 54) — 仅通过此选项声明服务端标识
	if serverIP != nil {
		offset = appendOption(buf, offset, OptServerID, serverIP.To4())
	}

	// Lease Time
	if opts.LeaseTime > 0 {
		ltBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(ltBuf, opts.LeaseTime)
		offset = appendOption(buf, offset, OptLeaseTime, ltBuf)
	}

	// Renewal Time (T1) = 50% lease time
	if opts.LeaseTime > 0 {
		t1 := make([]byte, 4)
		binary.BigEndian.PutUint32(t1, opts.LeaseTime/2)
		offset = appendOption(buf, offset, OptRenewalTime, t1)
	}

	// Rebinding Time (T2) = 87.5% lease time
	if opts.LeaseTime > 0 {
		t2 := make([]byte, 4)
		binary.BigEndian.PutUint32(t2, opts.LeaseTime*7/8)
		offset = appendOption(buf, offset, OptRebindingTime, t2)
	}

	// Subnet Mask
	if opts.SubnetMask != nil {
		offset = appendOption(buf, offset, OptSubnetMask, opts.SubnetMask)
	}

	// V2新增: 网关非空才下发 Option 3 (Router)
	if opts.Router != nil {
		if r := opts.Router.To4(); r != nil {
			offset = appendOption(buf, offset, OptRouter, r)
		}
	}

	// V2新增: DNS 非空才下发 Option 6，多个 DNS 按顺序拼接为连续 IPv4 字节
	if len(opts.DNSServers) > 0 {
		dnsBuf := make([]byte, 0, len(opts.DNSServers)*4)
		for _, dns := range opts.DNSServers {
			if dns == nil {
				continue
			}
			if d := dns.To4(); d != nil {
				dnsBuf = append(dnsBuf, d...)
			}
		}
		if len(dnsBuf) > 0 {
			offset = appendOption(buf, offset, OptDNSServer, dnsBuf)
		}
	}

	// End
	buf[offset] = OptEnd
	offset++

	return buf[:offset]
}

// appendOption 向缓冲区追加一个 DHCP 选项
func appendOption(buf []byte, offset int, code byte, data []byte) int {
	buf[offset] = code
	offset++
	buf[offset] = byte(len(data))
	offset++
	copy(buf[offset:offset+len(data)], data)
	offset += len(data)
	return offset
}

// trimNull 去除 null 字节
func trimNull(b []byte) []byte {
	for i := range b {
		if b[i] == 0 {
			return b[:i]
		}
	}
	return b
}
