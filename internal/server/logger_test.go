package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// newTestResponseRecorder 创建测试用 ResponseRecorder
func newTestResponseRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

// newTestPostRequest 创建 POST 测试请求
func newTestPostRequest(target, body string) *http.Request {
	req, _ := http.NewRequest("POST", target, strings.NewReader(body))
	return req
}

// newTestGetRequest 创建 GET 测试请求
func newTestGetRequest(target string) *http.Request {
	req, _ := http.NewRequest("GET", target, nil)
	return req
}

// newTestLogger 创建测试用 Logger(使用 t.TempDir 隔离),测试结束自动关闭
func newTestLogger(t *testing.T) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := NewLogger(dir)
	if err != nil {
		t.Fatalf("NewLogger 失败: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// readLogFile 读取日志文件全部内容,用于验证文件截断
func readLogFile(t *testing.T, l *Logger) string {
	t.Helper()
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		// 文件不存在视为空
		return ""
	}
	return string(data)
}

// TestLogger_WriteAndRead 写入日志后能从 GetRecentLogs 读回
func TestLogger_WriteAndRead(t *testing.T) {
	l := newTestLogger(t)

	l.Log("第一条日志 %d", 1)
	l.Log("第二条日志 %d", 2)
	l.Log("第三条日志 %d", 3)

	logs := l.GetRecentLogs()
	if len(logs) != 3 {
		t.Fatalf("期望 3 条日志,实际 %d", len(logs))
	}
	// 验证内容按时间顺序
	if !strings.Contains(logs[0], "第一条日志 1") {
		t.Errorf("第1条内容异常: %s", logs[0])
	}
	if !strings.Contains(logs[2], "第三条日志 3") {
		t.Errorf("第3条内容异常: %s", logs[2])
	}

	// 验证文件也写入了内容
	content := readLogFile(t, l)
	if !strings.Contains(content, "第一条日志 1") {
		t.Errorf("文件中缺少第1条日志,内容: %s", content)
	}
	if !strings.Contains(content, "第三条日志 3") {
		t.Errorf("文件中缺少第3条日志,内容: %s", content)
	}
}

// TestLogger_Clear 清空后内存和文件均为空
func TestLogger_Clear(t *testing.T) {
	l := newTestLogger(t)

	l.Log("清空前日志A")
	l.Log("清空前日志B")

	// 清空前验证有内容
	logs := l.GetRecentLogs()
	if len(logs) != 2 {
		t.Fatalf("清空前期望 2 条日志,实际 %d", len(logs))
	}
	if readLogFile(t, l) == "" {
		t.Fatal("清空前文件不应为空")
	}

	// 执行清空
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}

	// 验证内存缓冲区已清空
	logs = l.GetRecentLogs()
	if len(logs) != 0 {
		t.Errorf("清空后内存应无日志,实际 %d 条", len(logs))
	}

	// 验证文件已截断为空
	content := readLogFile(t, l)
	if content != "" {
		t.Errorf("清空后文件应为空,实际长度 %d,内容: %q", len(content), content)
	}
}

// TestLogger_WriteAfterClear 清空后继续写入正常工作
func TestLogger_WriteAfterClear(t *testing.T) {
	l := newTestLogger(t)

	l.Log("旧日志")
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}

	// 清空后写入新日志
	l.Log("清空后新日志1")
	l.Log("清空后新日志2")

	// 验证内存只有新日志
	logs := l.GetRecentLogs()
	if len(logs) != 2 {
		t.Fatalf("清空后写入应只有 2 条日志,实际 %d", len(logs))
	}
	if !strings.Contains(logs[0], "清空后新日志1") {
		t.Errorf("第1条内容异常: %s", logs[0])
	}
	if !strings.Contains(logs[1], "清空后新日志2") {
		t.Errorf("第2条内容异常: %s", logs[1])
	}

	// 验证文件只有新日志,不含旧日志
	content := readLogFile(t, l)
	if strings.Contains(content, "旧日志") {
		t.Errorf("清空后文件不应含旧日志,内容: %s", content)
	}
	if !strings.Contains(content, "清空后新日志1") {
		t.Errorf("文件中缺少新日志1,内容: %s", content)
	}
	if !strings.Contains(content, "清空后新日志2") {
		t.Errorf("文件中缺少新日志2,内容: %s", content)
	}
}

// TestLogger_ConcurrentLogClear 并发 Log/Clear 不崩溃不死锁
func TestLogger_ConcurrentLogClear(t *testing.T) {
	l := newTestLogger(t)

	var wg sync.WaitGroup
	// 多个 goroutine 并发写入日志
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l.Log("并发日志 goroutine=%d seq=%d", id, j)
			}
		}(i)
	}
	// 一个 goroutine 并发清空
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := 0; k < 10; k++ {
			// 忽略错误,只测试不崩溃不死锁
			_ = l.Clear()
		}
	}()

	wg.Wait()

	// 最终清空一次,验证 Clear 正常返回
	if err := l.Clear(); err != nil {
		t.Fatalf("并发后 Clear 失败: %v", err)
	}

	// 再写入一条,验证日志功能仍正常
	l.Log("并发测试结束")
	logs := l.GetRecentLogs()
	if len(logs) != 1 {
		t.Errorf("最终清空后写入应只有 1 条日志,实际 %d", len(logs))
	}
	if !strings.Contains(logs[0], "并发测试结束") {
		t.Errorf("最终日志内容异常: %s", logs[0])
	}
}

// TestLogger_ClearPreservesFileHandle Clear 成功路径: 文件句柄仍可写入
// V14: 同时覆盖失败路径(下方 TestLogger_ClearTruncateFailurePreservesHandle)
func TestLogger_ClearPreservesFileHandle(t *testing.T) {
	l := newTestLogger(t)

	l.Log("测试日志1")
	if err := l.Clear(); err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}

	// 验证 l.file 仍非 nil(句柄保留,Truncate 路径不关闭句柄)
	l.mu.Lock()
	fileNil := l.file == nil
	l.mu.Unlock()
	if fileNil {
		t.Error("Clear 后文件句柄不应为 nil")
	}

	// 继续写入验证句柄可用
	l.Log("测试日志2")
	content := readLogFile(t, l)
	if !strings.Contains(content, "测试日志2") {
		t.Errorf("Clear 后写入失败,文件内容: %s", content)
	}
}

// TestLogger_ClearTruncateFailurePreservesHandle V14新增: 截断失败路径
// 通过测试钩子模拟 Truncate 失败,验证:
// 1. 内存日志保留未清空
// 2. 原文件句柄仍有效(不关闭不替换)
// 3. 失败后继续 Log 仍可写入文件和内存
func TestLogger_ClearTruncateFailurePreservesHandle(t *testing.T) {
	l := newTestLogger(t)

	l.Log("截断前日志A")
	l.Log("截断前日志B")

	// 记录原句柄引用,用于验证失败后未被替换
	l.mu.Lock()
	originalFile := l.file
	l.testTruncateErr = fmt.Errorf("simulated truncate failure")
	l.mu.Unlock()

	// 执行 Clear,预期返回截断失败错误
	err := l.Clear()
	if err == nil {
		t.Fatal("注入截断失败后 Clear 应返回错误")
	}
	if !strings.Contains(err.Error(), "truncate_log_failed") {
		t.Errorf("错误应含 truncate_log_failed,实际: %v", err)
	}

	// 验证 1: 内存日志保留未清空
	logs := l.GetRecentLogs()
	if len(logs) != 2 {
		t.Errorf("截断失败时内存日志应保留,期望 2 条,实际 %d", len(logs))
	}

	// 验证 2: 原文件句柄未被替换(同一指针)
	l.mu.Lock()
	currentFile := l.file
	l.mu.Unlock()
	if currentFile != originalFile {
		t.Error("截断失败时原文件句柄不应被替换")
	}

	// 验证 3: 失败后继续 Log 仍可写入文件和内存
	l.Log("失败后新日志")
	logs = l.GetRecentLogs()
	if len(logs) != 3 {
		t.Errorf("失败后继续 Log 应有 3 条,实际 %d", len(logs))
	}
	if !strings.Contains(logs[2], "失败后新日志") {
		t.Errorf("第3条内容异常: %s", logs[2])
	}
	content := readLogFile(t, l)
	if !strings.Contains(content, "失败后新日志") {
		t.Errorf("失败后文件写入失败,内容: %s", content)
	}

	// 清除测试钩子后 Clear 应成功
	l.mu.Lock()
	l.testTruncateErr = nil
	l.mu.Unlock()
	if err := l.Clear(); err != nil {
		t.Fatalf("清除钩子后 Clear 应成功,实际: %v", err)
	}
	// 验证清空成功
	logs = l.GetRecentLogs()
	if len(logs) != 0 {
		t.Errorf("清除钩子后清空应无日志,实际 %d", len(logs))
	}
}

// TestLogger_ClearNilHandleReopens V14新增: 句柄为 nil 时重新打开
func TestLogger_ClearNilHandleReopens(t *testing.T) {
	l := newTestLogger(t)
	l.Log("初始日志")

	// V14修复: 关闭原句柄再置空,避免句柄泄漏导致 TempDir 清理失败
	l.mu.Lock()
	if l.file != nil {
		l.file.Close()
	}
	l.file = nil
	l.mu.Unlock()

	// Clear 应重新打开文件并清空内存
	if err := l.Clear(); err != nil {
		t.Fatalf("句柄为 nil 时 Clear 应重新打开,实际: %v", err)
	}

	// 验证句柄已恢复
	l.mu.Lock()
	fileNil := l.file == nil
	l.mu.Unlock()
	if fileNil {
		t.Error("Clear 后句柄应已恢复")
	}

	// 验证内存已清空
	logs := l.GetRecentLogs()
	if len(logs) != 0 {
		t.Errorf("句柄恢复后内存应清空,实际 %d", len(logs))
	}

	// 验证可继续写入
	l.Log("恢复后日志")
	content := readLogFile(t, l)
	if !strings.Contains(content, "恢复后日志") {
		t.Errorf("恢复后写入失败,内容: %s", content)
	}
}

// TestLogger_RingBufferWraparound 环形缓冲区超过容量后正确覆盖旧条目
func TestLogger_RingBufferWraparound(t *testing.T) {
	l := newTestLogger(t)

	// 写入超过 logRingSize(500) 条日志,验证不崩溃且能读回最近 500 条
	for i := 0; i < logRingSize+50; i++ {
		l.Log("溢出测试 seq=%d", i)
	}

	logs := l.GetRecentLogs()
	if len(logs) != logRingSize {
		t.Errorf("期望 %d 条日志(环形缓冲区满),实际 %d", logRingSize, len(logs))
	}
	// 最早一条应是 seq=50(前 50 条被覆盖)
	if !strings.Contains(logs[0], "seq=50") {
		t.Errorf("环形缓冲区覆盖异常,最早一条: %s", logs[0])
	}
}

// ---- /api/logs/clear HTTP 处理器测试 ----

// TestHandleLogsClear_PostSuccess POST 清空成功返回 ok
func TestHandleLogsClear_PostSuccess(t *testing.T) {
	app := newTestAppServer(t)
	app.logger.Log("待清空日志")

	// 直接调用处理器(不启动 HTTP 服务器)
	w := newTestResponseRecorder()
	req := newTestPostRequest("/api/logs/clear", "")
	app.handleLogsClear(w, req)

	if w.Code != 200 {
		t.Errorf("期望 200,实际 %d, body=%s", w.Code, w.Body.String())
	}
	// 验证内存已清空
	logs := app.logger.GetRecentLogs()
	if len(logs) != 0 {
		t.Errorf("清空后内存应无日志,实际 %d 条", len(logs))
	}
}

// TestHandleLogsClear_WrongMethod GET 方法返回 405 和 method_not_allowed code
func TestHandleLogsClear_WrongMethod(t *testing.T) {
	app := newTestAppServer(t)

	w := newTestResponseRecorder()
	req := newTestGetRequest("/api/logs/clear")
	app.handleLogsClear(w, req)

	if w.Code != 405 {
		t.Errorf("期望 405,实际 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "method_not_allowed") {
		t.Errorf("响应应含 method_not_allowed code,实际: %s", body)
	}
}

// TestHandleLogsClear_ClosingState 服务关闭中返回 503 和 service_closing code
func TestHandleLogsClear_ClosingState(t *testing.T) {
	app := newTestAppServer(t)
	app.logger.Log("清空前的日志")
	app.SetClosing() // 标记服务正在关闭

	w := newTestResponseRecorder()
	req := newTestPostRequest("/api/logs/clear", "")
	app.handleLogsClear(w, req)

	if w.Code != 503 {
		t.Errorf("期望 503,实际 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "service_closing") {
		t.Errorf("响应应含 service_closing code,实际: %s", body)
	}
	// 验证日志未被清空(closing 状态拒绝操作)
	logs := app.logger.GetRecentLogs()
	if len(logs) != 1 {
		t.Errorf("closing 状态下日志不应被清空,期望 1 条,实际 %d", len(logs))
	}
}
