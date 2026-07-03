/* DacatDHCP 主题管理 (light / dark) */
/* V10重构: 仅保留 light/dark 两种主题,首次默认 light,删除"跟随系统"选项 */
/* 主题选择写入 localStorage,刷新或重启后保持;太阳/月亮图标状态与实际主题一致 */
/* IE11 兼容: 仅使用 var/function,禁止 const/let/箭头函数/模板字符串 */

(function (window) {
    "use strict";

    var STORAGE_KEY = "dacatdhcp_theme";
    var currentTheme = "light"; // V10: 首次默认 light(不再支持 system)

    // 从 localStorage 读取已保存主题,仅接受 light/dark,其余回退 light
    function loadSavedTheme() {
        try {
            var saved = window.localStorage.getItem(STORAGE_KEY);
            if (saved === "light" || saved === "dark") {
                return saved;
            }
        } catch (e) {
            // localStorage 不可用时忽略
        }
        return "light"; // V10: 首次默认浅色
    }

    // 应用主题到 <html> 元素
    function applyTheme() {
        var html = document.documentElement;
        html.setAttribute("data-theme", currentTheme);
    }

    // 获取当前主题设置(light/dark)
    function getTheme() {
        return currentTheme;
    }

    // 设置主题并保存到 localStorage
    function setTheme(theme) {
        // V10: 仅允许 light/dark,拒绝 system
        if (theme !== "light" && theme !== "dark") {
            return;
        }
        currentTheme = theme;
        try {
            window.localStorage.setItem(STORAGE_KEY, theme);
        } catch (e) {
            // localStorage 不可用时忽略
        }
        applyTheme();
    }

    // 切换主题(light <-> dark),供顶部主题图标按钮调用
    function toggleTheme() {
        setTheme(currentTheme === "light" ? "dark" : "light");
    }

    // resolvedTheme 保留以兼容旧调用,现在直接返回当前主题
    function resolvedTheme() {
        return currentTheme;
    }

    // startSystemListener 保留为空操作(V10: 不再支持跟随系统),避免 app.js 调用报错
    function startSystemListener() {
        // no-op: 仅 light/dark,无需监听系统主题变化
    }

    // 初始化主题(模块加载时立即读取并应用,避免页面闪烁)
    currentTheme = loadSavedTheme();
    applyTheme();

    // 暴露到全局
    window.THEME = {
        getTheme: getTheme,
        setTheme: setTheme,
        toggleTheme: toggleTheme,
        applyTheme: applyTheme,
        startSystemListener: startSystemListener,
        resolvedTheme: resolvedTheme
    };
})(window);
