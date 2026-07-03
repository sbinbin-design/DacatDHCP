package version

import (
	"strings"
	"testing"
)

// TestVersion_NonEmpty 验证核心字段非空
func TestVersion_NonEmpty(t *testing.T) {
	if Version() == "" {
		t.Error("Version() 返回空字符串")
	}
	if FileVersion() == "" {
		t.Error("FileVersion() 返回空字符串")
	}
	if ProductName() == "" {
		t.Error("ProductName() 返回空字符串")
	}
	if Copyright() == "" {
		t.Error("Copyright() 返回空字符串")
	}
}

// TestVersion_Format 验证版本号格式（不硬编码具体版本值）
// Version 应为 X.Y.Z 三段格式；FileVersion 应为 X.Y.Z.W 四段格式
func TestVersion_Format(t *testing.T) {
	v := Version()
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		t.Errorf("Version() = %q 应为 X.Y.Z 三段格式，实际 %d 段", v, len(parts))
	}
	for _, p := range parts {
		if p == "" {
			t.Errorf("Version() = %q 包含空段", v)
		}
	}

	fv := FileVersion()
	fparts := strings.Split(fv, ".")
	if len(fparts) != 4 {
		t.Errorf("FileVersion() = %q 应为 X.Y.Z.W 四段格式，实际 %d 段", fv, len(fparts))
	}
	for _, p := range fparts {
		if p == "" {
			t.Errorf("FileVersion() = %q 包含空段", fv)
		}
	}

	// 品牌常量（非版本号，可固定断言）
	if pn := ProductName(); pn != "DacatDHCP" {
		t.Errorf("ProductName() = %q, want %q", pn, "DacatDHCP")
	}
	if c := Copyright(); c != "DACAT.CC" {
		t.Errorf("Copyright() = %q, want %q", c, "DACAT.CC")
	}
}

// TestVersion_Consistency 验证 FileVersion 与 Version 前缀一致
func TestVersion_Consistency(t *testing.T) {
	v := Version()
	fv := FileVersion()
	if len(fv) < len(v) || fv[:len(v)] != v {
		t.Errorf("FileVersion()=%q 前缀与 Version()=%q 不一致", fv, v)
	}
}
