#!/usr/bin/env node
'use strict';
// DacatDHCP 前端 poolReqSeq 回归测试 (Node)
// 仅用于开发环境,不引入运行时依赖,不影响单 EXE 发布
// 直接加载真实 web/app.js,通过最小 DOM mock 触发事件验证核心判断逻辑
// 覆盖场景: A/B 快速切换、手动编辑、切换空网卡、语言切换时旧响应返回

const fs = require('fs');
const path = require('path');
const assert = require('assert');

// ==== 最小 DOM mock (仅支持 app.js 用到的 API) ====

const elements = {};
function makeElement(id) {
    return {
        id: id,
        value: "",
        innerHTML: "",
        textContent: "",
        className: "",
        style: {},
        disabled: false,
        options: [],
        checked: false,
        _attrs: {},
        _listeners: {},
        setAttribute: function (name, val) { this._attrs[name] = val; },
        getAttribute: function (name) { return this._attrs[name] !== undefined ? this._attrs[name] : null; },
        appendChild: function (child) { return child; },
        querySelector: function () { return makeElement("query"); },
        querySelectorAll: function () { return []; },
        addEventListener: function (type, handler) {
            (this._listeners[type] = this._listeners[type] || []).push(handler);
        },
        remove: function () {
            if (this.options.length > 1) this.options.length = this.options.length - 1;
        },
        add: function (opt) { this.options.push(opt); }
    };
}

function getElement(id) {
    if (!elements[id]) elements[id] = makeElement(id);
    return elements[id];
}

const documentMock = {
    getElementById: getElement,
    createElement: function (tag) { return makeElement(tag); },
    createTextNode: function (text) { return { textContent: text, innerHTML: text }; },
    getElementsByTagName: function () { return []; },
    getElementsByName: function () { return []; },
    documentElement: makeElement("html"),
    readyState: "complete",
    addEventListener: function () {}
};

// 模拟 XMLHttpRequest (可控回调时序,send 不自动回调)
const xhrQueue = [];
class MockXHR {
    constructor() {
        this.readyState = 0;
        this.status = 0;
        this.responseText = "";
        this._onrsc = null;
        this._method = "";
        this._url = "";
        xhrQueue.push(this);
    }
    open(method, url) { this._method = method; this._url = url; }
    setRequestHeader() {}
    send() {}
    set onreadystatechange(fn) { this._onrsc = fn; }
    get onreadystatechange() { return this._onrsc; }
    _respond(status, body) {
        this.status = status;
        this.responseText = body;
        this.readyState = 4;
        if (this._onrsc) this._onrsc();
    }
}

// 注入全局环境
global.document = documentMock;
global.window = {
    I18N: null,
    THEME: null,
    navigator: {},
    setInterval: function () { return 0; },
    clearInterval: function () {},
    setTimeout: function () { return 0; },
    URL: { createObjectURL: function () {}, revokeObjectURL: function () {} },
    Blob: function () {}
};
global.XMLHttpRequest = MockXHR;
// Node.js v24+ 中 global.navigator 为只读 getter,通过 window.navigator 间接提供
global.window.navigator = {};
global.setInterval = global.window.setInterval;
global.clearInterval = global.window.clearInterval;
global.setTimeout = global.window.setTimeout;
global.alert = function () {};
global.confirm = function () { return true; };

// 加载并执行真实 web/app.js
const appJsPath = path.join(__dirname, '..', 'web', 'app.js');
const appJsCode = fs.readFileSync(appJsPath, 'utf8');
// V18: 加载 app.js 前设置测试标记,允许其暴露内部函数供测试调用
// 正式运行环境 window.__DACAT_TEST__ 不为 true,不暴露任何测试入口
global.window.__DACAT_TEST__ = true;
// eslint-disable-next-line no-eval
eval(appJsCode);

// 清空初始化阶段产生的 XHR 请求 (/api/config, /api/version, /api/status, /api/leases, /api/logs)
xhrQueue.length = 0;

// ==== 测试辅助函数 ====

// 触发元素的 change 事件 (app.js 已统一使用 addEventListener,仅监听 change)
function triggerChange(id) {
    const el = getElement(id);
    const handlers = el._listeners["change"] || [];
    handlers.forEach(function (fn) { fn({}); });
}

// 触发元素的 input 事件 (app.js 已统一使用 addEventListener,仅监听 input)
function triggerInput(id) {
    const el = getElement(id);
    const handlers = el._listeners["input"] || [];
    handlers.forEach(function (fn) { fn({}); });
}

// 模拟用户切换网卡: 设置 value 并触发 change 事件
function setAdapter(val) {
    const sel = getElement("adapter-select");
    sel.value = val;
    triggerChange("adapter-select");
}

// 模拟用户手动编辑地址池: 设置 value 并触发 input 事件
function setPoolInput(field, val) {
    const el = getElement(field);
    el.value = val;
    triggerInput(field);
}

// 响应指定网卡的 pool-recommend 请求 (按 URL 匹配)
function respondPoolRecommendFor(adapterName, start, end) {
    const idx = xhrQueue.findIndex(function (x) {
        return x._url && x._url.indexOf("/api/pool-recommend") >= 0 && x._url.indexOf("adapter_name=" + adapterName) >= 0;
    });
    if (idx < 0) throw new Error("无 " + adapterName + " 的 pending pool-recommend 请求");
    const xhr = xhrQueue.splice(idx, 1)[0];
    xhr._respond(200, JSON.stringify({ pool_start: start, pool_end: end }));
}

// 响应最早的 pool-recommend 请求
function respondFirstPoolRecommend(start, end) {
    const idx = xhrQueue.findIndex(function (x) { return x._url && x._url.indexOf("/api/pool-recommend") >= 0; });
    if (idx < 0) throw new Error("无 pending pool-recommend 请求");
    const xhr = xhrQueue.splice(idx, 1)[0];
    xhr._respond(200, JSON.stringify({ pool_start: start, pool_end: end }));
}

// 语言新增: 响应最早的 PUT /api/language 请求,使其触发 applyLanguageChange 回调
// switchLang/onSettingsLangChange 先调用 PUT /api/language,成功后才执行 applyLanguageChange
function respondLanguagePut(lang) {
    const idx = xhrQueue.findIndex(function (x) {
        return x._method === "PUT" && x._url && x._url.indexOf("/api/language") >= 0;
    });
    if (idx < 0) throw new Error("无 pending PUT /api/language 请求");
    const xhr = xhrQueue.splice(idx, 1)[0];
    xhr._respond(200, JSON.stringify({ language: lang }));
}

function getPoolValues() {
    return {
        start: getElement("pool-start").value,
        end: getElement("pool-end").value
    };
}

function clearAllPending() {
    xhrQueue.length = 0;
}

function resetState() {
    getElement("adapter-select").value = "";
    getElement("pool-start").value = "";
    getElement("pool-end").value = "";
    clearAllPending();
}

// ==== 测试用例 ====

let passCount = 0;
let failCount = 0;

function test(name, fn) {
    try {
        fn();
        console.log("  PASS: " + name);
        passCount++;
    } catch (e) {
        console.log("  FAIL: " + name + " - " + e.message);
        failCount++;
    }
}

console.log("=== DacatDHCP 前端 poolReqSeq 回归测试 (Node) ===");
console.log("加载真实 web/app.js 并通过 DOM 事件触发核心判断逻辑\n");

// ---- 场景 1: A/B 快速切换 ----
console.log("[场景 1] A/B 快速切换: 旧响应不得覆盖新网卡");

test("切换 A 后立即切换 B, A 的旧响应应被丢弃", function () {
    resetState();
    setAdapter("AdapterA");
    setAdapter("AdapterB");
    // A 的延迟响应返回 (应被丢弃,序号不匹配)
    respondPoolRecommendFor("AdapterA", "192.168.1.2", "192.168.1.100");
    const vals = getPoolValues();
    assert.strictEqual(vals.start, "", "A 的旧响应不应覆盖, start 应为空, 实际: " + vals.start);
    assert.strictEqual(vals.end, "", "A 的旧响应不应覆盖, end 应为空, 实际: " + vals.end);
});

test("切换 A 后立即切换 B, B 的新响应应被应用", function () {
    resetState();
    setAdapter("AdapterA");
    setAdapter("AdapterB");
    respondPoolRecommendFor("AdapterB", "10.0.0.2", "10.0.0.100");
    const vals = getPoolValues();
    assert.strictEqual(vals.start, "10.0.0.2", "B 的新响应应被应用, start 应为 10.0.0.2");
    assert.strictEqual(vals.end, "10.0.0.100", "B 的新响应应被应用, end 应为 10.0.0.100");
});

// ---- 场景 2: 手动编辑后旧响应返回 ----
console.log("\n[场景 2] 手动编辑: 旧响应不得覆盖用户输入");

test("用户手动编辑 pool-start/pool-end 后, 旧响应应被丢弃", function () {
    resetState();
    setAdapter("AdapterA");
    // 用户手动编辑地址池
    setPoolInput("pool-start", "192.168.50.10");
    setPoolInput("pool-end", "192.168.50.20");
    // A 的延迟响应返回 (应被丢弃,用户已编辑且序号已递增)
    respondPoolRecommendFor("AdapterA", "192.168.1.2", "192.168.1.100");
    const vals = getPoolValues();
    assert.strictEqual(vals.start, "192.168.50.10", "用户输入应保留, 不被旧响应覆盖");
    assert.strictEqual(vals.end, "192.168.50.20", "用户输入应保留, 不被旧响应覆盖");
});

// ---- 场景 3: 切换空网卡后旧响应返回 ----
console.log("\n[场景 3] 切换空网卡: 旧响应不得执行, 地址池必须清空");

test("切换 A 后选择空白网卡, A 的旧响应应被丢弃且地址池为空", function () {
    resetState();
    setAdapter("AdapterA");
    // 切换到空白网卡 (onAdapterChange -> requestPoolRecommend 递增序号并清空,网卡为空直接 return)
    setAdapter("");
    // A 的延迟响应返回 (应被丢弃,序号不匹配且网卡名不匹配)
    respondPoolRecommendFor("AdapterA", "192.168.1.2", "192.168.1.100");
    const vals = getPoolValues();
    assert.strictEqual(vals.start, "", "空白网卡后地址池应为空, 旧响应不得覆盖");
    assert.strictEqual(vals.end, "", "空白网卡后地址池应为空, 旧响应不得覆盖");
});

test("切换 A 后选择空白网卡, 不应发出新的 pool-recommend 请求", function () {
    resetState();
    setAdapter("AdapterA");
    clearAllPending(); // 清除 A 的请求
    setAdapter("");
    // 队列中不应有 pool-recommend 请求
    const hasPoolReq = xhrQueue.some(function (x) {
        return x._url && x._url.indexOf("/api/pool-recommend") >= 0;
    });
    assert.strictEqual(hasPoolReq, false, "空白网卡不应发出 pool-recommend 请求");
});

// ---- 场景 4: 语言切换时旧响应返回 ----
console.log("\n[场景 4] 语言切换: 旧响应不得覆盖表单");

test("语言切换后, 旧 pool-recommend 响应应被丢弃", function () {
    resetState();
    // 设置 I18N mock 以便 switchLang 能执行 applyLanguageChange
    const i18nState = { lang: "zh-CN" };
    const i18nMock = {
        getLang: function () { return i18nState.lang; },
        setLang: function (lang) { i18nState.lang = lang; },
        applyTranslations: function () {},
        t: function (key) { return key; },
        te: function (code) { return code; },
        teByCode: function (code, msg) { return msg; }
    };
    global.window.I18N = i18nMock;
    global.I18N = i18nMock; // app.js 内部直接引用 I18N,需设为全局变量
    const themeMock = { getTheme: function () { return "light"; } };
    global.window.THEME = themeMock;
    global.THEME = themeMock; // app.js 内部直接引用 THEME,需设为全局变量

    setAdapter("AdapterA");
    // 调用语言切换 (先 PUT /api/language,成功后 applyLanguageChange 递增 poolReqSeq)
    global.window.switchLang("en-US");
    // 语言新增: 响应 PUT /api/language 请求,触发 applyLanguageChange 回调
    respondLanguagePut("en-US");
    // A 的延迟响应返回 (应被丢弃,语言切换使 poolReqSeq 递增)
    respondPoolRecommendFor("AdapterA", "192.168.1.2", "192.168.1.100");
    const vals = getPoolValues();
    assert.strictEqual(vals.start, "", "语言切换后旧响应应被丢弃, start 应为空");
    assert.strictEqual(vals.end, "", "语言切换后旧响应应被丢弃, end 应为空");

    // 清理
    global.window.I18N = null;
    global.I18N = undefined;
    global.window.THEME = null;
    global.THEME = undefined;
});

// ---- 场景 5: 正常路径对照 ----
console.log("\n[场景 5] 正常路径对照: 无并发编辑时响应应被应用");

test("切换 A 后正常响应应被应用", function () {
    resetState();
    setAdapter("AdapterA");
    respondPoolRecommendFor("AdapterA", "192.168.1.2", "192.168.1.100");
    const vals = getPoolValues();
    assert.strictEqual(vals.start, "192.168.1.2", "正常响应应被应用");
    assert.strictEqual(vals.end, "192.168.1.100", "正常响应应被应用");
});

// ---- 场景 6: /api/adapters 可控响应 + 语言切换 ----
console.log("\n[场景 6] /api/adapters 可控响应: 语言切换中旧请求不得覆盖用户选择");

// 辅助函数: 响应最早的 /api/adapters 请求
function respondFirstAdapters(adapterList) {
    const idx = xhrQueue.findIndex(function (x) { return x._url && x._url.indexOf("/api/adapters") >= 0; });
    if (idx < 0) throw new Error("无 pending /api/adapters 请求");
    const xhr = xhrQueue.splice(idx, 1)[0];
    xhr._respond(200, JSON.stringify({ adapters: adapterList }));
}

// 辅助函数: 响应最后一个 /api/adapters 请求(较新请求先返回)
function respondLastAdapters(adapterList) {
    let lastIdx = -1;
    for (let i = xhrQueue.length - 1; i >= 0; i--) {
        if (xhrQueue[i]._url && xhrQueue[i]._url.indexOf("/api/adapters") >= 0) {
            lastIdx = i;
            break;
        }
    }
    if (lastIdx < 0) throw new Error("无 pending /api/adapters 请求");
    const xhr = xhrQueue.splice(lastIdx, 1)[0];
    xhr._respond(200, JSON.stringify({ adapters: adapterList }));
}

// 辅助函数: 设置 I18N/THEME mock
function setupI18NMock() {
    const i18nState = { lang: "zh-CN" };
    const i18nMock = {
        getLang: function () { return i18nState.lang; },
        setLang: function (lang) { i18nState.lang = lang; },
        applyTranslations: function () {},
        t: function (key) { return key; },
        te: function (code) { return code; },
        teByCode: function (code, msg) { return msg; }
    };
    global.window.I18N = i18nMock;
    global.I18N = i18nMock;
    const themeMock = { getTheme: function () { return "light"; } };
    global.window.THEME = themeMock;
    global.THEME = themeMock;
}

function teardownI18NMock() {
    global.window.I18N = null;
    global.I18N = undefined;
    global.window.THEME = null;
    global.THEME = undefined;
}

// 辅助函数: 检查是否有 pending /api/adapters 请求
function hasPendingAdapters() {
    return xhrQueue.some(function (x) { return x._url && x._url.indexOf("/api/adapters") >= 0; });
}

test("初始请求未返回时切换语言, 旧请求返回后不得覆盖用户选择 B", function () {
    resetState();
    setupI18NMock();

    // 步骤 1: 触发初始 /api/adapters 请求(loadAdapters(true))
    // loadAdapters 入口 adapterReqSeq++ => mySeq=1
    global.window._testLoadAdapters(true);
    assert.ok(hasPendingAdapters(), "应有一个 pending /api/adapters 请求");

    // 步骤 2: 切换语言(applyLanguageChange 递增 adapterReqSeq, 再调用 loadAdapters(false) 内部递增)
    // applyLanguageChange 先 adapterReqSeq++ => 2, 再调用 loadAdapters(false)
    // loadAdapters(false) 因 cachedAdapters 为 null 走 API 路径, adapterReqSeq++ => mySeq=3
    global.window._testApplyLanguageChange("en-US");
    assert.ok(hasPendingAdapters(), "语言切换后应有新的 pending /api/adapters 请求");

    // 步骤 3: 旧请求(mySeq=1)返回 - 应直接 return, 不更新 cachedAdapters, 不调用 renderAdapters
    // 此时 adapterReqSeq=3, mySeq=1 != adapterReqSeq, 旧请求被丢弃
    respondFirstAdapters([
        { name: "AdapterA", ip: "192.168.1.1", mask: "255.255.255.0", mac: "AA:BB:CC:DD:EE:01", type: "physical", hasIPv4: true }
    ]);
    // 旧请求返回后不应影响 adapter-select 的值(仍为空,因为 renderAdapters 未被调用)
    assert.strictEqual(getElement("adapter-select").value, "", "旧请求返回后 adapter-select 应保持空, 不应被 renderAdapters 覆盖");

    // 步骤 4: 较新请求(mySeq=3)返回 - 应更新 cachedAdapters 并调用 renderAdapters, 下拉框出现 B
    respondFirstAdapters([
        { name: "AdapterA", ip: "192.168.1.1", mask: "255.255.255.0", mac: "AA:BB:CC:DD:EE:01", type: "physical", hasIPv4: true },
        { name: "AdapterB", ip: "10.0.0.1", mask: "255.255.255.0", mac: "AA:BB:CC:DD:EE:02", type: "physical", hasIPv4: true }
    ]);
    // V18修复: 验证 B 选项真实存在于下拉框,后续 setAdapter 依赖此 option
    const sel = getElement("adapter-select");
    const hasBOption = sel.options.some(function (opt) { return opt.value === "AdapterB"; });
    assert.ok(hasBOption, "新请求返回后下拉框应包含 AdapterB 选项");

    // 步骤 5: 用户选择 B (onAdapterChange: 写 data-selected=B, 递增 adapterReqSeq)
    setAdapter("AdapterB");
    assert.strictEqual(sel.value, "AdapterB", "用户应已选择 B");
    assert.strictEqual(sel.getAttribute("data-selected"), "AdapterB", "data-selected 应为 AdapterB");

    // 步骤 6: 最终验证 - 必须仍选中 B, 不得清空或恢复旧网卡
    assert.strictEqual(sel.value, "AdapterB", "最终必须仍选中 B, 不得被清空或恢复旧网卡");
    assert.strictEqual(sel.getAttribute("data-selected"), "AdapterB", "data-selected 必须仍为 AdapterB");

    teardownI18NMock();
});

// ---- 场景 7: 网卡请求乱序 - 较新请求先返回新列表, 较旧请求后返回旧列表 ----
console.log("\n[场景 7] 网卡请求乱序: 较新先返回新列表, 较旧后返回旧列表, 旧列表不得进入缓存或重新渲染");

test("较新请求先返回新列表, 较旧请求后返回旧列表, 随后切换语言必须仍显示新列表", function () {
    resetState();
    setupI18NMock();

    // 步骤 1: 触发第一次 /api/adapters 请求(loadAdapters(true))
    // loadAdapters 入口 adapterReqSeq++ => mySeq=1
    global.window._testLoadAdapters(true);
    assert.ok(hasPendingAdapters(), "应有一个 pending /api/adapters 请求 (seq=1)");

    // 步骤 2: 触发第二次 /api/adapters 请求(loadAdapters(true))
    // loadAdapters 入口 adapterReqSeq++ => mySeq=2
    global.window._testLoadAdapters(true);
    assert.ok(hasPendingAdapters(), "应有两个 pending /api/adapters 请求 (seq=1, seq=2)");

    // 步骤 3: 较新请求(seq=2)先返回新列表(含 AdapterNew)
    // 此时 adapterReqSeq=2, mySeq=2 == adapterReqSeq, 应更新缓存并 renderAdapters
    respondLastAdapters([
        { name: "AdapterNew", ip: "192.168.99.1", mask: "255.255.255.0", mac: "AA:BB:CC:DD:EE:99", type: "physical", hasIPv4: true }
    ]);
    // 验证新列表已渲染
    const sel1 = getElement("adapter-select");
    const hasNewOption = sel1.options.some(function (opt) { return opt.value === "AdapterNew"; });
    assert.ok(hasNewOption, "较新请求返回后下拉框应包含 AdapterNew 选项");

    // 步骤 4: 较旧请求(seq=1)后返回旧列表(仅含 AdapterOld)
    // 此时 adapterReqSeq=2, mySeq=1 != adapterReqSeq, 应直接 return
    // V18修复: 旧列表不得更新 cachedAdapters, 不得调用 renderAdapters
    respondFirstAdapters([
        { name: "AdapterOld", ip: "192.168.1.1", mask: "255.255.255.0", mac: "AA:BB:CC:DD:EE:01", type: "physical", hasIPv4: true }
    ]);
    // 验证旧列表未覆盖新列表, AdapterOld 不应出现, AdapterNew 应保留
    const sel2 = getElement("adapter-select");
    const hasOldOption = sel2.options.some(function (opt) { return opt.value === "AdapterOld"; });
    assert.strictEqual(hasOldOption, false, "较旧请求的旧列表不得重新渲染, AdapterOld 不应出现");
    const stillHasNewOption = sel2.options.some(function (opt) { return opt.value === "AdapterNew"; });
    assert.ok(stillHasNewOption, "较新请求的新列表应保留, AdapterNew 不应被清除");

    // 步骤 5: 切换语言(applyLanguageChange 使用 cachedAdapters 重新渲染)
    // 此时 cachedAdapters 应仍为较新请求的新列表(含 AdapterNew), 不应被旧列表污染
    global.window._testApplyLanguageChange("en-US");

    // 步骤 6: 最终验证 - 必须继续显示新列表, 旧列表不得进入缓存或重新渲染
    const sel3 = getElement("adapter-select");
    const finalHasNew = sel3.options.some(function (opt) { return opt.value === "AdapterNew"; });
    assert.ok(finalHasNew, "切换语言后必须继续显示新列表, AdapterNew 应保留");
    const finalHasOld = sel3.options.some(function (opt) { return opt.value === "AdapterOld"; });
    assert.strictEqual(finalHasOld, false, "切换语言后旧列表不得重新渲染, AdapterOld 不应出现");

    teardownI18NMock();
});

// ---- 场景 8: 语言切换连续点击防乱序 ----
console.log("\n[场景 8] 语言切换连续点击: 旧响应不得覆盖用户最后一次选择");

// 语言新增: 响应最早的 PUT /api/language 请求,指定状态码和响应体
function respondLanguagePutWithStatus(status, body) {
    const idx = xhrQueue.findIndex(function (x) {
        return x._method === "PUT" && x._url && x._url.indexOf("/api/language") >= 0;
    });
    if (idx < 0) throw new Error("无 pending PUT /api/language 请求");
    const xhr = xhrQueue.splice(idx, 1)[0];
    xhr._respond(status, body);
}

// 语言新增: 响应最早的 GET /api/language 请求
function respondLanguageGet(lang) {
    const idx = xhrQueue.findIndex(function (x) {
        return x._method === "GET" && x._url && x._url.indexOf("/api/language") >= 0;
    });
    if (idx < 0) throw new Error("无 pending GET /api/language 请求");
    const xhr = xhrQueue.splice(idx, 1)[0];
    xhr._respond(200, JSON.stringify({ language: lang }));
}

test("连续切换语言, 旧响应不得覆盖用户最后一次选择", function () {
    resetState();
    setupI18NMock();

    // 初始语言为 zh-CN
    assert.strictEqual(global.I18N.getLang(), "zh-CN", "初始语言应为 zh-CN");

    // 步骤 1: 用户点击切换到 en-US (langReqSeq=1, 禁用控件, 发出 PUT A)
    global.window.switchLang("en-US");
    // 验证控件已禁用
    assert.strictEqual(getElement("lang-zh").disabled, true, "请求期间 lang-zh 应禁用");
    assert.strictEqual(getElement("lang-en").disabled, true, "请求期间 lang-en 应禁用");

    // 步骤 2: 用户再次点击切换到 en-US (绕过禁用直接调用,模拟快速点击)
    // I18N.getLang() 仍为 zh-CN,因此不会因相同语言返回,继续发出 PUT B
    global.window.switchLang("en-US");
    // langReqSeq 应为 2

    // 步骤 3: 旧请求 A (seq=1) 先返回成功 - 应被丢弃,不应用语言
    respondLanguagePutWithStatus(200, JSON.stringify({ language: "en-US" }));
    // 旧响应被丢弃,语言应仍为 zh-CN
    assert.strictEqual(global.I18N.getLang(), "zh-CN", "旧响应应被丢弃, 语言仍为 zh-CN");

    // 步骤 4: 新请求 B (seq=2) 返回成功 - 应应用 en-US
    respondLanguagePutWithStatus(200, JSON.stringify({ language: "en-US" }));
    // 新响应被应用,语言应为 en-US
    assert.strictEqual(global.I18N.getLang(), "en-US", "新响应应被应用, 语言为 en-US");
    // 控件应恢复可用
    assert.strictEqual(getElement("lang-zh").disabled, false, "请求完成后 lang-zh 应恢复可用");
    assert.strictEqual(getElement("lang-en").disabled, false, "请求完成后 lang-en 应恢复可用");

    teardownI18NMock();
});

test("语言切换失败后恢复请求与新切换请求防乱序", function () {
    resetState();
    setupI18NMock();

    // 初始语言为 zh-CN
    assert.strictEqual(global.I18N.getLang(), "zh-CN", "初始语言应为 zh-CN");

    // 步骤 1: 用户点击切换到 en-US (langReqSeq=1, 禁用控件, 发出 PUT A)
    global.window.switchLang("en-US");

    // 步骤 2: PUT A 失败 - 触发 restoreServerLanguage(1), 发出 GET 请求
    respondLanguagePutWithStatus(500, JSON.stringify({ error: "save failed", code: "language_save_failed" }));
    // 失败后语言应仍为 zh-CN (restoreServerLanguage 尚未完成)
    assert.strictEqual(global.I18N.getLang(), "zh-CN", "保存失败后语言应仍为 zh-CN");

    // 步骤 3: 绕过禁用直接调用 switchLang("en-US") (langReqSeq=2, 发出 PUT B)
    global.window.switchLang("en-US");

    // 步骤 4: 旧的 restoreServerLanguage GET 请求 (seq=1) 返回 - 应被丢弃
    // 因为 restoreSeq(1) !== langReqSeq(2), 不得恢复控件或改变语言
    respondLanguageGet("zh-CN");
    // 语言应仍为 zh-CN (旧 GET 被丢弃,新 PUT 尚未返回)
    assert.strictEqual(global.I18N.getLang(), "zh-CN", "旧 GET 响应应被丢弃");
    // 控件应仍为禁用 (旧 GET 不得恢复控件)
    assert.strictEqual(getElement("lang-zh").disabled, true, "旧 GET 不得恢复控件, lang-zh 应仍禁用");

    // 步骤 5: 新请求 B (seq=2) 返回成功 - 应应用 en-US 并恢复控件
    respondLanguagePutWithStatus(200, JSON.stringify({ language: "en-US" }));
    assert.strictEqual(global.I18N.getLang(), "en-US", "新响应应被应用, 语言为 en-US");
    assert.strictEqual(getElement("lang-zh").disabled, false, "新请求完成后控件应恢复");

    teardownI18NMock();
});

// ---- 汇总 ----
console.log("\n=== 测试结果 ===");
console.log("PASS: " + passCount + ", FAIL: " + failCount);
if (failCount > 0) {
    process.exit(1);
}
