/* DacatDHCP 管理界面 JavaScript (ES5 语法) */
/* 版本号从 /api/version 动态获取，禁止在前端硬编码 */
/* 严禁使用: fetch, Promise, async/await, 箭头函数, 可选链, 模板字符串 */

(function () {
    "use strict";

    var pollTimer = null;
    var clockTimer = null;     // V10新增: 本机时间时钟定时器
    var logCount = 0;
    var lastStartedAt = null;  // V10新增: 真实启动时间(RFC3339),由后端 started_at 提供,禁止前端伪造
    var dnsList = [];          // V10新增: DNS 列表(可视化添加/清除),提交时作为数组,保留现有数组提交逻辑
    var savedIsAdmin = null;   // V11新增: 保存真实 isAdmin 状态,语言切换时单独渲染,不得丢失或伪造
    var savedVersion = "";     // V12新增: 保存后端返回的版本号,供语言切换时重新渲染底部右侧模板
    var savedCopyright = "";   // V12新增: 保存后端返回的版权信息,供语言切换时重新渲染底部右侧模板
    var cachedAdapters = null; // V13新增: 缓存网卡 API 数据,语言切换只重新渲染缓存不重新请求
    var adapterReqSeq = 0;     // V14新增: 网卡请求序号,旧回调不得在语言切换或用户编辑后执行 autoFillPool
    var poolReqSeq = 0;        // V15新增: 地址池推荐请求序号,旧回调不得覆盖新网卡或用户已编辑的起止 IP
    var poolUserEditing = false; // V15新增: 用户开始手动编辑地址池时置 true,使在途推荐请求失效
    var langReqSeq = 0;        // 语言新增: 语言切换请求序号,过期响应不得覆盖用户最后一次选择

    // ---- 工具函数 ----

    // P0安全: 从 <meta name="dacat-csrf-token"> 读取 CSRF 令牌
    // 使用 getElementsByTagName (广泛兼容)
    // 令牌由后端 handleIndex 注入,写接口 AJAX 必须携带 X-Dacat-CSRF-Token
    function getCSRFToken() {
        var metas = document.getElementsByTagName("meta");
        if (!metas) return "";
        for (var i = 0; i < metas.length; i++) {
            if (metas[i].getAttribute("name") === "dacat-csrf-token") {
                return metas[i].getAttribute("content") || "";
            }
        }
        return "";
    }

    // P0安全: 判断是否为写方法(需携带 CSRF 令牌)
    function isWriteMethod(method) {
        return method === "POST" || method === "PUT" || method === "PATCH" || method === "DELETE";
    }

    // 发送 AJAX 请求
    // V11新增: 错误回调传入 {message, code} 对象,优先使用 code 翻译
    // P0安全: 写方法(POST/PUT/PATCH/DELETE)统一携带 X-Dacat-CSRF-Token,后端校验失败返回 403 + csrf_token_invalid
    function ajax(method, url, data, callback) {
        var xhr = new XMLHttpRequest();
        xhr.open(method, url, true);
        xhr.setRequestHeader("Content-Type", "application/json");
        // P0安全: 写方法携带 CSRF 令牌,令牌从首页 meta 读取
        if (isWriteMethod(method)) {
            var token = getCSRFToken();
            if (token) {
                xhr.setRequestHeader("X-Dacat-CSRF-Token", token);
            }
        }
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
                        // V11: 优先提取 code,用于按错误码翻译
                        var errObj = {
                            message: errResp.error || ("HTTP " + xhr.status),
                            code: errResp.code || ""
                        };
                        callback(errObj, null);
                    } catch (e) {
                        callback({message: "HTTP " + xhr.status, code: ""}, null);
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

    // HTML 转义
    function escHtml(s) {
        if (!s) return "";
        var div = document.createElement("div");
        div.appendChild(document.createTextNode(s));
        return div.innerHTML;
    }

    // 设置元素文本的安全封装
    function setText(id, text) {
        var el = document.getElementById(id);
        if (el) el.innerHTML = "";
        if (el && text) el.appendChild(document.createTextNode(text));
    }

    // V12新增: 更新按钮内 span 文本,保留 SVG 图标(按钮结构为 <svg/><span>text</span>)
    function setBtnText(btn, text) {
        if (!btn) return;
        var span = btn.querySelector("span");
        if (span) {
            span.textContent = text;
        } else {
            // 兜底: 无 span 时直接更新整个按钮文本
            btn.textContent = text;
        }
    }

    // V10新增: 两位补零(用于时钟和运行时间格式化)
    function pad2(n) {
        return n < 10 ? "0" + n : "" + n;
    }

    // V10新增: 格式化本机时间
    // V12: 改为 YYYY-MM-DD HH:MM:SS 格式,符合效果图要求
    function formatClock(d) {
        return d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) +
               " " + pad2(d.getHours()) + ":" + pad2(d.getMinutes()) + ":" + pad2(d.getSeconds());
    }

    // V11重构: 运行时间统一显示 HH:MM:SS,超过24小时显示"天数 + HH:MM:SS",中英文不使用 h/m/s 缩写
    function formatUptime(ms) {
        if (ms < 0) ms = 0;
        var totalSec = Math.floor(ms / 1000);
        var d = Math.floor(totalSec / 86400);
        totalSec -= d * 86400;
        var h = Math.floor(totalSec / 3600);
        totalSec -= h * 3600;
        var m = Math.floor(totalSec / 60);
        var s = totalSec - m * 60;
        var timePart = pad2(h) + ":" + pad2(m) + ":" + pad2(s);
        if (d > 0) {
            // V11: 天数 + HH:MM:SS,使用"天"/"d"前缀避免歧义
            var dayLabel = window.I18N ? I18N.t("status.days") : "天";
            return d + dayLabel + " " + timePart;
        }
        return timePart;
    }

    // V10新增: IPv4 格式校验(用于 DNS 添加时的前端校验)
    function isValidIPv4(s) {
        if (!s) return false;
        var parts = s.split(".");
        if (parts.length !== 4) return false;
        for (var i = 0; i < 4; i++) {
            var p = parts[i];
            if (!/^\d+$/.test(p)) return false;
            var n = parseInt(p, 10);
            if (isNaN(n) || n < 0 || n > 255) return false;
            if (p.length > 1 && p.charAt(0) === "0") return false;
        }
        return true;
    }

    // V10新增: 格式化时间戳(用于导出日志文件名)
    function formatStamp(d) {
        return d.getFullYear() + pad2(d.getMonth() + 1) + pad2(d.getDate()) +
               "-" + pad2(d.getHours()) + pad2(d.getMinutes()) + pad2(d.getSeconds());
    }

    // ---- P0安全: 事件绑定(移除 HTML 内联 onclick/onchange,统一通过 addEventListener) ----
    // IE 兼容分支已移除: ie11-check.js 在 app.js 之前加载,IE 环境已被替换为不支持页面

    // 点击事件绑定(统一使用 addEventListener)
    function bindClick(id, handler) {
        var el = document.getElementById(id);
        if (!el) return;
        el.addEventListener("click", handler);
    }

    // change 事件绑定(统一使用 addEventListener)
    function bindChange(id, handler) {
        var el = document.getElementById(id);
        if (!el) return;
        el.addEventListener("change", handler);
    }

    // P0安全: 统一绑定所有按钮事件,替代 HTML 内联 onclick/onchange
    // CSP script-src 不依赖 unsafe-inline,所有事件通过 addEventListener 绑定
    function bindEvents() {
        // 顶部导航: 语言切换、主题、设置、关于
        bindClick("lang-zh", function () { switchLang("zh-CN"); });
        bindClick("lang-en", function () { switchLang("en-US"); });
        bindClick("btn-theme", function () { toggleTheme(); });
        bindClick("btn-settings", function () { openSettings(); });
        bindClick("btn-about", function () { openAbout(); });

        // 服务状态卡: 启动/停止、刷新信息、刷新租约
        bindClick("btn-start", function () { startService(); });
        bindClick("btn-stop", function () { stopService(); });
        bindClick("btn-refresh-all", function () { refreshAll(); });
        bindClick("btn-refresh-leases", function () { refreshLeases(); });

        // DNS 添加/清除
        bindClick("btn-dns-add", function () { addDnsServer(); });
        bindClick("btn-dns-clear", function () { clearDnsServers(); });

        // 日志导出/清空
        bindClick("btn-export-logs", function () { exportLogs(); });
        bindClick("btn-clear-logs", function () { clearLogs(); });

        // 设置弹窗: 关闭按钮和底部关闭按钮
        bindClick("btn-settings-close", function () { closeSettings(); });
        bindClick("btn-settings-done", function () { closeSettings(); });

        // 关于弹窗: 关闭按钮和底部关闭按钮
        bindClick("btn-about-close", function () { closeAbout(); });
        bindClick("btn-about-done", function () { closeAbout(); });

        // 设置弹窗语言单选变化
        bindChange("settings-lang-zh", function () { onSettingsLangChange("zh-CN"); });
        bindChange("settings-lang-en", function () { onSettingsLangChange("en-US"); });
    }

    // P0安全: 检测 CSRF 令牌失效错误,显示专用提示(不附加失败前缀)
    // 返回 true 表示已处理(调用方应 return),false 表示非 CSRF 错误继续常规处理
    function handleCSRFError(err) {
        if (typeof err === "object" && err && err.code === "csrf_token_invalid") {
            // P0安全收口: 直接使用 teByCode 按错误码翻译,不调用 I18N.t(那是 data-i18n 键查找)
            // teByCode 从 errCodeMap 返回中英文提示,禁止直接显示错误码或后端英文消息
            var backendMsg = err.message || "";
            var translated = window.I18N ? I18N.teByCode("csrf_token_invalid", backendMsg) : "页面安全凭据已失效，请刷新页面";
            alert(translated);
            return true;
        }
        return false;
    }

    // ---- 页面初始化 ----

    function init() {
        // P0安全: 统一绑定所有按钮事件(替代 HTML 内联 onclick/onchange)
        bindEvents();
        // 新增: 应用国际化文案(根据 localStorage 保存的语言)并刷新语言分段控件
        if (window.I18N) {
            I18N.applyTranslations();
            updateLangSeg();
        }
        // 新增: 启动系统主题变化监听(跟随系统模式下实时响应)
        if (window.THEME) {
            THEME.startSystemListener();
            updateThemeButton(); // V10新增: 同步主题图标按钮的 aria-label/title
        }
        // V10新增: 首次加载时同步 <html lang> 属性
        if (window.I18N) {
            document.documentElement.setAttribute("lang", I18N.getLang());
        }
        // V13新增: 页面初始化时立即渲染占位符,savedIsAdmin/savedVersion 尚未加载时显示"-"
        renderAdmin();
        renderFooterRight();
        // V1修复: 配置加载顺序：先加载 config.json，再加载网卡列表，最后恢复已保存网卡
        loadConfigAndAdapters();
        // 版本号从后端 /api/version 动态获取（唯一源: internal/version/versioninfo.json）
        loadVersion();
        // V11新增: 页面初始化时立即执行刷新,不等待首次轮询
        refreshStatus();
        refreshLeases();
        refreshLogs();
        // V10新增: 启动本机时间时钟(每秒更新)
        startClock();
        // 启动轮询
        pollTimer = setInterval(function () {
            refreshStatus();
            refreshLeases();
            refreshLogs();
        }, 3000);
    }

    // ---- 加载版本号（从 /api/version 动态获取，不硬编码）----

    function loadVersion() {
        get("/api/version", function (err, data) {
            if (err || !data || !data.version) return;
            var display = "V" + data.version;
            var appVer = document.getElementById("app-version");
            if (appVer) appVer.innerHTML = display;
            // V12: footer-version 元素已移除,版本号通过 footer-right-text 模板统一渲染
            // 新增: 同步填充关于弹窗的产品信息(均为后端返回,不硬编码)
            setText("about-product", data.product_name || "");
            setText("about-version", data.version || "");
            setText("about-file-version", data.file_version || "");
            setText("about-copyright", data.copyright || "");
            // V12: 保存版本号和版权到全局变量,供语言切换时重新渲染底部右侧模板
            savedVersion = data.version || "";
            savedCopyright = data.copyright || "";
            renderFooterRight();
            // V11重构: 保存真实 isAdmin 状态并单独渲染,语言切换不得修改此状态
            savedIsAdmin = (data.is_admin === true || data.is_admin === "true");
            renderAdmin();
        });
    }

    // V12新增: 按模板渲染底部右侧"DacatDHCP V版本号｜版权"
    // 模板来自 i18n 的 footer.right_template,占位符 %s 分别替换为版本号和版权
    // V13修复: 版本尚未加载时不显示空的"DacatDHCP V｜©",显示占位"-"
    // V14重构: 模板改为"DacatDHCP V版本号 · © 年份 ",年份动态生成,版权(DACAT.CC)作为外链追加
    //         版本号仍读取 savedVersion(来自 /api/version 唯一源),不新增重复版本常量
    function renderFooterRight() {
        var el = document.getElementById("footer-right-text");
        if (!el) return;
        // V13: 版本号尚未加载时显示占位符,不显示空模板
        if (!savedVersion && !savedCopyright) {
            el.textContent = "-";
            return;
        }
        var tmpl = window.I18N ? I18N.t("footer.right_template") : "DacatDHCP %s · © %s ";
        var ver = savedVersion || "";
        // V14: 年份动态生成,不硬编码
        var year = new Date().getFullYear();
        // 替换两个 %s 占位符: 第一个为版本号(带 V 前缀),第二个为动态年份
        var textPart = tmpl.replace("%s", "V" + ver).replace("%s", String(year));
        el.innerHTML = "";
        el.appendChild(document.createTextNode(textPart));
        // V14新增: 追加 DACAT.CC 外链,新窗口打开并添加 rel="noopener noreferrer"
        var link = document.createElement("a");
        link.href = "https://dacat.cc";
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.className = "footer-link";
        link.textContent = savedCopyright || "DACAT.CC";
        el.appendChild(link);
    }

    // V11新增: 单独渲染管理员权限状态,基于已保存的 savedIsAdmin,语言切换时调用此函数而非重新请求
    // V13修复: savedIsAdmin 尚未加载时显示"-",不临时显示"非管理员"
    function renderAdmin() {
        var adminEl = document.getElementById("footer-admin");
        if (!adminEl) return;
        // V13: savedIsAdmin 为 null(尚未加载)时显示占位符"-"
        if (savedIsAdmin === null) {
            adminEl.innerHTML = "-";
            return;
        }
        var isAdmin = savedIsAdmin === true;
        adminEl.setAttribute("data-admin-state", isAdmin ? "true" : "false");
        if (window.I18N) {
            adminEl.innerHTML = escHtml(I18N.t(isAdmin ? "footer.admin" : "footer.no_admin"));
        } else {
            adminEl.innerHTML = isAdmin ? "管理员权限" : "非管理员";
        }
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
                // V2新增: 恢复已保存的网关
                document.getElementById("gateway").value = cfg.gateway || "";
                // V10重构: DNS 改为可视化列表,dns-servers 输入框仅用于录入新条目
                dnsList = (cfg.dns_servers || []).slice();
                syncDnsDisplay();
            }
            // 配置加载完成后再加载网卡列表（保证 data-selected 已设置）
            loadAdapters();
        });
    }

    // ---- 加载网卡列表 ----
    // V13修复: 增加 allowRecommend 参数控制是否自动推荐地址池
    // 初始加载传 true 允许推荐;语言切换传 false 保留所有表单值不变
    // V13缓存: 网卡 API 数据存入 cachedAdapters,语言切换只重新渲染缓存数据不重新请求
    // V14修复: 增加请求序号 adapterReqSeq,旧回调返回时序号不匹配则跳过 autoFillPool

    function loadAdapters(allowRecommend) {
        // V14: 语言切换时使用缓存数据,不重新请求 API,保留所有表单值
        if (allowRecommend === false && cachedAdapters) {
            renderAdapters(cachedAdapters, allowRecommend);
            return;
        }
        // V14: 递增请求序号,此回调返回时若序号不匹配说明有新请求或语言切换发生,跳过 autoFillPool
        adapterReqSeq++;
        var mySeq = adapterReqSeq;
        get("/api/adapters", function (err, data) {
            if (err || !data || !data.adapters) return;
            // V18修复: 旧回调序号不匹配时直接 return,禁止更新 cachedAdapters
            // 只有当前最新请求才允许同时更新缓存并调用 renderAdapters
            if (mySeq !== adapterReqSeq) {
                return;
            }
            // V13: 缓存网卡 API 数据,供语言切换重新渲染
            cachedAdapters = data.adapters;
            // V14: 当前回调序号匹配,按调用方意图决定是否推荐地址池
            renderAdapters(data.adapters, allowRecommend);
        });
    }

    // V13新增: 渲染网卡下拉框和详情(基于缓存或新拉取的数据)
    function renderAdapters(adapters, allowRecommend) {
        var sel = document.getElementById("adapter-select");
        var selected = sel.getAttribute("data-selected") || "";
        // 清除旧选项（保留第一个占位）
        while (sel.options.length > 1) {
            sel.remove(1);
        }
        for (var i = 0; i < adapters.length; i++) {
            var a = adapters[i];
            // 跳过没有 IPv4 的网卡
            if (!a.hasIPv4) continue;
            var opt = document.createElement("option");
            opt.value = a.name;
            // 新增: 虚拟网卡标签使用 i18n
            var typeTag = a.type === "virtual" ? " " + (I18N ? I18N.t("adapter.virtual") : "[虚拟]") : "";
            opt.text = a.name + " - " + a.ip + typeTag;
            if (a.name === selected) {
                opt.selected = true;
            }
            sel.add(opt);
        }
        // V1修复: 网卡不存在时不自动选择错误网卡，保留占位选项
        // 更新网卡详情
        updateAdapterInfo();
        // V13: 仅初始加载(allowRecommend 非 false)时允许推荐地址池
        if (allowRecommend !== false) {
            autoFillPool();
        }
    }

    // 网卡选择变化时更新详情
    // V1修复: 用户主动切换网卡时清空地址池并重新请求推荐值
    // V2新增: 用户主动切换网卡时清空网关（DNS 保留原值）
    // V15修复: 切换网卡时重置 poolUserEditing 并递增 poolReqSeq,旧推荐回调失效
    // V17修复: 立即将当前网卡值(含空字符串)写入 data-selected 并递增 adapterReqSeq,
    //         使所有在途网卡列表请求失效,再更新详情和请求地址池
    function onAdapterChange() {
        var sel = document.getElementById("adapter-select");
        // V17: 立即保存当前值(包括空字符串),防止旧请求覆盖用户选择
        sel.setAttribute("data-selected", sel.value || "");
        // V17: 递增序号使所有在途网卡列表请求失效,旧回调不得调用 renderAdapters
        adapterReqSeq++;
        updateAdapterInfo();
        // V15: 切换网卡时清除用户编辑标记,允许新网卡重新推荐
        poolUserEditing = false;
        requestPoolRecommend();
        document.getElementById("gateway").value = "";
    }

    // V15新增: 用户开始手动编辑地址池输入时,使在途推荐请求失效
    // 通过 oninput/onchange 事件绑定到 pool-start/pool-end
    function onPoolInput() {
        poolUserEditing = true;
        // V15: 递增序号使在途推荐请求回调失效
        poolReqSeq++;
    }

    // V13重构: 网卡详情显示真实 IP/掩码/MAC/类型,不再重复下拉框完整文字
    function updateAdapterInfo() {
        var sel = document.getElementById("adapter-select");
        var info = document.getElementById("adapter-info");
        var val = sel.value;
        if (!val) {
            info.textContent = ""; // V12: 改用 textContent 避免 innerHTML 注入网卡数据
            return;
        }
        // V13: 从缓存数据中查找网卡详情,显示真实 IP/掩码/MAC/类型
        var adapter = null;
        if (cachedAdapters) {
            for (var i = 0; i < cachedAdapters.length; i++) {
                if (cachedAdapters[i].name === val) {
                    adapter = cachedAdapters[i];
                    break;
                }
            }
        }
        if (!adapter) {
            info.textContent = "";
            return;
        }
        // V13: 构造详情文本,使用 textContent 安全写入
        var typeLabel = adapter.type === "virtual"
            ? (I18N ? I18N.t("adapter.virtual") : "[虚拟]")
            : (I18N && I18N.t("adapter.physical") ? I18N.t("adapter.physical") : "[物理]");
        var parts = [];
        parts.push("IP: " + (adapter.ip || "-"));
        // V14修复: 掩码拼接括号优先级错误,先拼接标签再拼接值,确保显示"掩码: 255.255.255.0"
        if (adapter.mask) parts.push((I18N ? I18N.t("adapter.mask") : "掩码") + ": " + adapter.mask);
        if (adapter.mac) parts.push("MAC: " + adapter.mac);
        parts.push(typeLabel);
        info.textContent = parts.join("  |  ");
    }

    // V1修复: 初始加载时自动填充地址池（如果已有保存值则保留，否则请求推荐）
    // V15修复: 用户已开始手动编辑时不自动填充
    function autoFillPool() {
        var sel = document.getElementById("adapter-select");
        var val = sel.value;
        if (!val) return;

        var startEl = document.getElementById("pool-start");
        var endEl = document.getElementById("pool-end");
        // 如果地址池已有值（来自保存的配置），不覆盖
        if (startEl.value && endEl.value) return;
        // V15: 用户已开始手动编辑时不自动填充
        if (poolUserEditing) return;

        // 无保存值时请求后端推荐
        requestPoolRecommend();
    }

    // V1修复: 用户主动切换网卡时强制清空地址池并请求后端推荐
    // V15修复: 增加独立 poolReqSeq,回调返回时同时校验序号和当前网卡名
    // 旧请求不得覆盖新网卡或用户已修改的起始/结束 IP
    // V16修复: 每次调用先递增 poolReqSeq 并清空起止 IP,再判网卡是否为空
    // 选择空白网卡时必须立即清除旧地址池并使所有在途推荐失效(不发起后端请求)
    function requestPoolRecommend() {
        var startEl = document.getElementById("pool-start");
        var endEl = document.getElementById("pool-end");

        // V16: 无论后续是否发请求,都先递增序号使在途请求失效,并清空地址池
        poolReqSeq++;
        startEl.value = "";
        endEl.value = "";

        var sel = document.getElementById("adapter-select");
        var val = sel.value;
        // V16: 空白网卡立即清除旧地址池并使所有在途推荐失效,不再发起后端请求
        if (!val) return;

        // V16: 捕获本次请求的序号和网卡名,回调返回时校验
        var mySeq = poolReqSeq;
        var myAdapter = val;

        get("/api/pool-recommend?adapter_name=" + encodeURIComponent(val), function (err, data) {
            if (err || !data) return;
            // V15: 校验 1 - 序号不匹配说明有新请求或用户编辑发生,丢弃旧响应
            if (mySeq !== poolReqSeq) return;
            // V15: 校验 2 - 当前网卡名与请求时不一致(用户已切换网卡),丢弃旧响应
            var curSel = document.getElementById("adapter-select");
            if (!curSel || curSel.value !== myAdapter) return;
            // V15: 校验 3 - 用户已开始手动编辑,丢弃旧响应
            if (poolUserEditing) return;
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
            // V12: 删除不存在的 service-status 残留引用,仅保留 HTML 中实际存在的元素
            var statusDot = document.getElementById("status-dot");     // V10新增: 圆形状态图标
            var statusState = document.getElementById("status-state"); // V10新增: 运行/停止状态
            var statusSub = document.getElementById("status-sub");     // V10新增: 运行/停止副标题
            var btnStart = document.getElementById("btn-start");
            var btnStop = document.getElementById("btn-stop");
            var errorEl = document.getElementById("error-msg");
            var ipEl = document.getElementById("server-ip");
            var poolEl = document.getElementById("pool-info");
            var usageEl = document.getElementById("pool-usage");
            var uptimeEl = document.getElementById("status-uptime"); // V10新增: 运行时间

            // V10新增: 记录真实启动时间,供时钟定时器计算运行时间(禁止前端伪造)
            lastStartedAt = (status.running && status.started_at) ? status.started_at : null;

            if (status.running) {
                // V11修复: status-state 必须设置 running/stopped 类,确保颜色生效
                if (statusState) {
                    statusState.innerHTML = I18N ? I18N.t("status.running") : "运行中";
                    statusState.className = "status-state running";
                }
                if (statusSub) statusSub.innerHTML = I18N ? I18N.t("status.running.subtitle") : "服务运行中";
                if (statusDot) statusDot.className = "status-dot running";
                btnStart.disabled = true;
                btnStop.disabled = false;
                setConfigDisabled(true);
            } else {
                // V11修复: status-state 必须设置 running/stopped 类,确保颜色生效
                if (statusState) {
                    statusState.innerHTML = I18N ? I18N.t("status.stopped") : "已停止";
                    statusState.className = "status-state stopped";
                }
                if (statusSub) statusSub.innerHTML = I18N ? I18N.t("status.stopped.subtitle") : "服务已停止";
                if (statusDot) statusDot.className = "status-dot stopped";
                btnStart.disabled = false;
                btnStop.disabled = true;
                setConfigDisabled(false);
            }

            if (ipEl) ipEl.innerHTML = escHtml(status.server_ip || "-");
            if (poolEl) poolEl.innerHTML = escHtml((status.pool_start || "-") + " ~ " + (status.pool_end || "-"));
            if (usageEl) usageEl.innerHTML = escHtml((status.pool_used || 0) + " / " + (status.pool_total || 0));

            // V10新增: 立即更新一次运行时间(后续由时钟定时器每秒更新)
            updateUptime();

            if (status.error) {
                // V10新增: 后端错误使用 I18N.te() 翻译,英文界面不直接展示中文
                errorEl.innerHTML = window.I18N ? escHtml(I18N.te(status.error)) : escHtml(status.error);
                errorEl.style.display = "block";
            } else {
                errorEl.style.display = "none";
            }
        });
    }

    // 运行时锁定配置输入
    // V2新增: 网关和 DNS 与网卡、地址池、租约时间一起锁定
    // V10新增: DNS 添加/清除按钮一并锁定
    function setConfigDisabled(disabled) {
        document.getElementById("adapter-select").disabled = disabled;
        document.getElementById("pool-start").disabled = disabled;
        document.getElementById("pool-end").disabled = disabled;
        document.getElementById("lease-time").disabled = disabled;
        document.getElementById("gateway").disabled = disabled;
        document.getElementById("dns-servers").disabled = disabled;
        // V10新增: DNS 操作按钮与输入框同步锁定
        var btnAdd = document.getElementById("btn-dns-add");
        var btnClear = document.getElementById("btn-dns-clear");
        if (btnAdd) btnAdd.disabled = disabled;
        if (btnClear) btnClear.disabled = disabled;
    }

    // ---- 启动服务 ----

    window.startService = function () {
        var adapter = document.getElementById("adapter-select").value;
        var poolStart = document.getElementById("pool-start").value.trim();
        var poolEnd = document.getElementById("pool-end").value.trim();
        var leaseTime = parseInt(document.getElementById("lease-time").value, 10);
        // V2新增: 读取网关（可选）
        var gateway = document.getElementById("gateway").value.trim();
        // V10重构: DNS 从可视化列表提交(保留数组提交逻辑,来源改为 dnsList)
        var dnsServers = dnsList.slice();

        // 前端校验
        // 新增: 校验提示文案使用 i18n
        if (!adapter) {
            alert(I18N ? I18N.t("alert.select_adapter") : "请选择服务网卡");
            return;
        }
        if (!poolStart) {
            alert(I18N ? I18N.t("alert.pool_start") : "请输入起始 IP 地址");
            return;
        }
        if (!poolEnd) {
            alert(I18N ? I18N.t("alert.pool_end") : "请输入结束 IP 地址");
            return;
        }
        if (isNaN(leaseTime) || leaseTime <= 0) {
            alert(I18N ? I18N.t("alert.lease_time") : "租约时间必须大于 0");
            return;
        }

        var btnStart = document.getElementById("btn-start");
        btnStart.disabled = true;
        // V12: 按钮含 SVG+span 结构,仅更新 span 文本避免覆盖图标
        setBtnText(btnStart, I18N ? I18N.t("status.starting") : "启动中...");

        post("/api/start", {
            adapter_name: adapter,
            pool_start: poolStart,
            pool_end: poolEnd,
            lease_minutes: leaseTime,
            gateway: gateway,
            dns_servers: dnsServers
        }, function (err, resp) {
            setBtnText(btnStart, I18N ? I18N.t("status.start_dhcp") : "启动 DHCP");
            if (err) {
                // P0安全: CSRF 令牌失效单独提示"请刷新页面",不附加"启动失败:"前缀
                if (handleCSRFError(err)) {
                    btnStart.disabled = false;
                    return;
                }
                // V11重构: err 为 {message, code} 对象,优先按 code 翻译,回退到中文片段翻译
                var errMsg = (typeof err === "object" && err) ? err.message : String(err);
                var errCode = (typeof err === "object" && err) ? err.code : "";
                var translated = window.I18N ? I18N.teByCode(errCode, errMsg) : errMsg;
                alert((I18N ? I18N.t("alert.start_fail") : "启动失败: ") + translated);
                btnStart.disabled = false;
            } else {
                // V14: DHCP 已启动,检查配置保存是否失败(服务已启动但配置未持久化)
                // 不当作启动失败处理(服务确实在运行),仅提示配置保存失败
                if (resp && resp.config_save_fail) {
                    var saveCode = resp.code || "config_save_failed";
                    var saveMsg = resp.error || "";
                    var saveTranslated = window.I18N ? I18N.teByCode(saveCode, saveMsg) : saveMsg;
                    alert((I18N ? I18N.t("alert.config_save_warn") : "DHCP 已启动,但配置保存失败: ") + saveTranslated);
                }
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
        // V12: 按钮含 SVG+span 结构,仅更新 span 文本避免覆盖图标
        setBtnText(btnStop, I18N ? I18N.t("status.stopping") : "停止中...");

        post("/api/stop", null, function (err, resp) {
            setBtnText(btnStop, I18N ? I18N.t("status.stop") : "停止服务");
            if (err) {
                // P0安全: CSRF 令牌失效单独提示"请刷新页面",不附加"停止失败:"前缀
                if (handleCSRFError(err)) {
                    btnStop.disabled = false;
                    return;
                }
                // V11重构: err 为 {message, code} 对象,优先按 code 翻译,回退到中文片段翻译
                var errMsg = (typeof err === "object" && err) ? err.message : String(err);
                var errCode = (typeof err === "object" && err) ? err.code : "";
                var translated = window.I18N ? I18N.teByCode(errCode, errMsg) : errMsg;
                alert((I18N ? I18N.t("alert.stop_fail") : "停止失败: ") + translated);
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
            // V10新增: 标题右侧显示真实总数
            var totalEl = document.getElementById("lease-total-count");
            if (totalEl) totalEl.innerHTML = String(leases.length);
            if (leases.length === 0) {
                // 新增: 空状态文案使用 i18n
                var emptyText = I18N ? I18N.t("lease.empty") : "暂无租约";
                tbody.innerHTML = '<tr><td colspan="6" class="empty-state">' +
                    '<svg class="empty-icon" width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="8" y1="10" x2="16" y2="10"/><line x1="8" y1="14" x2="16" y2="14"/></svg>' +
                    '<span>' + escHtml(emptyText) + '</span></td></tr>';
                return;
            }
            var html = "";
            for (var i = 0; i < leases.length; i++) {
                var l = leases[i];
                // V11新增: 租约 active/expired 状态增加中英文映射
                var statusText = l.status;
                if (window.I18N) {
                    if (l.status === "active") statusText = I18N.t("lease.status_active");
                    else if (l.status === "expired") statusText = I18N.t("lease.status_expired");
                    else if (l.status === "pending") statusText = I18N.t("lease.status_pending");
                }
                html += "<tr>";
                html += "<td>" + escHtml(l.ip) + "</td>";
                html += "<td>" + escHtml(l.mac) + "</td>";
                html += "<td>" + escHtml(l.hostname || "-") + "</td>";
                html += "<td>" + escHtml(l.assigned_at) + "</td>";
                html += "<td>" + escHtml(l.expires_at) + "</td>";
                html += "<td>" + escHtml(statusText) + "</td>";
                html += "</tr>";
            }
            tbody.innerHTML = html;
        });
    }

    // V10新增: 暴露 refreshLeases 供 HTML onclick 调用
    window.refreshLeases = refreshLeases;

    // ---- 刷新日志 ----

    function refreshLogs() {
        get("/api/logs", function (err, data) {
            if (err || !data) return;
            var logs = data.logs || [];
            var box = document.getElementById("log-box");
            // V11新增: 更新前记录原 scrollTop,自动滚动关闭时保留原位置
            var savedScrollTop = box ? box.scrollTop : 0;
            var text = "";
            for (var i = 0; i < logs.length; i++) {
                text += logs[i] + "\n";
            }
            box.innerHTML = "";
            box.appendChild(document.createTextNode(text));
            // V11重构: 自动滚动开启时才滚到底部;关闭时必须保留原 scrollTop
            var autoChk = document.getElementById("autoscroll-check");
            var shouldScroll = !autoChk || autoChk.checked;
            if (shouldScroll) {
                box.scrollTop = box.scrollHeight;
            } else {
                box.scrollTop = savedScrollTop;
            }
        });
    }

    // V10新增: 快捷操作 - 同时刷新状态、租约、日志
    window.refreshAll = function () {
        refreshStatus();
        refreshLeases();
        refreshLogs();
    };

    // ---- V10新增: 本机时间时钟与运行时间 ----

    // 启动本机时间时钟(每秒更新底部状态栏中间时间和运行时间)
    function startClock() {
        updateClock();
        if (clockTimer) clearInterval(clockTimer);
        clockTimer = setInterval(function () {
            updateClock();
            updateUptime();
        }, 1000);
    }

    // 更新底部状态栏本机时间
    function updateClock() {
        var el = document.getElementById("footer-time");
        if (el) el.innerHTML = formatClock(new Date());
    }

    // 更新运行时间(基于后端提供的真实 started_at,禁止前端伪造)
    function updateUptime() {
        var el = document.getElementById("status-uptime");
        if (!el) return;
        if (!lastStartedAt) {
            el.innerHTML = "-";
            return;
        }
        var start = new Date(lastStartedAt);
        if (isNaN(start.getTime())) {
            el.innerHTML = "-";
            return;
        }
        var uptimeMs = new Date().getTime() - start.getTime();
        el.innerHTML = formatUptime(uptimeMs);
    }

    // ---- V10新增: DNS 可视化操作 ----

    // 同步 DNS 当前列表显示
    function syncDnsDisplay() {
        var el = document.getElementById("dns-current");
        if (!el) return;
        if (dnsList.length === 0) {
            el.innerHTML = "-";
        } else {
            el.innerHTML = escHtml(dnsList.join(", "));
        }
    }

    // 添加 DNS 服务器: 解析输入框内容,校验 IPv4,去重,限制最多 3 个
    window.addDnsServer = function () {
        var input = document.getElementById("dns-servers");
        if (!input) return;
        var raw = input.value.trim();
        if (!raw) return;
        var parts = raw.split(",");
        var added = 0;
        for (var i = 0; i < parts.length; i++) {
            var s = parts[i].trim();
            if (!s) continue;
            if (!isValidIPv4(s)) {
                alert((I18N ? I18N.t("alert.dns_invalid") : "DNS 地址无效: ") + s);
                return;
            }
            // 去重
            var exists = false;
            for (var j = 0; j < dnsList.length; j++) {
                if (dnsList[j] === s) { exists = true; break; }
            }
            if (exists) continue;
            if (dnsList.length >= 3) {
                alert(I18N ? I18N.t("alert.dns_max") : "DNS 服务器最多允许 3 个");
                break;
            }
            dnsList.push(s);
            added++;
        }
        // 清空输入框,准备录入下一条
        input.value = "";
        syncDnsDisplay();
    };

    // 清除所有 DNS 服务器
    window.clearDnsServers = function () {
        dnsList = [];
        var input = document.getElementById("dns-servers");
        if (input) input.value = "";
        syncDnsDisplay();
    };

    // ---- V10新增: 日志操作 ----

    // 导出日志: 将当前日志框内容下载为文件
    window.exportLogs = function () {
        var box = document.getElementById("log-box");
        if (!box) return;
        var text = box.textContent || box.innerText || "";
        if (!text) {
            alert(I18N ? I18N.t("logs.empty") : "暂无日志");
            return;
        }
        var filename = "dhcpsrv-" + formatStamp(new Date()) + ".log";
        var blob = new Blob([text], { type: "text/plain;charset=utf-8" });
        // 统一使用 Blob + createObjectURL 下载(IE 兼容分支已移除,ie11-check.js 已拦截 IE)
        var url = URL.createObjectURL(blob);
        var a = document.createElement("a");
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
    };

    // V12重构: 清空日志调用真实 POST /api/logs/clear 接口
    // 后端 Logger.Clear() 在互斥锁内清空内存环形缓冲区并截断日志文件,成功后立即刷新日志
    window.clearLogs = function () {
        var confirmMsg = I18N ? I18N.t("logs.clear.confirm") : "确定清空所有日志吗?此操作不可恢复,内存和日志文件将同时清空。";
        if (!confirm(confirmMsg)) return;
        var btn = document.getElementById("btn-clear-logs");
        if (btn) btn.disabled = true;
        post("/api/logs/clear", null, function (err, resp) {
            if (btn) btn.disabled = false;
            if (err) {
                // P0安全: CSRF 令牌失效单独提示"请刷新页面",不附加"清空日志失败:"前缀
                if (handleCSRFError(err)) {
                    return;
                }
                // V11重构: err 为 {message, code} 对象,优先按 code 翻译,回退到中文片段翻译
                var errMsg = (typeof err === "object" && err) ? err.message : String(err);
                var errCode = (typeof err === "object" && err) ? err.code : "";
                var translated = window.I18N ? I18N.teByCode(errCode, errMsg) : errMsg;
                alert((I18N ? I18N.t("logs.clear.fail") : "清空日志失败: ") + translated);
                return;
            }
            // V12: 成功后立即刷新日志,从后端重新拉取(此时缓冲区已清空)
            refreshLogs();
        });
    };

    // ---- 国际化与主题相关全局函数 ----

    // V10重构: 语言切换改为"中文｜EN"双项切换,替代旧的 toggleLang
    // 语言新增: 先调用 PUT /api/language 保存到后端,成功后再刷新页面文案
    // 失败则恢复服务端当前语言并显示中英文错误提示,禁止在前端独立于后端切换语言
    // 语言新增: 请求期间禁用顶部语言按钮和设置页语言单选项,防止重复点击和请求乱序
    // 语言新增: 递增请求序号,过期响应不得覆盖用户最后一次选择;请求结束后只由最后一次有效请求恢复控件状态
    window.switchLang = function (lang) {
        if (!window.I18N) return;
        if (lang !== "zh-CN" && lang !== "en-US") return;
        if (I18N.getLang() === lang) return;
        // 递增请求序号,禁用控件,防止重复点击和请求乱序
        langReqSeq++;
        var mySeq = langReqSeq;
        setLangControlsEnabled(false);
        putLanguage(lang, function (err) {
            // 旧请求返回时不得覆盖用户最后一次选择
            if (mySeq !== langReqSeq) return;
            if (err) {
                // 失败:显示本地化错误,恢复服务端当前语言、按钮和单选状态
                showLanguageSaveError(err);
                restoreServerLanguage(mySeq);
                return;
            }
            // 成功:刷新页面语言、顶部按钮 active、设置页单选框 checked 及托盘回调
            applyLanguageChange(lang);
            if (window.I18N) {
                checkRadio("settings-lang", lang);
            }
            setLangControlsEnabled(true);
        });
    };

    // 语言新增: 调用 PUT /api/language 保存语言到后端
    // 复用 ajax 函数,自动携带 CSRF 令牌和 Content-Type
    // callback(err): err 为 null 表示成功,否则为 {message, code} 对象
    function putLanguage(lang, callback) {
        ajax("PUT", "/api/language", {language: lang}, function (err, resp) {
            if (err) {
                callback(err);
                return;
            }
            callback(null);
        });
    }

    // 语言新增: 显示语言保存失败的错误提示(中英文)
    // 使用 teByCode 按错误码翻译,兼容 CSRF 令牌失效等安全错误
    function showLanguageSaveError(err) {
        var msg = (err && err.message) ? err.message : "Failed to save language";
        var code = (err && err.code) ? err.code : "";
        var text = window.I18N ? I18N.teByCode(code, msg) : msg;
        // 使用原生 alert,不引入额外 UI 组件
        try {
            window.alert(text);
        } catch (e) {
            // alert 不可用时忽略
        }
    }

    // 语言新增: 禁用/启用顶部语言按钮和设置页语言单选项
    // 请求期间禁用控件,防止重复点击和请求乱序
    function setLangControlsEnabled(enabled) {
        var ids = ["lang-zh", "lang-en", "settings-lang-zh", "settings-lang-en"];
        for (var i = 0; i < ids.length; i++) {
            var el = document.getElementById(ids[i]);
            if (el) el.disabled = !enabled;
        }
    }

    // 语言新增: 失败时从服务端恢复当前语言状态
    // GET /api/language 获取服务端真实语言,返回值必须校验为 zh-CN 或 en-US 后才能应用
    // GET 失败或返回值无效时,以 I18N.getLang() 作为实际语言重新同步控件,禁止页面与单选框不一致
    // restoreSeq 为发起恢复时的序号,回调返回时若序号不匹配说明有新请求,跳过并交由最新请求处理
    function restoreServerLanguage(restoreSeq) {
        get("/api/language", function (err, data) {
            // 旧恢复请求返回时不再处理,由最新请求负责控件状态
            if (restoreSeq !== langReqSeq) return;
            var lang = null;
            if (!err && data && (data.language === "zh-CN" || data.language === "en-US")) {
                // GET 成功且返回值有效,使用服务端语言
                lang = data.language;
            } else {
                // GET 失败或返回值无效,以 I18N.getLang() 作为实际语言同步控件
                lang = window.I18N ? I18N.getLang() : "zh-CN";
            }
            // 同步页面文案、顶部按钮 active 状态、设置页单选框 checked 状态
            applyLanguageChange(lang);
            if (window.I18N) {
                checkRadio("settings-lang", lang);
            }
            setLangControlsEnabled(true);
        });
    }

    // 应用语言变更: 保存、刷新文案、更新分段控件、更新 <html lang>、更新主题按钮 aria-label
    function applyLanguageChange(newLang) {
        I18N.setLang(newLang);
        I18N.applyTranslations();
        updateLangSeg();
        // 同步 <html lang> 属性
        document.documentElement.setAttribute("lang", newLang);
        // V10新增: 主题按钮的 aria-label/title 依赖语言,需同步更新
        updateThemeButton();
        // V11新增: 单独渲染管理员权限状态(基于已保存的真实值,不得因语言切换而改变)
        renderAdmin();
        // V12新增: 重新渲染底部右侧模板(版本号和版权按新语言模板显示)
        renderFooterRight();
        // V12修复: 语言切换后重新加载网卡列表以更新虚拟网卡标签和网卡详情
        // V13重构: 传 false 禁止推荐地址池,使用缓存数据保留所有表单值(网卡/IP/租约/网关/DNS/dnsList)
        // V14修复: 始终保存 adapter-select 当前值(含空字符串),递增序号使旧网卡请求回调失效
        var sel = document.getElementById("adapter-select");
        if (sel) {
            // V14: 始终保存当前值(包括空字符串),不得恢复旧网卡
            sel.setAttribute("data-selected", sel.value || "");
        }
        // V14: 递增序号使任何在途的网卡请求回调失效,防止旧回调执行 autoFillPool
        adapterReqSeq++;
        // V15: 同时使在途的地址池推荐请求失效,防止旧响应覆盖用户表单
        poolReqSeq++;
        loadAdapters(false);
        // 新增: 刷新动态生成的文案(状态、按钮、空状态),保持界面与当前状态一致
        refreshStatus();
        refreshLeases();
    }

    // V10新增: 更新语言分段控件的 active 状态
    function updateLangSeg() {
        var lang = window.I18N ? I18N.getLang() : "zh-CN";
        var btnZh = document.getElementById("lang-zh");
        var btnEn = document.getElementById("lang-en");
        if (btnZh) {
            if (lang === "zh-CN") btnZh.className = "lang-seg-btn active";
            else btnZh.className = "lang-seg-btn";
        }
        if (btnEn) {
            if (lang === "en-US") btnEn.className = "lang-seg-btn active";
            else btnEn.className = "lang-seg-btn";
        }
    }

    // V10新增: 主题图标按钮 - 切换主题并更新图标状态和 aria-label/title
    window.toggleTheme = function () {
        if (!window.THEME) return;
        THEME.toggleTheme();
        updateThemeButton();
    };

    // V10新增: 同步主题按钮的 aria-label/title(浅色显示月亮提示切深色,深色显示太阳提示切浅色)
    function updateThemeButton() {
        var btn = document.getElementById("btn-theme");
        if (!btn || !window.THEME) return;
        var theme = THEME.getTheme();
        var key = (theme === "dark") ? "theme.toggle.dark" : "theme.toggle.light";
        var label = window.I18N ? I18N.t(key) : (theme === "dark" ? "切换到浅色主题" : "切换到深色主题");
        btn.setAttribute("aria-label", label);
        btn.setAttribute("title", label);
    }

    // 设置弹窗: 打开时同步当前语言选择(V10: 主题选项已删除,仅同步语言)
    window.openSettings = function () {
        var modal = document.getElementById("settings-modal");
        if (!modal) return;
        // 同步语言单选
        if (window.I18N) {
            checkRadio("settings-lang", I18N.getLang());
        }
        modal.style.display = "flex";
    };

    window.closeSettings = function () {
        var modal = document.getElementById("settings-modal");
        if (modal) modal.style.display = "none";
    };

    // 设置弹窗内语言单选变化: 立即切换语言
    // 语言新增: 先调用 PUT /api/language 保存,成功后再刷新页面,失败恢复服务端语言
    // 语言新增: 复用 switchLang 的请求序号和控件禁用机制,防止重复点击和请求乱序
    window.onSettingsLangChange = function (lang) {
        if (!window.I18N) return;
        if (lang !== "zh-CN" && lang !== "en-US") return;
        if (I18N.getLang() === lang) return;
        // 递增请求序号,禁用控件,防止重复点击和请求乱序
        langReqSeq++;
        var mySeq = langReqSeq;
        setLangControlsEnabled(false);
        putLanguage(lang, function (err) {
            // 旧请求返回时不得覆盖用户最后一次选择
            if (mySeq !== langReqSeq) return;
            if (err) {
                // 失败:显示本地化错误,恢复服务端当前语言、按钮和单选状态
                showLanguageSaveError(err);
                restoreServerLanguage(mySeq);
                return;
            }
            // 成功:刷新页面语言、顶部按钮 active、设置页单选框 checked 及托盘回调
            applyLanguageChange(lang);
            if (window.I18N) {
                checkRadio("settings-lang", lang);
            }
            setLangControlsEnabled(true);
        });
    };

    // 关于弹窗: 打开时确保版本信息已填充
    window.openAbout = function () {
        var modal = document.getElementById("about-modal");
        if (!modal) return;
        modal.style.display = "flex";
        // 若版本信息尚未加载,主动加载一次
        var verEl = document.getElementById("about-version");
        if (verEl && !verEl.innerHTML) {
            loadVersion();
        }
    };

    window.closeAbout = function () {
        var modal = document.getElementById("about-modal");
        if (modal) modal.style.display = "none";
    };

    // 选中指定 name 的单选按钮(value 不匹配则取消选中)
    function checkRadio(name, value) {
        var radios = document.getElementsByName(name);
        for (var i = 0; i < radios.length; i++) {
            if (radios[i].value === value) {
                radios[i].checked = true;
            } else {
                radios[i].checked = false;
            }
        }
    }

    // 绑定网卡选择事件(统一使用 addEventListener)
    document.getElementById("adapter-select").addEventListener("change", onAdapterChange);

    // V15新增: 绑定地址池输入事件,用户开始手动编辑时使在途推荐请求失效
    // 统一使用 addEventListener(IE 兼容分支已移除,ie11-check.js 已拦截 IE)
    (function () {
        var poolStartEl = document.getElementById("pool-start");
        var poolEndEl = document.getElementById("pool-end");
        var bindInput = function (el) {
            if (!el) return;
            el.addEventListener("input", onPoolInput);
        };
        bindInput(poolStartEl);
        bindInput(poolEndEl);
    })();

    // 页面加载完成后初始化(统一使用 DOMContentLoaded,IE 兼容分支已移除)
    if (document.readyState === "complete" || document.readyState === "interactive") {
        init();
    } else {
        document.addEventListener("DOMContentLoaded", init);
    }

    // V18修复: 测试钩子仅在 window.__DACAT_TEST__ === true 时暴露
    // 正式运行环境不得暴露测试入口,Node 测试加载 app.js 前设置该标记
    if (typeof window !== "undefined" && window.__DACAT_TEST__ === true) {
        window._testLoadAdapters = loadAdapters;
        window._testApplyLanguageChange = applyLanguageChange;
    }
})();
