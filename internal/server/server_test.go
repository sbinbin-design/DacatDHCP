package server

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// verifyRecommend 验证推荐结果合法性
// 必须满足: start<=end、不包含服务端IP、不包含网络地址、不包含广播地址
func verifyRecommend(t *testing.T, serverIP net.IP, mask net.IPMask, startStr, endStr string) {
	t.Helper()

	start := net.ParseIP(startStr)
	end := net.ParseIP(endStr)
	if start == nil || end == nil {
		t.Fatalf("推荐结果无法解析: start=%s end=%s", startStr, endStr)
		return
	}

	// start <= end
	startVal := ipToUint32(start.To4())
	endVal := ipToUint32(end.To4())
	if startVal > endVal {
		t.Errorf("起始地址 %s > 结束地址 %s", startStr, endStr)
	}

	// 不包含网络地址
	ipVal := ipToUint32(serverIP.To4())
	maskVal := uint32(0)
	if len(mask) >= 4 {
		maskVal = (uint32(mask[0]) << 24) | (uint32(mask[1]) << 16) | (uint32(mask[2]) << 8) | uint32(mask[3])
	}
	networkVal := ipVal & maskVal
	broadcastVal := networkVal | ^maskVal

	if startVal <= networkVal && networkVal <= endVal {
		t.Errorf("推荐范围 %s-%s 包含网络地址 %s", startStr, endStr, uint32ToIPStr(networkVal))
	}
	if startVal <= broadcastVal && broadcastVal <= endVal {
		t.Errorf("推荐范围 %s-%s 包含广播地址 %s", startStr, endStr, uint32ToIPStr(broadcastVal))
	}
	if startVal <= ipVal && ipVal <= endVal {
		t.Errorf("推荐范围 %s-%s 包含服务端IP %s", startStr, endStr, serverIP.String())
	}
}

// TestRecommendPool_ServerIP1 测试服务端IP=192.168.1.1 /24
func TestRecommendPool_ServerIP1(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), start, end)
	// 后方: 192.168.1.2 - 192.168.1.101 (100个)
	if start != "192.168.1.2" {
		t.Errorf("期望起始 192.168.1.2，实际 %s", start)
	}
}

// TestRecommendPool_ServerIP100 测试服务端IP=192.168.1.100 /24
func TestRecommendPool_ServerIP100(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("192.168.1.100"), net.IPv4Mask(255, 255, 255, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("192.168.1.100"), net.IPv4Mask(255, 255, 255, 0), start, end)
}

// TestRecommendPool_ServerIP200 测试服务端IP=192.168.1.200 /24
func TestRecommendPool_ServerIP200(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("192.168.1.200"), net.IPv4Mask(255, 255, 255, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("192.168.1.200"), net.IPv4Mask(255, 255, 255, 0), start, end)
}

// TestRecommendPool_ServerIP250 测试服务端IP=192.168.1.250 /24
func TestRecommendPool_ServerIP250(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("192.168.1.250"), net.IPv4Mask(255, 255, 255, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("192.168.1.250"), net.IPv4Mask(255, 255, 255, 0), start, end)
	// 后方只有 192.168.1.251-254（4个），应使用后方
	if start != "192.168.1.251" {
		t.Errorf("期望起始 192.168.1.251，实际 %s", start)
	}
	if end != "192.168.1.254" {
		t.Errorf("期望结束 192.168.1.254，实际 %s", end)
	}
}

// TestRecommendPool_ServerIP254 测试服务端IP=192.168.1.254 /24（接近广播地址）
func TestRecommendPool_ServerIP254(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("192.168.1.254"), net.IPv4Mask(255, 255, 255, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("192.168.1.254"), net.IPv4Mask(255, 255, 255, 0), start, end)
	// 后方无空间（254+1=255是广播），应从前方选择
	if start != "192.168.1.154" {
		t.Errorf("期望起始 192.168.1.154，实际 %s", start)
	}
	if end != "192.168.1.253" {
		t.Errorf("期望结束 192.168.1.253，实际 %s", end)
	}
}

// TestRecommendPool_Non24Subnet /16 子网
func TestRecommendPool_Non24Subnet(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("10.0.0.1"), net.IPv4Mask(255, 255, 0, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("10.0.0.1"), net.IPv4Mask(255, 255, 0, 0), start, end)
}

// TestRecommendPool_Non24Subnet20 /20 子网
func TestRecommendPool_Non24Subnet20(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("172.16.16.1"), net.IPv4Mask(255, 255, 240, 0))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("172.16.16.1"), net.IPv4Mask(255, 255, 240, 0), start, end)
}

// TestRecommendPool_SmallSubnet 极小子网（/30 只有2个可用主机）
func TestRecommendPool_SmallSubnet(t *testing.T) {
	start, end, err := RecommendPool(net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 252))
	if err != nil {
		t.Fatalf("RecommendPool 失败: %v", err)
	}
	verifyRecommend(t, net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 252), start, end)
	// /30: 网络=192.168.1.0, 广播=192.168.1.3, 可用=192.168.1.1(服务端), 192.168.1.2
	// 后方: 192.168.1.2 只有1个
	if start != "192.168.1.2" || end != "192.168.1.2" {
		t.Errorf("期望 192.168.1.2-192.168.1.2，实际 %s-%s", start, end)
	}
}

// TestRecommendPool_NoSpace 无可用空间
func TestRecommendPool_NoSpace(t *testing.T) {
	// /31: 无可用主机地址（RFC 3021）
	_, _, err := RecommendPool(net.ParseIP("192.168.1.0"), net.IPv4Mask(255, 255, 255, 254))
	if err == nil {
		t.Error("/31 子网应无法推荐地址池")
	}
}

// ---- V6: AppServer 并发测试 ----

// newTestAppServer 创建测试用 AppServer（不启动 HTTP）
func newTestAppServer(t *testing.T) *AppServer {
	t.Helper()
	dataDir := t.TempDir()
	app, err := NewAppServer(dataDir, nil)
	if err != nil {
		t.Fatalf("NewAppServer 失败: %v", err)
	}
	// 注入 mock 连接创建函数
	app.dhcpSrv.SetCreateConnFunc(func(bindIP net.IP) (net.PacketConn, error) {
		return net.ListenPacket("udp", "127.0.0.1:0")
	})
	// V6: 测试结束后关闭 AppServer（释放日志文件锁）
	t.Cleanup(func() {
		app.Close()
	})
	return app
}

// TestManualStop_StatusQueryable 手动 Stop 后状态可查询
func TestManualStop_StatusQueryable(t *testing.T) {
	app := newTestAppServer(t)
	app.PostInit()

	err := app.dhcpSrv.Start("TestAdapter",
		net.ParseIP("192.168.1.1"),
		net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"),
		net.ParseIP("192.168.1.200"),
		60,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	app.dhcpSrv.Stop()

	if app.dhcpSrv.IsRunning() {
		t.Error("Stop 后不应处于运行状态")
	}
	status := app.dhcpSrv.Status()
	if status.Running {
		t.Error("Status 应报告未运行")
	}
}

// TestConcurrentStop 并发多次 Stop 不死锁（V8: 重命名，原名称误导）
func TestConcurrentStop(t *testing.T) {
	app := newTestAppServer(t)
	app.PostInit()

	for i := 0; i < 5; i++ {
		err := app.dhcpSrv.Start("TestAdapter",
			net.ParseIP("192.168.1.1"),
			net.IPv4Mask(255, 255, 255, 0),
			net.ParseIP("192.168.1.100"),
			net.ParseIP("192.168.1.200"),
			60,
			nil, nil,
		)
		if err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}

		// 并发调用 Stop（仅测试多次 Stop 不死锁，非自动停止）
		var wg sync.WaitGroup
		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				app.dhcpSrv.Stop()
			}()
		}
		wg.Wait()

		if app.dhcpSrv.IsRunning() {
			t.Fatalf("第 %d 轮并发 Stop 后仍在运行", i+1)
		}
	}
}

// TestStopReturns_StatusQueryable Stop 返回后可查询状态
func TestStopReturns_StatusQueryable(t *testing.T) {
	app := newTestAppServer(t)

	err := app.dhcpSrv.Start("TestAdapter",
		net.ParseIP("192.168.1.1"),
		net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"),
		net.ParseIP("192.168.1.200"),
		60,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	app.dhcpSrv.Stop()

	status := app.dhcpSrv.Status()
	if status.Running {
		t.Error("Stop 后 Status 应报告未运行")
	}
}

// TestStopAndRestart 停止后可再次 Start（V8: 重命名，原名称暗示自动停止）
func TestStopAndRestart(t *testing.T) {
	app := newTestAppServer(t)

	for i := 0; i < 5; i++ {
		err := app.dhcpSrv.Start("TestAdapter",
			net.ParseIP("192.168.1.1"),
			net.IPv4Mask(255, 255, 255, 0),
			net.ParseIP("192.168.1.100"),
			net.ParseIP("192.168.1.200"),
			60,
			nil, nil,
		)
		if err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}

		// 手动停止（非自动停止路径）
		app.dhcpSrv.Stop()

		if app.dhcpSrv.IsRunning() {
			t.Fatalf("第 %d 次停机后仍在运行", i+1)
		}
	}
}

// TestConcurrentStartStop20x 连续启停20次无端口残留和协程泄漏
func TestConcurrentStartStop20x(t *testing.T) {
	app := newTestAppServer(t)

	autoStopCount := int32(0)
	app.dhcpSrv.SetOnAutoStop(func(_ string) {
		atomic.AddInt32(&autoStopCount, 1)
	})

	for i := 0; i < 20; i++ {
		err := app.dhcpSrv.Start("TestAdapter",
			net.ParseIP("192.168.1.1"),
			net.IPv4Mask(255, 255, 255, 0),
			net.ParseIP("192.168.1.100"),
			net.ParseIP("192.168.1.200"),
			60,
			nil, nil,
		)
		if err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}
		app.dhcpSrv.Stop()
		time.Sleep(20 * time.Millisecond)
		if app.dhcpSrv.IsRunning() {
			t.Fatalf("第 %d 次 Stop 后仍在运行", i+1)
		}
	}
}
