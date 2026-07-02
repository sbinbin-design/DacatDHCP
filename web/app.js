/* DacatDHCP V1 - IE11 兼容 JavaScript */
/* 严禁使用: fetch, Promise, async/await, 箭头函数, 可选链, 模板字符串 */

(function () {
    "use strict";

    var pollTimer = null;
    var logCount = 0;

    // ---- 工具函数 ----

    // 发送 AJAX 请求
    function ajax(method, url, data, callback) {
        var xhr = new XMLHttpRequest();
        xhr.open(method, url, true);
        xhr.setRequestHeader("Content-Type", "application/json");
        xhr.onreadystatechange = function () {
            if (xhr.readyState === 4) {
                if (xhr.status === 200) {
                    try {
                        var resp = JSON.parse(xhr.responseText);
                        callback(null, resp);
                    } catch (e) {
                        callback(e, null);
                    }
                } else {
                    try {
                        var errResp = JSON.parse(xhr.responseText);
                        callback(errResp.error || ("HTTP " + xhr.status), null);
                    } catch (e) {
                        callback("HTTP " + xhr.status, null);
                    }
                }
            }
        };
        if (data !== null && data !== undefined) {
            xhr.send(JSON.stringify(data));
        } else {
            xhr.send();
        }
    }

    // GET 请求
    function get(url, callback) {
        ajax("GET", url, null, callback);
    }

    // POST 请求
    function post(url, data, callback) {
        ajax("POST", url, data, callback);
    }

    // ---- 页面初始化 ----

    function init() {
        // V1修复: 配置加载顺序：先加载 config.json，再加载网卡列表，最后恢复已保存网卡
        loadConfigAndAdapters();
        refreshStatus();
        // 启动轮询
        pollTimer = setInterval(function () {
            refreshStatus();
            refreshLeases();
            refreshLogs();
        }, 3000);
    }

    // ---- 加载配置和网卡（V1修复: 确保配置先加载，再加载网卡列表恢复已保存选择）----

    function loadConfigAndAdapters() {
        get("/api/config", function (err, cfg) {
            if (!err && cfg) {
                var sel = document.getElementById("adapter-select");
                if (cfg.adapter_name) {
                    sel.setAttribute("data-selected", cfg.adapter_name);
                }
                if (cfg.pool_start) {
                    document.getElementById("pool-start").value = cfg.pool_start;
                }
                if (cfg.pool_end) {
                    document.getElementById("pool-end").value = cfg.pool_end;
                }
                if (cfg.lease_minutes && cfg.lease_minutes > 0) {
                    document.getElementById("lease-time").value = cfg.lease_minutes;
                }
            }
            // 配置加载完成后再加载网卡列表（保证 data-selected 已设置）
            loadAdapters();
        });
    }

    // ---- 加载网卡列表 ----

    function loadAdapters() {
        get("/api/adapters", function (err, data) {
            if (err || !data || !data.adapters) return;
            var sel = document.getElementById("adapter-select");
            var selected = sel.getAttribute("data-selected") || "";
            // 清除旧选项（保留第一个占位）
            while (sel.options.length > 1) {
                sel.remove(1);
            }
            var adapters = data.adapters;
            for (var i = 0; i < adapters.length; i++) {
                var a = adapters[i];
                // 跳过没有 IPv4 的网卡
                if (!a.hasIPv4) continue;
                var opt = document.createElement("option");
                opt.value = a.name;
                var typeTag = a.type === "virtual" ? " [虚拟]" : "";
                opt.text = a.name + " - " + a.ip + typeTag;
                if (a.name === selected) {
                    opt.selected = true;
                }
                sel.add(opt);
            }
            // V1修复: 网卡不存在时不自动选择错误网卡，保留占位选项
            // 更新网卡详情
            updateAdapterInfo();
            // V1修复: 初始加载时如果没有保存的地址池，请求后端推荐
            autoFillPool();
        });
    }

    // 网卡选择变化时更新详情
    // V1修复: 用户主动切换网卡时清空地址池并重新请求推荐值
    function onAdapterChange() {
        updateAdapterInfo();
        requestPoolRecommend();
    }

    function updateAdapterInfo() {
        var sel = document.getElementById("adapter-select");
        var info = document.getElementById("adapter-info");
        var val = sel.value;
        if (!val) {
            info.innerHTML = "";
            return;
        }
        // 从选项文本中提取 IP
        var opt = sel.options[sel.selectedIndex];
        if (opt) {
            info.innerHTML = opt.text;
        }
    }

    // V1修复: 初始加载时自动填充地址池（如果已有保存值则保留，否则请求推荐）
    function autoFillPool() {
        var sel = document.getElementById("adapter-select");
        var val = sel.value;
        if (!val) return;

        var startEl = document.getElementById("pool-start");
        var endEl = document.getElementById("pool-end");
        // 如果地址池已有值（来自保存的配置），不覆盖
        if (startEl.value && endEl.value) return;

        // 无保存值时请求后端推荐
        requestPoolRecommend();
    }

    // V1修复: 用户主动切换网卡时强制清空地址池并请求后端推荐
    function requestPoolRecommend() {
        var sel = document.getElementById("adapter-select");
        var val = sel.value;
        if (!val) return;

        var startEl = document.getElementById("pool-start");
        var endEl = document.getElementById("pool-end");
        // 清空旧值，禁止沿用上一网卡的地址池
        startEl.value = "";
        endEl.value = "";

        get("/api/pool-recommend?adapter_name=" + encodeURIComponent(val), function (err, data) {
            if (err || !data) return;
            if (data.pool_start) {
                startEl.value = data.pool_start;
            }
            if (data.pool_end) {
                endEl.value = data.pool_end;
            }
        });
    }

    // ---- 刷新服务状态 ----

    function refreshStatus() {
        get("/api/status", function (err, status) {
            if (err || !status) return;
            var statusEl = document.getElementById("service-status");
            var btnStart = document.getElementById("btn-start");
            var btnStop = document.getElementById("btn-stop");
            var errorEl = document.getElementById("error-msg");
            var ipEl = document.getElementById("server-ip");
            var poolEl = document.getElementById("pool-info");
            var usageEl = document.getElementById("pool-usage");

            if (status.running) {
                statusEl.innerHTML = "运行中";
                statusEl.className = "status-running";
                btnStart.disabled = true;
                btnStop.disabled = false;
                setConfigDisabled(true);
            } else {
                statusEl.innerHTML = "已停止";
                statusEl.className = "status-stopped";
                btnStart.disabled = false;
                btnStop.disabled = true;
                setConfigDisabled(false);
            }

            ipEl.innerHTML = status.server_ip || "-";
            poolEl.innerHTML = (status.pool_start || "-") + " ~ " + (status.pool_end || "-");
            usageEl.innerHTML = (status.pool_used || 0) + " / " + (status.pool_total || 0);

            if (status.error) {
                errorEl.innerHTML = status.error;
                errorEl.style.display = "block";
            } else {
                errorEl.style.display = "none";
            }
        });
    }

    // 运行时锁定配置输入
    function setConfigDisabled(disabled) {
        document.getElementById("adapter-select").disabled = disabled;
        document.getElementById("pool-start").disabled = disabled;
        document.getElementById("pool-end").disabled = disabled;
        document.getElementById("lease-time").disabled = disabled;
    }

    // ---- 启动服务 ----

    window.startService = function () {
        var adapter = document.getElementById("adapter-select").value;
        var poolStart = document.getElementById("pool-start").value.trim();
        var poolEnd = document.getElementById("pool-end").value.trim();
        var leaseTime = parseInt(document.getElementById("lease-time").value, 10);

        // 前端校验
        if (!adapter) {
            alert("请选择服务网卡");
            return;
        }
        if (!poolStart) {
            alert("请输入起始 IP 地址");
            return;
        }
        if (!poolEnd) {
            alert("请输入结束 IP 地址");
            return;
        }
        if (isNaN(leaseTime) || leaseTime <= 0) {
            alert("租约时间必须大于 0");
            return;
        }

        var btnStart = document.getElementById("btn-start");
        btnStart.disabled = true;
        btnStart.innerHTML = "启动中...";

        post("/api/start", {
            adapter_name: adapter,
            pool_start: poolStart,
            pool_end: poolEnd,
            lease_minutes: leaseTime
        }, function (err, resp) {
            btnStart.innerHTML = "启动服务";
            if (err) {
                alert("启动失败: " + err);
                btnStart.disabled = false;
            } else {
                refreshStatus();
                refreshLeases();
                refreshLogs();
            }
        });
    };

    // ---- 停止服务 ----

    window.stopService = function () {
        var btnStop = document.getElementById("btn-stop");
        btnStop.disabled = true;
        btnStop.innerHTML = "停止中...";

        post("/api/stop", null, function (err, resp) {
            btnStop.innerHTML = "停止服务";
            if (err) {
                alert("停止失败: " + err);
                btnStop.disabled = false;
            } else {
                refreshStatus();
            }
        });
    };

    // ---- 刷新租约列表 ----

    function refreshLeases() {
        get("/api/leases", function (err, data) {
            if (err || !data) return;
            var tbody = document.getElementById("lease-body");
            var leases = data.leases || [];
            if (leases.length === 0) {
                tbody.innerHTML = '<tr><td colspan="6" class="empty-msg">暂无租约</td></tr>';
                return;
            }
            var html = "";
            for (var i = 0; i < leases.length; i++) {
                var l = leases[i];
                html += "<tr>";
                html += "<td>" + escHtml(l.ip) + "</td>";
                html += "<td>" + escHtml(l.mac) + "</td>";
                html += "<td>" + escHtml(l.hostname || "-") + "</td>";
                html += "<td>" + escHtml(l.assigned_at) + "</td>";
                html += "<td>" + escHtml(l.expires_at) + "</td>";
                html += "<td>" + escHtml(l.status) + "</td>";
                html += "</tr>";
            }
            tbody.innerHTML = html;
        });
    }

    // ---- 刷新日志 ----

    function refreshLogs() {
        get("/api/logs", function (err, data) {
            if (err || !data) return;
            var logs = data.logs || [];
            var box = document.getElementById("log-box");
            var text = "";
            for (var i = 0; i < logs.length; i++) {
                text += logs[i] + "\n";
            }
            box.innerHTML = "";
            box.appendChild(document.createTextNode(text));
            // 滚动到底部
            box.scrollTop = box.scrollHeight;
        });
    }

    // HTML 转义
    function escHtml(s) {
        if (!s) return "";
        var div = document.createElement("div");
        div.appendChild(document.createTextNode(s));
        return div.innerHTML;
    }

    // 绑定网卡选择事件
    document.getElementById("adapter-select").attachEvent
        ? document.getElementById("adapter-select").attachEvent("onchange", onAdapterChange)
        : document.getElementById("adapter-select").addEventListener("change", onAdapterChange);

    // 页面加载完成后初始化
    if (document.readyState === "complete" || document.readyState === "interactive") {
        init();
    } else if (document.attachEvent) {
        document.attachEvent("onreadystatechange", function () {
            if (document.readyState === "complete") init();
        });
    } else {
        document.addEventListener("DOMContentLoaded", init);
    }
})();
