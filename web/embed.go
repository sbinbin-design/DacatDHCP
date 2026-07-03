package web

import "embed"

// V1修复: 嵌入 dhcp.ico 用于 Web favicon
// 新增: 嵌入 i18n.js(国际化资源)与 theme.js(主题管理),仍保持单 EXE 无外部依赖
//
//go:embed index.html style.css app.js i18n.js theme.js dhcp.ico
var Assets embed.FS
