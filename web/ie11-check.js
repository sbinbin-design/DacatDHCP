/* DacatDHCP IE11 检测脚本 */
/* 必须在 theme.js / i18n.js / app.js 之前加载 */
/* 检测到 document.documentMode (IE8~IE11 私有属性) 时替换为不支持浏览器页面,不再加载管理界面 */
/* 仅使用 IE11 可执行的 ES5 语法: var/function,禁止 const/let/箭头函数/模板字符串 */
/* 不支持页面仅使用基础 HTML/CSS,不使用 CSS 变量/Grid/Flex gap/clamp 等现代特性 */

(function () {
    "use strict";

    // document.documentMode 为 IE 专有属性,现代浏览器(Edge/Chrome/Firefox)为 undefined
    if (typeof document.documentMode !== "number") {
        return;
    }

    // 根据浏览器语言选择中文或英文提示
    var lang = "";
    try {
        lang = navigator.language || navigator.userLanguage || "";
    } catch (e) {
        lang = "";
    }
    lang = (lang || "").toLowerCase();
    var isZh = lang.indexOf("zh") === 0;

    var title, message, browsers;
    if (isZh) {
        title = "当前浏览器不受支持";
        message = "请使用 Microsoft Edge 或 Google Chrome 访问 DacatDHCP。";
        browsers = "Internet Explorer 11 已不再受支持。";
    } else {
        title = "Unsupported Browser";
        message = "Please use Microsoft Edge or Google Chrome to access DacatDHCP.";
        browsers = "Internet Explorer 11 is no longer supported.";
    }

    // 使用基础 CSS(无 CSS 变量/Grid/Flex gap/clamp),居中布局用 text-align + margin:auto
    var html = '<!DOCTYPE html><html><head><meta charset="utf-8">'
        + '<meta name="viewport" content="width=device-width, initial-scale=1">'
        + '<title>DacatDHCP</title><style>'
        + 'body{margin:0;padding:40px 20px;background:#f5f6f8;color:#1f2937;'
        + 'font-family:"Microsoft YaHei","Segoe UI",Arial,sans-serif;text-align:center;}'
        + '.box{max-width:560px;margin:64px auto 0;background:#ffffff;'
        + 'border:1px solid #e5e7eb;padding:40px 32px;}'
        + '.icon{font-size:48px;color:#d97706;margin-bottom:16px;font-weight:bold;line-height:1;}'
        + 'h1{font-size:22px;margin:0 0 12px;font-weight:600;}'
        + 'p{font-size:15px;line-height:1.7;margin:0;color:#4b5563;}'
        + '.browsers{margin-top:16px;font-size:15px;color:#111827;}'
        + '</style></head><body>'
        + '<div class="box"><div class="icon">!</div>'
        + '<h1>' + title + '</h1>'
        + '<p>' + message + '</p>'
        + '<p class="browsers">' + browsers + '</p>'
        + '</div></body></html>';

    // document.open/write/close 替换整个文档,丢弃原页面剩余内容(含后续脚本)
    document.open();
    document.write(html);
    document.close();
})();
