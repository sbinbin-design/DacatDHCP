package web

import "embed"

// V1修复: 嵌入 dhcp.ico 用于 Web favicon
//
//go:embed index.html style.css app.js dhcp.ico
var Assets embed.FS
