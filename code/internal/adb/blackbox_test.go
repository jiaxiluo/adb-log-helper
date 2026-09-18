package adb

// ============================================================================
// 文件名称 : blackbox_test.go（包 adb，仅 Windows）
// 功    能 : 黑盒集成测试 —— 不关心内部实现，只验证"输入 → 真实子进程 →
//            输出"的端到端行为。用 stubadb 假 adb 验证：
//              1. ClearLogcat 确实以 adb -s <serial> logcat -c 命令行调用 adb
//              2. Devices 对真实子进程 stdout 的解析（list of devices 附表）
//            需要 go 工具链（测试内编译 stub）；不可用时跳过。
// ============================================================================

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildStubADB 把 stubadb 编译为临时 exe，返回路径（失败时跳过测试）。
func buildStubADB(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 需要（本项目仅面向 Windows）")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("无 go 工具链，跳过黑盒测试:", err)
	}
	src, err := filepath.Abs(filepath.Join("blackbox", "stubadb"))
	if err != nil {
		t.Skip(err)
	}
	bin := filepath.Join(t.TempDir(), "stubadb.exe")
	cmd := exec.Command(goTool, "build", "-o", bin, src)
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skip("编译 stub adb 失败，跳过黑盒测试:", err, string(out))
	}
	return bin
}

// 黑盒用例 1：ClearLogcat 发出的命令行必须是 -s <serial> logcat -c
func TestBlackboxClearLogcatCommand(t *testing.T) {
	stub := buildStubADB(t)
	argsFile := filepath.Join(t.TempDir(), "args.txt")

	t.Setenv("STUB_OUT", argsFile)
	if err := os.Setenv("STUB_OUT", argsFile); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("STUB_OUT")

	if _, err := ClearLogcat(stub, "192.168.1.100:5555"); err != nil {
		t.Fatal("ClearLogcat 不应失败:", err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal("stub 未收到调用:", err)
	}
	want := "-s\n192.168.1.100:5555\nlogcat\n-c"
	if string(got) != want {
		t.Fatalf("命令行不符：\n期望 %q\n实际 %q", want, string(got))
	}
}

// 黑盒用例 2：Devices 端到端解析（真子进程 stdout → []Device）
func TestBlackboxDevicesParse(t *testing.T) {
	stub := buildStubADB(t)

	// 通过 STUB_REPLY 让 stub 输出一段标准 adb devices 报文。
	// 注意：stub 只在启动时读一次环境变量，这里用 exec 注入式验证不可行
	//（run() 不透传自定义 env），故直接在测试进程设置后调用。
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("STUB_OUT", argsFile)
	t.Setenv("STUB_REPLY", "List of devices attached\r\n192.168.1.100:5555\tdevice\r\nemulator-5554\toffline\r\n\r\n")
	if err := os.Setenv("STUB_OUT", argsFile); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("STUB_OUT")
	if err := os.Setenv("STUB_REPLY", "List of devices attached\r\n192.168.1.100:5555\tdevice\r\nemulator-5554\toffline\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("STUB_REPLY")

	devices, err := Devices(stub)
	if err != nil {
		t.Fatal("Devices 不应失败:", err)
	}
	if len(devices) != 2 {
		t.Fatalf("应解析出 2 台设备，实际 %d 台：%+v", len(devices), devices)
	}
	if devices[0].Serial != "192.168.1.100:5555" || devices[0].State != "device" {
		t.Fatalf("第 1 台设备不符：%+v", devices[0])
	}
	if devices[1].Serial != "emulator-5554" || devices[1].State != "offline" {
		t.Fatalf("第 2 台设备不符：%+v", devices[1])
	}
	_ = strings.TrimSpace // 保留 import 以便扩展
}
