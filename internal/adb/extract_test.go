package adb

// ============================================================================
// 文件名称 : extract_test.go
// 功    能 : internal/adb 包的单元测试（无需真实设备与 adb 环境）。
//            覆盖两个关键纯逻辑：
//              1. extractPlatformTools 解压的安全性与正确性（含 zip-slip 防护）
//              2. parseDevices 对 adb devices 输出的解析
// 运行方式 : 在项目根目录执行 go test ./internal/adb/
// ============================================================================

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ----------------------------------------------------------------------------
// 测试辅助：构造测试用 ZIP 文件
// ----------------------------------------------------------------------------

// writeTestZip 在指定路径创建一个测试用 ZIP 文件。
// 入参:
//   - zipPath: 要生成的 ZIP 文件路径
//   - entries: ZIP 内条目列表，形如 map[条目名]文件内容
//
// 返回: 创建失败时的错误
func writeTestZip(zipPath string, entries map[string]string) error {
	outFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	defer zw.Close()

	for name, content := range entries {
		fw, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			return err
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// extractPlatformTests 测试组
// ----------------------------------------------------------------------------

// TestExtractNormalZip 验证正常 ZIP（带 platform-tools/ 前缀）能正确解压，
// 且前缀被去掉，文件直接落在 adb-tools/ 目录下。
func TestExtractNormalZip(t *testing.T) {
	// 切换到临时目录，避免测试在源码目录留下 adb-tools/ 残留
	t.Chdir(t.TempDir())

	// 构造一个模拟官方 platform-tools 的 ZIP：顶层目录 + 一个文件
	zipPath := "platform-tools.zip"
	entries := map[string]string{
		"platform-tools/adb.exe":       "fake-adb-binary",
		"platform-tools/AdbWinApi.dll": "fake-dll",
	}
	if err := writeTestZip(zipPath, entries); err != nil {
		t.Fatalf("创建测试 ZIP 失败: %v", err)
	}

	// 执行解压
	if err := extractPlatformTools(zipPath); err != nil {
		t.Fatalf("解压正常 ZIP 失败: %v", err)
	}

	// 验证前缀被去掉、内容正确
	for name, want := range entries {
		// ZIP 内路径 platform-tools/xxx → 解压后应为 adb-tools/xxx
		stripped := strings.TrimPrefix(name, "platform-tools/")
		got, err := os.ReadFile(filepath.Join(adbToolsDir, stripped))
		if err != nil {
			t.Fatalf("解压后未找到文件 %s: %v", stripped, err)
		}
		if string(got) != want {
			t.Fatalf("文件 %s 内容不符: 期望 %q, 实际 %q", stripped, want, string(got))
		}
	}
}

// TestExtractZipSlipRejected 验证含 "../" 恶意路径的 ZIP 会被拒绝，
// 且不会把文件写到目标目录（adb-tools/）之外。
func TestExtractZipSlipRejected(t *testing.T) {
	// 切换到临时目录；恶意条目若未被拦截，将尝试写到临时目录之外的 evil.txt
	t.Chdir(t.TempDir())

	zipPath := "evil.zip"
	entries := map[string]string{
		// 恶意条目：解压路径将指向目标目录之外（zip slip 攻击样本）
		"../evil.txt": "you-should-not-write-me",
	}
	if err := writeTestZip(zipPath, entries); err != nil {
		t.Fatalf("创建测试 ZIP 失败: %v", err)
	}

	// 期望解压被安全校验拦截，返回错误
	err := extractPlatformTools(zipPath)
	if err == nil {
		t.Fatal("含 ../ 恶意路径的 ZIP 未被拦截，zip slip 防护失效")
	}
	if !strings.Contains(err.Error(), "非法路径") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}

	// 双重确认：恶意文件确实没有被写出（本目录与上级目录均无 evil.txt）
	for _, p := range []string{"evil.txt", "../evil.txt"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("恶意文件 %s 被写出，zip slip 防护失效", p)
		}
	}
}

// TestExtractZipSlipInsideSurvives 验证合法的子目录路径（如 platform-tools/sub/x.txt）
// 不会被安全校验误伤，能正常解压。
func TestExtractZipSlipInsideSurvives(t *testing.T) {
	t.Chdir(t.TempDir())

	zipPath := "platform-tools.zip"
	entries := map[string]string{
		"platform-tools/sub/x.txt": "nested-content",
	}
	if err := writeTestZip(zipPath, entries); err != nil {
		t.Fatalf("创建测试 ZIP 失败: %v", err)
	}

	if err := extractPlatformTools(zipPath); err != nil {
		t.Fatalf("含子目录的正常条目被误伤: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(adbToolsDir, "sub", "x.txt"))
	if err != nil {
		t.Fatalf("解压后未找到子目录文件: %v", err)
	}
	if string(got) != "nested-content" {
		t.Fatalf("子目录文件内容不符: %q", string(got))
	}
}

// ----------------------------------------------------------------------------
// parsePackages 测试组
// ----------------------------------------------------------------------------

// TestParsePackages 验证 pm list packages 输出解析：
// 去掉 package: 前缀、跳过杂行、关键字过滤。
func TestParsePackages(t *testing.T) {
	output := "package:com.example.app1\n" +
		"package:com.android.systemui\n" +
		"无关行\n" +
		"package:com.example.app2\n"

	// 不带过滤：返回全部合法包名
	pkgs := parsePackages(output, "")
	if len(pkgs) != 3 {
		t.Fatalf("无过滤时包数量不符: 期望 3, 实际 %d (%v)", len(pkgs), pkgs)
	}
	if pkgs[0] != "com.example.app1" || pkgs[2] != "com.example.app2" {
		t.Fatalf("解析结果不符: %v", pkgs)
	}

	// 带过滤：只保留匹配关键字的包名
	filtered := parsePackages(output, "example")
	if len(filtered) != 2 {
		t.Fatalf("过滤后包数量不符: 期望 2, 实际 %d (%v)", len(filtered), filtered)
	}
	for _, p := range filtered {
		if !strings.Contains(p, "example") {
			t.Fatalf("过滤结果混入了不匹配项: %s", p)
		}
	}
}

// TestParsePackagesEmpty 验证空输出返回空切片（非 nil，便于 JSON 序列化为 []）。
func TestParsePackagesEmpty(t *testing.T) {
	pkgs := parsePackages("", "")
	if len(pkgs) != 0 || pkgs == nil {
		t.Fatalf("空输入应返回空切片, 实际 %v", pkgs)
	}
}

// ----------------------------------------------------------------------------
// NormalizeAddress 测试组
// ----------------------------------------------------------------------------

// TestNormalizeAddress 验证设备地址规范化：三种合法输入统一为 "host:port"。
func TestNormalizeAddress(t *testing.T) {
	cases := []struct {
		input string // 用户输入
		want  string // 期望规范化结果
	}{
		{"192.168.1.100", "192.168.1.100:5555"},      // 不带端口 → 补默认 5555
		{"192.168.1.100:5555", "192.168.1.100:5555"}, // 完整地址 → 原样
		{"192.168.1.100:4000", "192.168.1.100:4000"}, // 自定义端口 → 原样
		{" 192.168.1.100 ", "192.168.1.100:5555"},    // 含首尾空白 → 容错去除
	}
	for _, c := range cases {
		got, err := NormalizeAddress(c.input)
		if err != nil {
			t.Fatalf("输入 %q 不应报错: %v", c.input, err)
		}
		if got != c.want {
			t.Fatalf("输入 %q 规范化不符: 期望 %q, 实际 %q", c.input, c.want, got)
		}
	}
}

// TestNormalizeAddressInvalid 验证非法地址被拒绝：空串、坏 IP、坏端口。
func TestNormalizeAddressInvalid(t *testing.T) {
	cases := []string{
		"",                 // 空地址
		"   ",              // 纯空白
		"not-an-ip",        // 非法主机名（非 IP 也非 localhost）
		"abc.def.ghi:5555", // 非法 IP 带端口
		"1.2.3:",           // 端口为空
	}
	for _, c := range cases {
		if got, err := NormalizeAddress(c); err == nil {
			t.Fatalf("输入 %q 应被拒绝, 实际返回 %q", c, got)
		}
	}
}

// ----------------------------------------------------------------------------
// parseDevices 测试组
// ----------------------------------------------------------------------------

// TestParseDevices 验证 adb devices 输出的标准解析：
// 跳过表头与空行，正确取出序列号与状态。
func TestParseDevices(t *testing.T) {
	output := "List of devices attached\n" +
		"192.168.1.100:5555\tdevice\n" +
		"ABC123\tunauthorized\n" +
		"\n" +
		"emulator-5554\toffline\n"

	devices := parseDevices(output)

	if len(devices) != 3 {
		t.Fatalf("设备数量不符: 期望 3, 实际 %d (%v)", len(devices), devices)
	}

	// 逐台校验序列号与状态
	want := []Device{
		{Serial: "192.168.1.100:5555", State: "device"},
		{Serial: "ABC123", State: "unauthorized"},
		{Serial: "emulator-5554", State: "offline"},
	}
	for i, w := range want {
		if devices[i] != w {
			t.Fatalf("第 %d 台设备不符: 期望 %+v, 实际 %+v", i, w, devices[i])
		}
	}
}

// TestParseDevicesEmpty 验证无设备时返回空列表（且非 nil，便于前端 JSON 序列化）。
func TestParseDevicesEmpty(t *testing.T) {
	devices := parseDevices("List of devices attached\n")
	if len(devices) != 0 {
		t.Fatalf("无设备时应返回空列表, 实际 %v", devices)
	}
	if devices == nil {
		t.Fatal("无设备时应返回空切片而非 nil（保证 JSON 序列化为 []）")
	}
}

// TestParseDevicesGarbage 验证对异常行（字段不足）的容错：跳过而不崩溃。
func TestParseDevicesGarbage(t *testing.T) {
	output := "List of devices attached\n" +
		"孤行无状态\n" + // 只有 1 个字段，应被跳过
		"serial1 device extra\n" // 超过 2 个字段，取前两个
	devices := parseDevices(output)
	if len(devices) != 1 {
		t.Fatalf("异常行容错解析数量不符: 期望 1, 实际 %d (%v)", len(devices), devices)
	}
	if devices[0].Serial != "serial1" || devices[0].State != "device" {
		t.Fatalf("解析结果不符: %+v", devices[0])
	}
}
