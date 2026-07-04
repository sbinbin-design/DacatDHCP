/* DacatDHCP 国际化资源 (zh-CN / en-US) */
/* IE11 兼容: 仅使用 var/function,禁止 const/let/箭头函数/模板字符串 */
/* 统一管理界面文案,禁止在 HTML/JS 中硬编码固定文字 */
/* DHCP 原始日志内容保持原样,只翻译界面标签和程序自身生成的提示 */
/* V10新增: 后端错误码映射,英文界面不直接展示固定中文错误 */

(function (window) {
    "use strict";

    // 语言资源表
    var dict = {
        "zh-CN": {
            "_label": "中文",

            // 顶部品牌与导航
            "app.title": "DacatDHCP",
            "app.subtitle": "轻量 · 稳定 · 高效的 DHCP 服务工具", // V12: 副标题改为新文案
            "nav.settings": "设置",
            "nav.about": "关于",

            // 主题
            "theme.light": "浅色",
            "theme.dark": "深色",
            "theme.title": "主题",
            "theme.toggle.light": "切换到深色主题", // V10: 浅色下显示月亮,点击切深色
            "theme.toggle.dark": "切换到浅色主题",  // V10: 深色下显示太阳,点击切浅色

            // 安全提示
            "security.title": "安全提示",
            // V12: security.text 已停用,改用 security.warning/detail/scope 分段文案

            // 服务状态
            "section.status": "服务状态",
            // V12: status.label 已停用,状态标签由 status-state 元素直接展示
            "status.running": "运行中",
            "status.stopped": "已停止",
            "status.running.subtitle": "服务运行中",
            "status.stopped.subtitle": "服务已停止",
            "status.server_ip": "服务端 IP：",
            "status.pool": "地址池：",
            "status.usage": "已分配 / 总数：",
            "status.uptime": "运行时间：", // V10新增: 真实运行时间
            "status.days": "天", // V11新增: 运行时间超过24小时显示"天数 + HH:MM:SS",不使用 h/m/s 缩写
            "status.start_dhcp": "启动 DHCP", // V12: 启动按钮中文文案改为"启动 DHCP"(status.start 已停用)
            "status.starting": "启动中...",
            "status.stop": "停止服务",
            "status.stopping": "停止中...",

            // 网卡与地址池配置
            "section.config": "网卡与地址池配置",
            "config.adapter": "服务网卡：",
            "config.adapter.placeholder": "-- 请选择网卡 --",
            "config.adapter.detail": "网卡详情：", // V10新增: 网卡详情独立显示
            "config.pool_start": "起始 IP：",
            "config.pool_end": "结束 IP：",
            "config.lease_time": "租约时间（分钟）：",
            "config.gateway": "默认网关（可选）：",
            "config.gateway.placeholder": "留空则不下发",
            "config.dns": "DNS 服务器（可选）：",
            "config.dns.placeholder": "多个 IPv4 用英文逗号分隔，最多 3 个",
            "config.dns.add": "添加",   // V10新增: DNS 添加操作
            "config.dns.clear": "清除", // V10新增: DNS 清除操作
            // V12: config.dns.current 已停用,DNS 值改为紧凑标签内联显示,不再使用独立标签
            // V12: 网关和 DNS 说明合并为卡片底部一条提示,减少卡片高度
            "config.bottom_hint": "网关须与所选网卡同子网,且不得位于地址池内;网关和 DNS 均为可选项,留空则不会向客户端下发。DacatDHCP 不会自动启用 Windows 路由、NAT 或网络共享。",

            // 当前租约
            "section.leases": "当前租约",
            "lease.ip": "IP 地址",
            "lease.mac": "MAC 地址",
            "lease.hostname": "主机名",
            "lease.assigned_at": "分配时间",
            "lease.expires_at": "到期时间",
            "lease.status": "状态",
            "lease.empty": "暂无租约",
            // V11新增: 租约 active/expired/pending 状态中英文映射,禁止直接展示后端原始字符串
            "lease.status_active": "活跃",
            "lease.status_expired": "已过期",
            "lease.status_pending": "待确认",
            "lease.total": "总数", // V10新增: 标题右侧显示真实总数

            // 运行日志
            "section.logs": "运行日志",
            "logs.empty": "暂无日志",
            "logs.autoscroll": "自动滚动", // V10新增: 自动滚动开关
            "logs.export": "导出日志",     // V10新增: 导出日志按钮
            "logs.clear": "清空日志",      // V10新增
            // V12: logs.clear.tip 已停用,清空日志已实现真实后端接口,改用 confirm/fail
            "logs.clear.confirm": "确定清空所有日志吗?此操作不可恢复,内存和日志文件将同时清空。", // V12新增: 清空日志确认
            "logs.clear.fail": "清空日志失败: ", // V12新增

            // 快捷操作
            "quick.title": "快捷操作",         // V10新增
            "quick.refresh_status": "刷新信息", // V10新增
            "quick.refresh_leases": "刷新租约", // V10新增

            // 底部状态栏
            // V12: footer.text 和 footer.version 已停用,版本号通过 footer.right_template 统一渲染
            "footer.admin": "管理员权限",     // V10新增: 真实权限状态
            "footer.no_admin": "非管理员",    // V10新增
            "footer.secure_run": "安全运行中", // V10新增
            "footer.local_time": "本机时间",   // V10新增: 前端实时更新
            // V12新增: 底部右侧"产品名 V版本号｜版权"模板,%s 占位符由前端填充
            // V14调整: 模板改为"DacatDHCP V版本号 · © 年份 ",年份动态生成,版权(DACAT.CC)作为外链由前端追加
            "footer.right_template": "DacatDHCP %s · © %s ",
            // V12新增: 安全提示分段文案(标题/警示/详情/适用场景)
            "security.warning": "警告",
            "security.detail": "本工具仅用于直连、测试网络或隔离网络,禁止在已有 DHCP 服务的生产网络中启用。",
            "security.scope": "适用场景: 自建网络 / 实验室 / 设备测试 / 临时网络",

            // 校验提示
            "alert.select_adapter": "请选择服务网卡",
            "alert.pool_start": "请输入起始 IP 地址",
            "alert.pool_end": "请输入结束 IP 地址",
            "alert.lease_time": "租约时间必须大于 0",
            "alert.start_fail": "启动失败: ",
            "alert.stop_fail": "停止失败: ",
            "alert.config_save_warn": "DHCP 已启动,但配置保存失败: ",
            // V10新增: DNS 前端校验提示
            "alert.dns_invalid": "DNS 地址无效: ",
            "alert.dns_max": "DNS 服务器最多允许 3 个",

            // 网卡标签
            "adapter.virtual": "[虚拟]",
            "adapter.physical": "[物理]", // V13新增: 物理网卡标签
            "adapter.mask": "掩码",       // V13新增: 网卡详情掩码字段

            // 设置弹窗
            "settings.title": "设置",
            "settings.language": "界面语言",
            // V12: 删除已停用的 settings.theme(主题选项已移除,仅保留语言)
            "settings.close": "关闭",
            "settings.lang.zh": "中文",
            "settings.lang.en": "English",

            // 关于弹窗
            "about.title": "关于",
            "about.product": "产品名称",
            "about.version": "版本",
            "about.file_version": "文件版本",
            "about.copyright": "版权",
            "about.description": "DacatDHCP 是一款 Windows 便携式轻量 DHCP 服务工具，通过单 EXE 提供图形化管理界面。",
            "about.support": "正式支持 Windows 10/11、Windows Server 2016+",
            "about.warning": "本工具仅适用于自建、测试或隔离网络，禁止在已有 DHCP 服务的生产网络中随意启用。",
            // V14新增: 关于弹窗技术支持与开源项目链接文案
            "about.tech_support": "技术支持",
            "about.open_source": "开源项目"
        },
        "en-US": {
            "_label": "EN",

            "app.title": "DacatDHCP",
            "app.subtitle": "Lightweight · Stable · Efficient DHCP Service Tool", // V12: subtitle updated
            "nav.settings": "Settings",
            "nav.about": "About",

            "theme.light": "Light",
            "theme.dark": "Dark",
            "theme.title": "Theme",
            "theme.toggle.light": "Switch to dark theme",
            "theme.toggle.dark": "Switch to light theme",

            "security.title": "Security Notice",
            // V12: security.text deprecated, use security.warning/detail/scope instead

            "section.status": "Service Status",
            // V12: status.label deprecated, status label shown directly via status-state element
            "status.running": "Running",
            "status.stopped": "Stopped",
            "status.running.subtitle": "Service running",
            "status.stopped.subtitle": "Service stopped",
            "status.server_ip": "Server IP: ",
            "status.pool": "Address Pool: ",
            "status.usage": "Allocated / Total: ",
            "status.uptime": "Uptime: ",
            "status.days": "d", // V11新增: uptime over 24h shown as "Nd HH:MM:SS", no h/m/s abbreviations
            "status.start_dhcp": "Start DHCP", // V12: start button label (status.start deprecated)
            "status.starting": "Starting...",
            "status.stop": "Stop Service",
            "status.stopping": "Stopping...",

            "section.config": "Adapter & Address Pool",
            "config.adapter": "Service Adapter: ",
            "config.adapter.placeholder": "-- Select Adapter --",
            "config.adapter.detail": "Adapter Detail: ",
            "config.pool_start": "Start IP: ",
            "config.pool_end": "End IP: ",
            "config.lease_time": "Lease Time (minutes): ",
            "config.gateway": "Default Gateway (optional): ",
            "config.gateway.placeholder": "Leave empty to skip",
            "config.dns": "DNS Servers (optional): ",
            "config.dns.placeholder": "Multiple IPv4 separated by commas, up to 3",
            "config.dns.add": "Add",
            "config.dns.clear": "Clear",
            // V12: config.dns.current deprecated, DNS value shown as compact inline tag
            // V12: merged gateway/DNS hint at card bottom to reduce card height
            "config.bottom_hint": "Gateway must be in the same subnet as the adapter and outside the address pool. Gateway and DNS are optional; empty values will not be delivered to clients. DacatDHCP does not enable Windows routing, NAT, or Internet sharing.",

            "section.leases": "Current Leases",
            "lease.ip": "IP Address",
            "lease.mac": "MAC Address",
            "lease.hostname": "Hostname",
            "lease.assigned_at": "Assigned At",
            "lease.expires_at": "Expires At",
            "lease.status": "Status",
            "lease.empty": "No leases",
            // V11新增: lease active/expired/pending status i18n mapping
            "lease.status_active": "Active",
            "lease.status_expired": "Expired",
            "lease.status_pending": "Pending",
            "lease.total": "Total",

            "section.logs": "Runtime Logs",
            "logs.empty": "No logs",
            "logs.autoscroll": "Auto-scroll",
            "logs.export": "Export Logs",
            "logs.clear": "Clear Logs",
            // V12: logs.clear.tip deprecated, clear logs now uses real backend API, use confirm/fail
            "logs.clear.confirm": "Are you sure to clear all logs? This cannot be undone. Both in-memory buffer and log file will be cleared.", // V12
            "logs.clear.fail": "Failed to clear logs: ", // V12

            "quick.title": "Quick Actions",
            "quick.refresh_status": "Refresh Info",
            "quick.refresh_leases": "Refresh Leases",

            // V12: footer.text and footer.version deprecated, version rendered via footer.right_template
            "footer.admin": "Administrator",
            "footer.no_admin": "Non-admin",
            "footer.secure_run": "Running securely",
            "footer.local_time": "Local Time",
            // V12: footer right template "ProductName Vversion|© copyright"
            // V14调整: template changed to "DacatDHCP Vversion · © year ", year is dynamic, copyright (DACAT.CC) appended as link
            "footer.right_template": "DacatDHCP %s · © %s ",
            // V12: security notice segmented text (title/warning/detail/scope)
            "security.warning": "Warning",
            "security.detail": "This tool is intended only for direct, test, or isolated networks. Do not enable it on production networks that already have a DHCP service.",
            "security.scope": "Applicable scenarios: Self-hosted networks / Labs / Device testing / Temporary networks",

            "alert.select_adapter": "Please select a service adapter",
            "alert.pool_start": "Please enter the start IP address",
            "alert.pool_end": "Please enter the end IP address",
            "alert.lease_time": "Lease time must be greater than 0",
            "alert.start_fail": "Start failed: ",
            "alert.stop_fail": "Stop failed: ",
            "alert.config_save_warn": "DHCP started, but config save failed: ",
            // V10新增: DNS 前端校验提示
            "alert.dns_invalid": "Invalid DNS address: ",
            "alert.dns_max": "DNS servers allow at most 3 entries",

            "adapter.virtual": "[Virtual]",
            "adapter.physical": "[Physical]", // V13新增: physical adapter label
            "adapter.mask": "Mask",           // V13新增: adapter detail mask field

            "settings.title": "Settings",
            "settings.language": "Interface Language",
            // V12: removed deprecated settings.theme (theme options removed, only language remains)
            "settings.close": "Close",
            "settings.lang.zh": "中文",
            "settings.lang.en": "English",

            "about.title": "About",
            "about.product": "Product",
            "about.version": "Version",
            "about.file_version": "File Version",
            "about.copyright": "Copyright",
            "about.description": "DacatDHCP is a portable lightweight DHCP service tool for Windows, providing a graphical management interface in a single EXE.",
            "about.support": "Officially supports Windows 10/11, Windows Server 2016+",
            "about.warning": "This tool is only for self-hosted, test, or isolated networks. Do not enable it on production networks that already have a DHCP service.",
            // V14新增: about dialog technical support and open source project link labels
            "about.tech_support": "Technical Support",
            "about.open_source": "Open Source"
        }
    };

    // V10新增: 后端错误码映射表(中文错误 -> 英文翻译)
    // 用于英文界面下将后端返回的中文错误映射为英文,保留 IP/数字等动态参数
    var errMap = [
        {zh: "服务运行中，无法修改配置", en: "Cannot modify config while the service is running"},
        {zh: "配置格式错误", en: "Invalid config format"},
        {zh: "方法不允许", en: "Method not allowed"},
        {zh: "DHCP 服务已在运行", en: "DHCP service is already running"},
        {zh: "DHCP 服务未运行", en: "DHCP service is not running"},
        {zh: "服务正在关闭", en: "Service is shutting down"},
        {zh: "请求格式错误", en: "Invalid request format"},
        {zh: "缺少网卡名称", en: "Missing adapter name"},
        {zh: "网卡 IPv4 地址无效", en: "Adapter IPv4 address is invalid"},
        {zh: "起始地址无效", en: "Start address is invalid"},
        {zh: "结束地址无效", en: "End address is invalid"},
        {zh: "租约时间必须大于 0", en: "Lease time must be greater than 0"},
        {zh: "起始地址与网卡不在同一网段", en: "Start address is not in the same subnet as the adapter"},
        {zh: "结束地址与网卡不在同一网段", en: "End address is not in the same subnet as the adapter"},
        {zh: "起始地址不能大于结束地址", en: "Start address cannot be greater than end address"},
        {zh: "网关地址无效", en: "Gateway address is invalid"},
        {zh: "DNS 地址无效", en: "DNS address is invalid"},
        {zh: "默认网关位于DHCP地址池内，请调整网关或地址池范围", en: "The default gateway is inside the DHCP address pool; please adjust the gateway or pool range"},
        {zh: "与网卡不在同一子网", en: "is not in the same subnet as the adapter"},
        {zh: "不能是网络地址", en: "cannot be the network address"},
        {zh: "不能是广播地址", en: "cannot be the broadcast address"},
        {zh: "DNS 服务器最多允许 3 个", en: "DNS servers allow at most 3 entries"},
        {zh: "子网地址空间不足，无法推荐地址池", en: "Insufficient subnet address space to recommend a pool"},
        {zh: "绑定 UDP 67 端口失败", en: "Failed to bind UDP port 67"},
        {zh: "管理端口", en: "Management port"},
        {zh: "被占用或无法绑定", en: "is occupied or cannot be bound"},
        {zh: "地址池已耗尽", en: "Address pool exhausted"},
        {zh: "请求的 IP 不可用", en: "Requested IP is not available"},
        {zh: "无法获取网卡地址", en: "Cannot get adapter address"},
        {zh: "网卡已断开或禁用", en: "Adapter disconnected or disabled"},
        {zh: "网卡 IP 地址变化", en: "Adapter IP address changed"},
        {zh: "页面加载失败", en: "Failed to load page"},
        {zh: "文件加载失败", en: "Failed to load file"},
        // V13新增: 日志清空相关错误,英文界面禁止显示"截断日志文件失败"等中文原文
        {zh: "truncate_log_failed", en: "Failed to truncate log file"},
        {zh: "reopen_log_failed", en: "Failed to reopen log file"},
        {zh: "seek_log_failed", en: "Failed to seek log file"}
    ];

    // V11新增: 稳定错误码 -> 中英文翻译映射表
    // 后端返回 code 时前端按 code 翻译,避免依赖中文片段匹配
    var errCodeMap = {
        "method_not_allowed":  {zh: "方法不允许",                                       en: "Method not allowed"},
        "service_running":     {zh: "DHCP 服务已在运行",                                 en: "DHCP service is already running"},
        "service_not_running": {zh: "DHCP 服务未运行",                                   en: "DHCP service is not running"},
        "service_closing":     {zh: "服务正在关闭",                                      en: "Service is shutting down"},
        "invalid_config":      {zh: "配置格式错误",                                      en: "Invalid config format"},
        "invalid_request":     {zh: "请求格式错误",                                      en: "Invalid request format"},
        "missing_adapter":     {zh: "缺少网卡名称",                                      en: "Missing adapter name"},
        // V11: 网卡类错误,后端 msg 含网卡名称动态参数,翻译时保留
        "adapter_not_found":   {zh: "未找到网卡",                                        en: "Adapter not found"},
        "adapter_no_ipv4":     {zh: "网卡没有 IPv4 地址",                                en: "Adapter has no IPv4 address"},
        "adapter_down":        {zh: "网卡未连接或已禁用",                                 en: "Adapter disconnected or disabled"},
        // V11: 地址池类错误,后端 msg 含地址/数量动态参数
        "pool_too_large":      {zh: "地址池过大,超出最大支持数量",                       en: "Address pool is too large, exceeds maximum supported size"},
        "pool_special_addr":   {zh: "地址池包含特殊地址(服务端IP/网络地址/广播地址)",     en: "Address pool contains a special address (server IP / network address / broadcast address)"},
        "pool_order_invalid":  {zh: "起始地址不能大于结束地址",                          en: "Start address cannot be greater than end address"},
        "gateway_in_pool":     {zh: "默认网关位于 DHCP 地址池内,请调整网关或地址池范围",  en: "The default gateway is inside the DHCP address pool; please adjust the gateway or pool range"},
        "bind_port_67":        {zh: "绑定 UDP 67 端口失败",                              en: "Failed to bind UDP port 67"},
        // V13新增: internal_error 及网关/DNS 校验错误码,英文界面禁止显示中文原文
        "internal_error":      {zh: "内部错误",                                          en: "Internal error"},
        "invalid_gateway":     {zh: "网关地址无效",                                      en: "Invalid gateway address"},
        "invalid_dns":         {zh: "DNS 地址无效",                                      en: "Invalid DNS address"},
        // V14新增: DNS 数量超限独立错误码,前端直接提供完整中英文文案,禁止"当前N个"中文混排
        "dns_too_many":        {zh: "DNS 服务器最多允许 3 个",                           en: "DNS servers allow at most 3 entries"},
        // V14新增: 配置写入文件失败独立错误码,启动后保存配置失败时使用
        "config_save_failed":  {zh: "配置保存失败",                                      en: "Failed to save configuration"},
        // P0安全新增: 写接口 CSRF 令牌缺失或不匹配,提示用户刷新页面重新获取令牌
        "csrf_token_invalid":  {zh: "页面安全凭据已失效，请刷新页面",                    en: "Page security credential has expired, please refresh the page"},
        // P0安全新增: Host 头与监听地址不一致(DNS Rebinding 防护)
        "host_rejected":       {zh: "请求来源被拒绝",                                    en: "Request source rejected"},
        // P0安全新增: Origin/Referer 来源非法
        "origin_rejected":     {zh: "请求来源被拒绝",                                    en: "Request source rejected"},
        // P0安全新增: 写接口 Content-Type 非 application/json
        "unsupported_media_type": {zh: "不支持的媒体类型",                               en: "Unsupported media type"},
        // P0安全新增: 请求体超过 64KB
        "payload_too_large":   {zh: "请求体过大",                                        en: "Request body too large"},
        // 语言新增: 不支持的语言代码,PUT /api/language 校验失败时返回
        "invalid_language":    {zh: "不支持的语言",                                      en: "Unsupported language"},
        // 语言新增: 语言保存到配置文件失败,内存语言已回滚
        "language_save_failed": {zh: "语言保存失败",                                     en: "Failed to save language"}
    };

    // 当前语言,默认 zh-CN
    var currentLang = "zh-CN";

    // 从服务端注入的 meta 或 localStorage 读取已保存语言,首次启动默认中文
    // 语言新增: 优先读取后端注入的 <meta name="dacat-language">,服务端语言为唯一权威来源
    // localStorage 仅作为旧版本兼容回退(后端 meta 不存在时使用),不再作为托盘语言的独立来源
    function loadSavedLang() {
        // 1. 优先读取服务端注入的语言 meta
        try {
            var metas = document.getElementsByTagName("meta");
            if (metas) {
                for (var i = 0; i < metas.length; i++) {
                    if (metas[i].getAttribute("name") === "dacat-language") {
                        var srvLang = metas[i].getAttribute("content") || "";
                        if (srvLang === "zh-CN" || srvLang === "en-US") {
                            return srvLang;
                        }
                    }
                }
            }
        } catch (e) {
            // meta 读取失败时回退到 localStorage
        }
        // 2. 旧版本兼容回退: 读取 localStorage 中保存的语言
        try {
            var saved = window.localStorage.getItem("dacatdhcp_lang");
            if (saved === "zh-CN" || saved === "en-US") {
                return saved;
            }
        } catch (e) {
            // localStorage 不可用时忽略
        }
        // 3. 最终回退: 根据系统语言自动选择(中文系统 zh-CN,其他 en-US)
        try {
            var navLang = (navigator.language || navigator.userLanguage || "zh-CN").toLowerCase();
            if (navLang.indexOf("zh") === 0) {
                return "zh-CN";
            }
            return "en-US";
        } catch (e) {
            return "zh-CN";
        }
    }

    // 获取翻译文案,key 不存在时返回 key 本身
    function t(key) {
        var lang = dict[currentLang];
        if (lang && lang.hasOwnProperty(key)) {
            return lang[key];
        }
        // 回退到中文
        var zh = dict["zh-CN"];
        if (zh && zh.hasOwnProperty(key)) {
            return zh[key];
        }
        return key;
    }

    // 获取当前语言
    function getLang() {
        return currentLang;
    }

    // 设置语言并保存到 localStorage
    function setLang(lang) {
        if (lang !== "zh-CN" && lang !== "en-US") {
            return;
        }
        currentLang = lang;
        try {
            window.localStorage.setItem("dacatdhcp_lang", lang);
        } catch (e) {
            // localStorage 不可用时忽略
        }
    }

    // 获取当前语言的对外标签(用于切换按钮显示)
    function getToggleLabel() {
        // 中文界面显示 EN,英文界面显示 中文
        return currentLang === "zh-CN" ? "EN" : "中文";
    }

    // V10新增: 翻译后端错误信息
    // 中文界面原样返回;英文界面按 errMap 将中文片段替换为英文,保留 IP/数字等动态参数
    function te(err) {
        if (!err) return "";
        var s = String(err);
        if (currentLang === "zh-CN") {
            return s;
        }
        for (var i = 0; i < errMap.length; i++) {
            if (s.indexOf(errMap[i].zh) >= 0) {
                s = s.split(errMap[i].zh).join(errMap[i].en);
            }
        }
        return s;
    }

    // V11新增: 按稳定错误码翻译后端错误
    // 优先使用 code 查 errCodeMap;无 code 或未命中时回退到 te(fallbackMsg) 按中文片段翻译
    // 对于网卡类/地址池类错误,后端 msg 含动态参数(IP/网卡名/数量),仅翻译固定片段,保留动态参数
    function teByCode(code, fallbackMsg) {
        var msg = fallbackMsg || "";
        if (code && errCodeMap.hasOwnProperty(code)) {
            var entry = errCodeMap[code];
            if (currentLang === "zh-CN") {
                // 中文界面: 后端 msg 已是中文,直接返回(保留动态参数)
                return msg || entry.zh;
            }
            // 英文界面: 用 errMap 将 msg 中的中文片段替换为英文,保留动态参数
            // 若 errMap 未覆盖,回退到 code 对应的英文固定文案
            var translated = te(msg);
            if (translated === msg && msg) {
                // errMap 未命中,使用 code 对应英文
                return entry.en;
            }
            return translated;
        }
        // 无 code,回退到按中文片段翻译
        return te(msg);
    }

    // 应用所有 data-i18n 标记的元素文本
    // 遍历 [data-i18n] 设置 textContent,[data-i18n-placeholder] 设置 placeholder
    function applyTranslations(root) {
        var scope = root || document;
        var nodes = scope.querySelectorAll("[data-i18n]");
        for (var i = 0; i < nodes.length; i++) {
            nodes[i].textContent = t(nodes[i].getAttribute("data-i18n"));
        }
        var phNodes = scope.querySelectorAll("[data-i18n-placeholder]");
        for (var j = 0; j < phNodes.length; j++) {
            phNodes[j].setAttribute("placeholder", t(phNodes[j].getAttribute("data-i18n-placeholder")));
        }
    }

    // 暴露到全局
    window.I18N = {
        t: t,
        te: te, // V10新增: 错误码翻译
        teByCode: teByCode, // V11新增: 按稳定错误码翻译,优先 code 回退中文片段
        getLang: getLang,
        setLang: setLang,
        getToggleLabel: getToggleLabel,
        applyTranslations: applyTranslations,
        loadSavedLang: loadSavedLang,
        has: function (key) {
            var lang = dict[currentLang];
            return !!(lang && lang.hasOwnProperty(key));
        }
    };

    // 初始化语言(模块加载时立即读取保存值)
    currentLang = loadSavedLang();
})(window);
