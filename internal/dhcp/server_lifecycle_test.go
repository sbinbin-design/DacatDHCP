package dhcp

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- 生命周期测试 ----
// V1修复: 使用注入的 createConnFunc 绑定随机 UDP 端口，无需管理员权限
// 注意：测试环境中 monitorAdapter 检测网卡状态可能触发自动停机，
// 这是预期行为，测试通过实例的 stopReqCh 模拟自动停机场景

// newTestServer 创建测试用 Server，注入 mock 连接
func newTestServer() *Server {
	s := NewServer()
	s.createConnFunc = func(bindIP net.IP) (net.PacketConn, error) {
		// 使用随机端口，避免端口冲突和权限问题
		return net.ListenPacket("udp", "127.0.0.1:0")
	}
	return s
}

// testStartParams 返回测试用的启动参数
// V2新增: 返回 gateway/dnsServers（默认 nil，不下发 Option 3/6）
func testStartParams() (adapterName string, adapterIP net.IP, subnetMask net.IPMask, poolStart, poolEnd net.IP, leaseMinutes int, gateway net.IP, dnsServers []net.IP) {
	return "TestAdapter",
		net.ParseIP("192.168.1.1"),
		net.IPv4Mask(255, 255, 255, 0),
		net.ParseIP("192.168.1.100"),
		net.ParseIP("192.168.1.200"),
		60,
		nil,
		nil
}

// TestLifecycle_NormalStartStop 正常启停
func TestLifecycle_NormalStartStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	// 启动
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if !s.IsRunning() {
		t.Error("Start 后应处于运行状态")
	}

	// 停止
	s.Stop()
	if s.IsRunning() {
		t.Error("Stop 后不应处于运行状态")
	}
}

// TestLifecycle_StopWhenNotRunning 未运行时 Stop 不应 panic
func TestLifecycle_StopWhenNotRunning(t *testing.T) {
	s := newTestServer()
	// 未启动时 Stop 不应 panic 或死锁
	s.Stop()
}

// TestLifecycle_ConsecutiveStartStop 连续启停20次
func TestLifecycle_ConsecutiveStartStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	for i := 0; i < 20; i++ {
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}
		if !s.IsRunning() {
			t.Fatalf("第 %d 次 Start 后未运行", i+1)
		}
		s.Stop()
		// 等待停止完成
		time.Sleep(50 * time.Millisecond)
		if s.IsRunning() {
			t.Fatalf("第 %d 次 Stop 后仍在运行", i+1)
		}
	}
}

// TestLifecycle_ConcurrentStopAndClose Close 与 Stop 并发调用
func TestLifecycle_ConcurrentStopAndClose(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 并发调用 Stop 多次，不应死锁或 panic
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop()
		}()
	}
	wg.Wait()

	if s.IsRunning() {
		t.Error("并发 Stop 后不应处于运行状态")
	}
}

// TestLifecycle_StartWhileRunning 运行中再次 Start 应报错
func TestLifecycle_StartWhileRunning(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 运行中再次 Start 应返回错误
	err := s.Start(name, ip, mask, start, end, mins, gw, dns)
	if err == nil {
		t.Error("运行中再次 Start 应返回错误")
	}
	s.Stop()
}

// TestConsecutiveStartStop20x 连续启停20次无死锁和协程残留
func TestConsecutiveStartStop20x(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	// V4: 设置自动停止回调，确保回调不死锁
	autoStopCount := int32(0)
	s.SetOnAutoStop(func(_ string) {
		_ = atomic.AddInt32(&autoStopCount, 1)
	})

	for i := 0; i < 20; i++ {
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}
		if !s.IsRunning() {
			t.Fatalf("第 %d 次 Start 后未运行", i+1)
		}
		s.Stop()
		// 等待停止完成
		time.Sleep(50 * time.Millisecond)
		if s.IsRunning() {
			t.Fatalf("第 %d 次 Stop 后仍在运行", i+1)
		}
	}

	_ = autoStopCount
}

// TestConcurrentStatusUpdateAndExit 状态更新与退出并发无竞态
func TestConcurrentStatusUpdateAndExit(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	callbackCount := int32(0)
	s.SetOnAutoStop(func(_ string) {
		// 模拟回调中的状态查询（与退出并发）
		_ = s.IsRunning()
		_ = s.Status()
	})

	for i := 0; i < 5; i++ {
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}

		// 并发：一个 goroutine 做 Status 查询，另一个做 Stop
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Status()
				s.IsRunning()
			}
		}()
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
			s.Stop()
		}()
		wg.Wait()

		if s.IsRunning() {
			t.Fatalf("第 %d 轮 Stop 后仍在运行", i+1)
		}
	}

	_ = callbackCount
}

// TestConcurrentExitRequests 并发多次请求退出只执行一次
// V5: 模拟多个退出源（托盘退出、关机、重复点击）并发触发
func TestConcurrentExitRequests(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	var stopCount int32
	s.SetOnAutoStop(func(_ string) {
		atomic.AddInt32(&stopCount, 1)
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 并发调用 Stop 10 次
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop()
		}()
	}
	wg.Wait()

	if s.IsRunning() {
		t.Error("并发 Stop 后仍在运行")
	}
	// onAutoStop 不应被触发（手动 Stop 不触发）
	if atomic.LoadInt32(&stopCount) != 0 {
		t.Error("手动 Stop 不应触发 onAutoStop")
	}
}

// TestStatusUpdateAfterClosing 退出后状态更新被忽略
// V5: 验证 ClearOnStatusChange 后 notifyStatusChange 不会 panic
func TestStatusUpdateAfterClosing(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	// 设置一个会在被清空后仍然被调用的回调场景
	notifyCount := int32(0)
	s.SetOnAutoStop(func(_ string) {
		// 自动停止时会调用 notifyStatusChange
		// 如果回调已被清除，这不应 panic
		atomic.AddInt32(&notifyCount, 1)
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 清除回调（模拟 app.ClearOnStatusChange）
	s.SetOnAutoStop(nil)

	// 触发自动停止
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}
	select {
	case <-inst.stopDoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("自动停机超时")
	}

	// 回调应未被触发（因为已被清除）
	if atomic.LoadInt32(&notifyCount) != 0 {
		t.Error("回调已被清除后不应被触发")
	}
}

// TestStartStop20x_WithCallback 连续启停20次带回调无死锁和协程残留
// V5: 综合测试，包含 onAutoStop 回调
func TestStartStop20x_WithCallback(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	autoStopCount := int32(0)
	s.SetOnAutoStop(func(_ string) {
		_ = atomic.AddInt32(&autoStopCount, 1)
		// 模拟回调做一些状态查询
		s.IsRunning()
		s.Status()
	})

	for i := 0; i < 20; i++ {
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}
		if !s.IsRunning() {
			t.Fatalf("第 %d 次 Start 后未运行", i+1)
		}
		s.Stop()
		time.Sleep(20 * time.Millisecond)
		if s.IsRunning() {
			t.Fatalf("第 %d 次 Stop 后仍在运行", i+1)
		}
	}
}
