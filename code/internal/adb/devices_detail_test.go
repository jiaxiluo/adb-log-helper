package adb

// ============================================================================
// 文件名称 : devices_detail_test.go
// 功    能 : internal/adb 包「设备详情」相关纯逻辑的单元测试（无需真实设备）。
//            覆盖三个函数：
//              1. classifyTransport    依据序列号推断连接方式（USB/TCP/模拟器）
//              2. parseResolveActivity 启动入口组件名解析（am start 前置）
//              3. parseGetpropOutput   型号/安卓版本/品牌组合命令输出的解析
// 运行方式 : 在项目根目录执行 go test ./internal/adb/
// ============================================================================

import (
	"testing"
)

// ----------------------------------------------------------------------------
// classifyTransport 测试组
// ----------------------------------------------------------------------------

// TestClassifyTransport 验证三类序列号分别被识别为 模拟器 / TCP / USB。
func TestClassifyTransport(t *testing.T) {
	cases := []struct {
		serial string // 输入序列号
		want   string // 期望的连接方式
	}{
		{"emulator-5554", "模拟器"},      // 模拟器：emulator- 前缀
		{"emulator-5556", "模拟器"},      // 模拟器：多实例
		{"192.168.1.100:5555", "TCP"}, // TCP：IP:端口
		{"127.0.0.1:5037", "TCP"},     // TCP：本机转发
		{"ABC123XYZ", "USB"},          // USB：厂商序列号（纯字母数字）
		{"0123456789ABCDEF", "USB"},   // USB：十六进制序列号
	}
	for _, c := range cases {
		got := classifyTransport(c.serial)
		if got != c.want {
			t.Fatalf("序列号 %q 连接方式不符: 期望 %q, 实际 %q", c.serial, c.want, got)
		}
	}
}

// ----------------------------------------------------------------------------
// parseResolveActivity 测试组
// ----------------------------------------------------------------------------

// TestParseResolveActivityNormal 验证从多行输出中取到最后一行合法组件名
// （部分系统首行是包安装路径等杂项信息）。
func TestParseResolveActivityNormal(t *testing.T) {
	output := "/data/user/0/com.example.app\r\n" +
		"com.example.app/.MainActivity\r\n"
	got := parseResolveActivity(output, "com.example.app")
	if got != "com.example.app/.MainActivity" {
		t.Fatalf("组件名解析不符: 期望 %q, 实际 %q", "com.example.app/.MainActivity", got)
	}
}

// TestParseResolveActivitySingleLine 验证单行输出（多数新系统的常态）。
func TestParseResolveActivitySingleLine(t *testing.T) {
	got := parseResolveActivity("com.twitter.android/com.twitter.android.StartActivity",
		"com.twitter.android")
	if got != "com.twitter.android/com.twitter.android.StartActivity" {
		t.Fatalf("单行组件名解析不符: %q", got)
	}
}

// TestParseResolveActivityNotFound 验证输出中无该包的组件行时返回空串
// （调用方据此走 monkey 兜底路径）。
func TestParseResolveActivityNotFound(t *testing.T) {
	cases := []struct {
		name   string // 场景说明
		output string // 模拟输出
	}{
		{"空输出", ""},
		{"只有杂项行", "No activity found\r\n"},
		{"组件属于其他包", "com.other.app/.MainActivity\r\n"},
	}
	for _, c := range cases {
		if got := parseResolveActivity(c.output, "com.example.app"); got != "" {
			t.Fatalf("场景 %q 应返回空串, 实际 %q", c.name, got)
		}
	}
}

// ----------------------------------------------------------------------------
// parseGetpropOutput 测试组
// ----------------------------------------------------------------------------

// TestParseGetpropNormal 验证标准三行输出（含 CRLF 换行）被正确解析。
func TestParseGetpropNormal(t *testing.T) {
	// 模拟真实设备输出：CRLF 换行、型号含空格、品牌含大小写
	output := "Pixel 6 Pro\r\n13\r\ngoogle\r\n"
	model, version, brand := parseGetpropOutput(output)
	if model != "Pixel 6 Pro" {
		t.Fatalf("型号解析不符: 期望 %q, 实际 %q", "Pixel 6 Pro", model)
	}
	if version != "13" {
		t.Fatalf("版本解析不符: 期望 %q, 实际 %q", "13", version)
	}
	if brand != "google" {
		t.Fatalf("品牌解析不符: 期望 %q, 实际 %q", "google", brand)
	}
}

// TestParseGetpropMissingTail 验证末尾行缺失（品牌/版本行缺失）时对应字段为占位符。
func TestParseGetpropMissingTail(t *testing.T) {
	// 只有一行：版本与品牌缺失
	model, version, brand := parseGetpropOutput("Mi TV\n")
	if model != "Mi TV" {
		t.Fatalf("型号解析不符: 期望 %q, 实际 %q", "Mi TV", model)
	}
	if version != "未知" || brand != "未知" {
		t.Fatalf("缺失行应为占位符, 实际 version=%q brand=%q", version, brand)
	}

	// 只有两行：品牌缺失
	model, version, brand = parseGetpropOutput("Mi TV\n9\n")
	if version != "9" {
		t.Fatalf("版本解析不符: 期望 %q, 实际 %q", "9", version)
	}
	if brand != "未知" {
		t.Fatalf("品牌行缺失时应为占位符 %q, 实际 %q", "未知", brand)
	}
}

// TestParseGetpropEmpty 验证空输出时三个字段均为占位符（不崩溃）。
func TestParseGetpropEmpty(t *testing.T) {
	model, version, brand := parseGetpropOutput("")
	if model != "未知" || version != "未知" || brand != "未知" {
		t.Fatalf("空输入应返回占位符, 实际 model=%q version=%q brand=%q", model, version, brand)
	}
}

// TestParseGetpropBlankLines 验证输出为空白行时字段为占位符（容错）。
func TestParseGetpropBlankLines(t *testing.T) {
	model, version, brand := parseGetpropOutput(" \r\n \r\n \r\n")
	if model != "未知" || version != "未知" || brand != "未知" {
		t.Fatalf("空白行输入应返回占位符, 实际 model=%q version=%q brand=%q", model, version, brand)
	}
}
