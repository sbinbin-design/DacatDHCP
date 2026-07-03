package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// V16重构: 本文件仅保留后端推荐 API 测试,不再用 Go 模拟前端状态机冒充前端测试
// 前端 poolReqSeq 三重校验逻辑由 scripts/test_pool_recommend.js (Node) 直接执行真实 web/app.js 核心判断

// callBackend 调用真实后端 pool-recommend API,返回响应记录器
func callBackend(app *AppServer, adapterName string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/pool-recommend?adapter_name="+adapterName, nil)
	w := httptest.NewRecorder()
	app.handlePoolRecommend(w, req)
	return w
}

// TestPoolRecommend_BackendReturnsCorrectPool 后端对不同网卡返回不同推荐结果
// 验证后端 API 契约,确保 A/B 网卡返回的地址池在不同子网
func TestPoolRecommend_BackendReturnsCorrectPool(t *testing.T) {
	app := newTestAppServer(t)

	app.testFindAdapterFunc = func(name string) (net.IP, net.IPMask, error) {
		switch name {
		case "AdapterA":
			return net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil
		case "AdapterB":
			return net.ParseIP("10.0.0.1"), net.IPv4Mask(255, 255, 255, 0), nil
		default:
			return nil, nil, fmt.Errorf("adapter not found: %s", name)
		}
	}

	// 请求 A 的推荐
	wA := callBackend(app, "AdapterA")
	if wA.Code != http.StatusOK {
		t.Fatalf("AdapterA 推荐应成功, 实际 %d, body=%s", wA.Code, wA.Body.String())
	}
	bodyA := wA.Body.String()
	if !strings.Contains(bodyA, "192.168.1.") {
		t.Errorf("AdapterA 推荐应在 192.168.1. 子网, body=%s", bodyA)
	}

	// 请求 B 的推荐
	wB := callBackend(app, "AdapterB")
	if wB.Code != http.StatusOK {
		t.Fatalf("AdapterB 推荐应成功, 实际 %d, body=%s", wB.Code, wB.Body.String())
	}
	bodyB := wB.Body.String()
	if !strings.Contains(bodyB, "10.0.0.") {
		t.Errorf("AdapterB 推荐应在 10.0.0. 子网, body=%s", bodyB)
	}

	// 验证 A 和 B 的推荐结果不同
	if bodyA == bodyB {
		t.Error("A 和 B 网卡的推荐结果应不同(不同子网)")
	}
}

// TestPoolRecommend_BackendConcurrentRequests 后端并发请求不崩溃
// 验证后端 API 在并发场景下的稳定性(前端序号校验由 Node 测试覆盖)
func TestPoolRecommend_BackendConcurrentRequests(t *testing.T) {
	app := newTestAppServer(t)

	app.testFindAdapterFunc = func(name string) (net.IP, net.IPMask, error) {
		return net.ParseIP("192.168.1.1"), net.IPv4Mask(255, 255, 255, 0), nil
	}

	// 并发发起 20 个推荐请求
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			time.Sleep(time.Duration(n) * time.Millisecond) // 错开请求时间
			adapterName := "AdapterA"
			if n%2 == 0 {
				adapterName = "AdapterB"
			}
			w := callBackend(app, adapterName)
			// 只验证不崩溃和 HTTP 200
			if w.Code != http.StatusOK {
				t.Errorf("并发请求 %d 应返回 200, 实际 %d", n, w.Code)
			}
		}(i)
	}
	wg.Wait()
}

// TestPoolRecommend_BackendMissingAdapter 缺少 adapter_name 参数时返回 400
func TestPoolRecommend_BackendMissingAdapter(t *testing.T) {
	app := newTestAppServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pool-recommend", nil)
	w := httptest.NewRecorder()
	app.handlePoolRecommend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("缺少 adapter_name 应返回 400, 实际 %d", w.Code)
	}
}

// TestPoolRecommend_BackendAdapterNotFound 网卡不存在时返回 400
func TestPoolRecommend_BackendAdapterNotFound(t *testing.T) {
	app := newTestAppServer(t)

	app.testFindAdapterFunc = func(name string) (net.IP, net.IPMask, error) {
		return nil, nil, fmt.Errorf("adapter not found: %s", name)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pool-recommend?adapter_name=NoSuchAdapter", nil)
	w := httptest.NewRecorder()
	app.handlePoolRecommend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("网卡不存在应返回 400, 实际 %d", w.Code)
	}
}
