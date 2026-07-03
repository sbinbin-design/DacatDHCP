package dhcp

import (
	"sync/atomic"
	"testing"
	"time"
)

// ---- 停止语义与回调测试 ----
// 本文件覆盖：自动停止、手动停止、退出停止、状态错误语义、并发停止

// TestLifecycle_AutoStop 模拟自动停机（通过实例的 stopReqCh 触发）
// V3修复: 先捕获 inst，只通过 inst.stopReqCh 和 inst.stopDoneCh 等待，禁止停止后再次读取 s.instance
func TestLifecycle_AutoStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// V3: 先捕获 inst 引用，停止后不再读取 s.instance（避免空指针）
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	// 模拟网卡异常，通过实例的 stopReqCh 触发自动停机
	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}

	// V3: 只通过捕获的 inst.stopDoneCh 等待，不读取 s.instance
	select {
	case <-inst.stopDoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("自动停机超时")
	}

	if s.IsRunning() {
		t.Error("自动停机后不应处于运行状态")
	}
}

// TestAutoStop_CallbackFired 自动停止时 onAutoStop 回调被触发
func TestAutoStop_CallbackFired(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	var callbackCalled int32
	s.SetOnAutoStop(func(_ string) {
		atomic.StoreInt32(&callbackCalled, 1)
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// V3: 先捕获 inst 引用
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	// 模拟网卡异常自动停止
	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}
	select {
	case <-inst.stopDoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("自动停机超时")
	}

	// V4: 回调在 stopDoneCh 关闭后的 stopCoordinator 中执行
	// 等待 stopCoordinator goroutine 退出（确保回调已完成）
	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&callbackCalled) != 1 {
		t.Error("自动停止后 onAutoStop 回调未被调用")
	}
}

// TestManualStop_CallbackNotFired 手动 Stop 时 onAutoStop 回调不应被触发
func TestManualStop_CallbackNotFired(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	var callbackCalled int32
	s.SetOnAutoStop(func(_ string) {
		atomic.StoreInt32(&callbackCalled, 1)
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	s.Stop()

	if atomic.LoadInt32(&callbackCalled) != 0 {
		t.Error("手动 Stop 不应触发 onAutoStop 回调")
	}
}

// TestAutoStop_StatusQueryableAndRestart 自动停止后可查询状态并重新启动
func TestAutoStop_StatusQueryableAndRestart(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// V3: 先捕获 inst 引用
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	// 模拟自动停止
	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}
	select {
	case <-inst.stopDoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("自动停机超时")
	}

	// V4: 自动停止后状态应可查询
	if s.IsRunning() {
		t.Error("自动停机后不应处于运行状态")
	}
	status := s.Status()
	if status.Running {
		t.Error("Status 应报告未运行")
	}
	if status.Error == "" {
		t.Error("Status 应包含错误信息")
	}

	// V4: 自动停止后应能重新启动
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("自动停机后重启失败: %v", err)
	}
	if !s.IsRunning() {
		t.Error("自动停机后重启应处于运行状态")
	}
	s.Stop()
}

// TestStop_ReturnsAfterCallbackComplete Stop 返回时 onAutoStop 回调已完成
// V5: stopDoneCh 在 log 和 callback 之后关闭，确保 Stop() 返回时状态通知已完成
func TestStop_ReturnsAfterCallbackComplete(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	var callbackCompleted int32
	s.SetOnAutoStop(func(_ string) {
		// 模拟回调做一些工作
		time.Sleep(50 * time.Millisecond)
		atomic.StoreInt32(&callbackCompleted, 1)
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 通过 stopReqCh 触发自动停止（会执行 onAutoStop 回调）
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡已断开或禁用，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}

	// 等待停止完成
	select {
	case <-inst.stopDoneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("自动停机超时")
	}

	// V5: Stop 返回时回调应已完成
	if atomic.LoadInt32(&callbackCompleted) != 1 {
		t.Error("Stop 返回后 onAutoStop 回调尚未完成")
	}
}

// TestConcurrentManualAndAutoStop 真实执行：手动 Stop 与自动 Stop 同时到达不死锁
// V8: 先确保自动停止请求已写入 stopReqCh，再并发调用 Server.Stop
// 断言 onAutoStop 回调调用次数恰好为 1，且收到正确的自动停止原因
func TestConcurrentManualAndAutoStop(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	autoStopCount := int32(0)
	var capturedReason string
	s.SetOnAutoStop(func(reason string) {
		atomic.AddInt32(&autoStopCount, 1)
		capturedReason = reason
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 捕获本轮 inst
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	// V8: 先确保自动停止请求已成功写入 stopReqCh
	// V9: 使用 stopRequest 携带 StopAuto 类型
	autoStopReason := "网卡已断开或禁用，DHCP 服务已自动停止"
	select {
	case inst.stopReqCh <- stopRequest{reason: autoStopReason, stopType: StopAuto}:
	default:
		t.Fatal("stopReqCh 写入失败")
	}

	// 然后并发调用 Server.Stop（手动停止）
	doneCh := make(chan struct{})
	go func() {
		defer func() { doneCh <- struct{}{} }()
		s.Stop()
	}()

	// 3 秒超时
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("并发 Stop 超时（死锁）")
	}

	// 等待 stopDoneCh 确保 doStop 完成
	select {
	case <-inst.stopDoneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("stopDoneCh 超时")
	}

	// 验证最终状态
	if s.IsRunning() {
		t.Fatal("并发 Stop 后仍在运行")
	}

	// V8: 断言 onAutoStop 回调调用次数恰好为 1
	if atomic.LoadInt32(&autoStopCount) != 1 {
		t.Errorf("onAutoStop 回调应调用 1 次，实际 %d 次", atomic.LoadInt32(&autoStopCount))
	}

	// V8: 断言回调收到正确的自动停止原因
	if capturedReason != autoStopReason {
		t.Errorf("停止原因 = %q, want %q", capturedReason, autoStopReason)
	}

	// V8: 验证 instance 已清理
	s.mu.RLock()
	instAfter := s.instance
	s.mu.RUnlock()
	if instAfter != nil {
		t.Error("停止后 instance 应为 nil")
	}

	// V8: 可再次 Start 后正常 Stop
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("再次 Start 失败: %v", err)
	}
	s.Stop()
	if s.IsRunning() {
		t.Fatal("再次 Stop 后仍在运行")
	}
}

// ---- V9: 停止状态语义测试 ----

// TestManualStop_ClearsStatusError 手动停止后 status.error 必须为空
// 验收: 手动停止后页面状态为"已停止"，无红色错误提示
func TestManualStop_ClearsStatusError(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	s.Stop()

	status := s.Status()
	if status.Running {
		t.Error("手动停止后不应处于运行状态")
	}
	// V9: 手动停止必须清空 status.error，不得显示为红色错误
	if status.Error != "" {
		t.Errorf("手动停止后 status.error 应为空，实际: %q", status.Error)
	}
}

// TestShutdownStop_ClearsStatusError 程序退出停止后 status.error 必须为空
// 验收: 托盘退出或系统关闭时不写入错误状态
func TestShutdownStop_ClearsStatusError(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// V9: 模拟程序退出路径的停止
	s.StopForShutdown()

	status := s.Status()
	if status.Running {
		t.Error("退出停止后不应处于运行状态")
	}
	// V9: 退出停止必须清空 status.error
	if status.Error != "" {
		t.Errorf("退出停止后 status.error 应为空，实际: %q", status.Error)
	}
}

// TestAutoStop_SetsStatusError 自动异常停止后 status.error 必须包含具体原因
// 验收: 拔网线等异常停止仍显示具体错误原因
func TestAutoStop_SetsStatusError(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	autoStopReason := "网卡已断开或禁用，DHCP 服务已自动停止"
	select {
	case inst.stopReqCh <- stopRequest{reason: autoStopReason, stopType: StopAuto}:
	default:
		t.Fatal("stopReqCh 写入失败")
	}

	select {
	case <-inst.stopDoneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("自动停机超时")
	}

	status := s.Status()
	if status.Running {
		t.Error("自动停止后不应处于运行状态")
	}
	// V9: 自动停止必须写入具体错误原因
	if status.Error != autoStopReason {
		t.Errorf("自动停止后 status.error = %q, want %q", status.Error, autoStopReason)
	}
}

// TestStart_ClearsOldStatusError 异常停止后重新启动成功，旧错误提示必须清空
// 验收: 异常停止后重新启动成功，旧错误提示自动清除
func TestStart_ClearsOldStatusError(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 触发自动停止，写入错误状态
	s.mu.RLock()
	inst := s.instance
	s.mu.RUnlock()

	select {
	case inst.stopReqCh <- stopRequest{reason: "网卡 IP 地址变化，DHCP 服务已自动停止", stopType: StopAuto}:
	default:
	}
	select {
	case <-inst.stopDoneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("自动停机超时")
	}

	// 验证错误已写入
	status := s.Status()
	if status.Error == "" {
		t.Fatal("自动停止后 status.error 不应为空")
	}

	// V9: 重新启动后旧错误必须清空
	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("重新 Start 失败: %v", err)
	}

	status = s.Status()
	if !status.Running {
		t.Error("重新启动后应处于运行状态")
	}
	if status.Error != "" {
		t.Errorf("重新启动后 status.error 应为空，实际: %q", status.Error)
	}

	s.Stop()
}

// TestStopForShutdown_NoAutoStopCallback 退出停止不应触发 onAutoStop 回调
func TestStopForShutdown_NoAutoStopCallback(t *testing.T) {
	s := newTestServer()
	name, ip, mask, start, end, mins, gw, dns := testStartParams()

	var callbackCalled int32
	s.SetOnAutoStop(func(_ string) {
		atomic.StoreInt32(&callbackCalled, 1)
	})

	if err := s.Start(name, ip, mask, start, end, mins, gw, dns); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// V9: 退出停止不应触发 onAutoStop
	s.StopForShutdown()

	if atomic.LoadInt32(&callbackCalled) != 0 {
		t.Error("退出停止不应触发 onAutoStop 回调")
	}
}
