package dhcp

import (
	"sync/atomic"
	"testing"
	"time"
)

// ---- 重启与续约测试 ----
// 本文件覆盖：停止后重启、自动停止后重启、连续自动停止后立即重启

// TestLifecycle_RestartAfterStop 停止后重启
func TestLifecycle_RestartAfterStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	// 第一次启停
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("第一次 Start 失败: %v", err)
	}
	s.Stop()

	// 等待停止完成（确保协程全部退出）
	time.Sleep(100 * time.Millisecond)

	if s.IsRunning() {
		t.Error("Stop 后不应处于运行状态")
	}

	// 重新启动
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("重启 Start 失败: %v", err)
	}
	if !s.IsRunning() {
		t.Error("重启后应处于运行状态")
	}
	s.Stop()
}

// TestLifecycle_RestartAfterAutoStop 自动停机后重启
// V3修复: 先捕获 inst，只通过 inst.stopReqCh 和 inst.stopDoneCh 等待
func TestLifecycle_RestartAfterAutoStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// V3: 先捕获 inst 引用
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	// 模拟自动停机
	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}
	select {
	case <-inst.stopDoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("自动停机超时")
	}

	// 自动停机后应能重启
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("自动停机后重启失败: %v", err)
	}
	if !s.IsRunning() {
		t.Error("自动停机后重启应处于运行状态")
	}
	s.Stop()
}

// TestAutoStop_ImmediateRestart 自动停止后可立即重新启动
// V5: 验证自动停止后快速重启无死锁
func TestAutoStop_ImmediateRestart(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	for i := 0; i < 10; i++ {
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}

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
			t.Fatalf("第 %d 次自动停机超时", i+1)
		}

		// V5: 立即重启，不等待
		if s.IsRunning() {
			t.Fatalf("第 %d 次自动停机后仍在运行", i+1)
		}
	}
}

// TestAutoStop_CanRestart_TrueAutoStop 真实自动停止后可立即重启
// V8: 通过 stopReqCh 触发自动停止，等待 stopDoneCh，验证 onAutoStop 完成后再重新 Start
func TestAutoStop_CanRestart_TrueAutoStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	autoStopCount := int32(0)
	s.SetOnAutoStop(func(_ string) {
		atomic.AddInt32(&autoStopCount, 1)
	})

	for i := 0; i < 5; i++ {
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次 Start 失败: %v", i+1, err)
		}

		s.mu.RLock()
		inst := s.instance
		s.mu.RUnlock()

		// V8: 通过 stopReqCh 触发自动停止（与 monitorAdapter 一致）
		select {
		case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
		default:
		}

		// V8: 等待 stopDoneCh，确保 onAutoStop 回调已完成
		select {
		case <-inst.stopDoneCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 次自动停机超时", i+1)
		}

		if s.IsRunning() {
			t.Fatalf("第 %d 次自动停机后仍在运行", i+1)
		}

		// V8: 立即重启（禁止用普通 Stop 代替自动停止路径）
		if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
			t.Fatalf("第 %d 次自动停机后重启失败: %v", i+1, err)
		}

		// 正常 Stop
		s.Stop()
		if s.IsRunning() {
			t.Fatalf("第 %d 次正常 Stop 后仍在运行", i+1)
		}
	}

	// V8: 断言自动停止回调被调用 5 次
	if atomic.LoadInt32(&autoStopCount) != 5 {
		t.Errorf("onAutoStop 回调应调用 5 次，实际 %d 次", atomic.LoadInt32(&autoStopCount))
	}
}
