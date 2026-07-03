// Package version 提供应用程序版本信息的只读访问
// 唯一版本源为 versioninfo.json，由 go:embed 编译进二进制
// 该文件同时供 generate_resource.bat (goversioninfo) 生成 Windows PE 版本资源
package version

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed versioninfo.json
var versionInfoData []byte

// versionFixed 对应 FixedFileInfo 中的版本号
type versionFixed struct {
	Major int `json:"Major"`
	Minor int `json:"Minor"`
	Patch int `json:"Patch"`
	Build int `json:"Build"`
}

// versionInfoJSON 对应 goversioninfo 的 JSON 配置格式
type versionInfoJSON struct {
	FixedFileInfo struct {
		FileVersion    versionFixed `json:"FileVersion"`
		ProductVersion versionFixed `json:"ProductVersion"`
	} `json:"FixedFileInfo"`
	StringFileInfo struct {
		CompanyName      string `json:"CompanyName"`
		FileDescription  string `json:"FileDescription"`
		FileVersion      string `json:"FileVersion"`
		InternalName     string `json:"InternalName"`
		LegalCopyright   string `json:"LegalCopyright"`
		OriginalFilename string `json:"OriginalFilename"`
		ProductName      string `json:"ProductName"`
		ProductVersion   string `json:"ProductVersion"`
	} `json:"StringFileInfo"`
}

// parsed 在 init 中解析 versioninfo.json
var parsed versionInfoJSON

func init() {
	if err := json.Unmarshal(versionInfoData, &parsed); err != nil {
		panic(fmt.Sprintf("version: 解析 versioninfo.json 失败: %v", err))
	}
}

// Version 返回显示版本号（StringFileInfo.ProductVersion，如 "1.0.0"）
func Version() string {
	return parsed.StringFileInfo.ProductVersion
}

// FileVersion 返回 Windows 文件版本号（StringFileInfo.FileVersion，如 "1.0.0.0"）
func FileVersion() string {
	return parsed.StringFileInfo.FileVersion
}

// ProductName 返回产品名称
func ProductName() string {
	return parsed.StringFileInfo.ProductName
}

// Copyright 返回版权信息（LegalCopyright）
func Copyright() string {
	return parsed.StringFileInfo.LegalCopyright
}

// FileDescription 返回文件说明
func FileDescription() string {
	return parsed.StringFileInfo.FileDescription
}

// InternalName 返回内部名称
func InternalName() string {
	return parsed.StringFileInfo.InternalName
}

// OriginalFilename 返回原始文件名
func OriginalFilename() string {
	return parsed.StringFileInfo.OriginalFilename
}

// CompanyName 返回公司名称
func CompanyName() string {
	return parsed.StringFileInfo.CompanyName
}
