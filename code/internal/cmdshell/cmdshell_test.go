//go:build windows

package cmdshell

// ============================================================================
// 文件名称 : cmdshell_test.go
// 功    能 : 命令行模式内核的单元测试：
//            1. ParseCd / ResolveCwd —— cd 命令识别与目录解析（纯函数）
//            2. DecodeConsoleBytes —— 输出编码自适应（UTF-8/GBK）
//            3. RunCommand —— 真实执行链路（echo / cd）
// 备注     : 用例中的路径一律用正斜杠书写（Windows 的 filepath 同样接受），
//            避免反斜杠转义在不同写入通道下的差异问题
// ============================================================================

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestParseCd 验证各种 cd 形态的识别与参数提取
func TestParseCd(t *testing.T) {
	cases := []struct {
		in     string
		target string
		isCd   bool
	}{
		{"cd", "", true},                   // 无参：显示当前目录
		{"CD", "", true},                   // 大小写不敏感
		{"cd /d D:/work", "D:/work", true}, // 跨盘切换
		{"cd ..", "..", true},              // 相对上级
		{"cd \"C:/Program Files\"", "C:/Program Files", true}, // 带引号路径
		{"  cd   /temp ", "/temp", true},                      // 前后空格
		{"dir", "", false},                                    // 非 cd 命令
		{"echo cd test", "", false},                           // cd 在参数位不算
		{"cdx abc", "", false},                                // cd 前缀但非 cd 命令
	}
	for _, c := range cases {
		target, isCd := ParseCd(c.in)
		if isCd != c.isCd || (isCd && target != c.target) {
			t.Errorf("ParseCd(%q) = (%q,%v), want (%q,%v)", c.in, target, isCd, c.target, c.isCd)
		}
	}
}

// TestResolveCwd 验证目录解析：相对/绝对/../不存在/文件
func TestResolveCwd(t *testing.T) {
	base := t.TempDir() // 测试沙盒目录（自动清理），注意其输出为反斜杠形式

	// 1) 相对路径 + ..：base/sub 的上级应回到 base
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveCwd(sub, "..")
	if err != nil || got != base {
		t.Errorf("ResolveCwd(..) = %q, err=%v, want %q", got, err, base)
	}

	// 2) 绝对路径（正斜杠输入，Clean 后与反斜杠形式等价比较）
	got, err = ResolveCwd(base, strings.ReplaceAll(sub, "\\", "/"))
	if err != nil || got != sub {
		t.Errorf("ResolveCwd(abs) = %q, err=%v, want %q", got, err, sub)
	}

	// 3) 目标不存在：报错且 cwd 不变
	got, err = ResolveCwd(base, filepath.Join(base, "no-such-dir"))
	if err == nil || got != base {
		t.Errorf("ResolveCwd(不存在) 应报错且保持 cwd: got %q err=%v", got, err)
	}

	// 4) 目标是文件：报错
	filePath := filepath.Join(base, "afile.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveCwd(base, "afile.txt")
	if err == nil || got != base {
		t.Errorf("ResolveCwd(文件) 应报错且保持 cwd: got %q err=%v", got, err)
	}

	// 5) 空参数 / "."：不变
	got, err = ResolveCwd(base, ".")
	if err != nil || got != base {
		t.Errorf("ResolveCwd(.) = %q err=%v, want %q", got, err, base)
	}
}

// TestDecodeConsoleBytes 验证输出编码自适应
func TestDecodeConsoleBytes(t *testing.T) {
	// 1) UTF-8 直通
	utf8Text := "目录 中文 test"
	if got := DecodeConsoleBytes([]byte(utf8Text)); got != utf8Text {
		t.Errorf("UTF-8 应直通: got %q", got)
	}

	// 2) GBK 字节（"目录名" 的 GBK 编码）→ 解码为 UTF-8。
	//    注意必须 3 个汉字以上：1~2 个汉字的 GBK 字节序列有可能恰好也是
	//    合法 UTF-8（编码歧义是本质局限），而真实命令输出的中文远多于 2 字，
	//    该启发式在真实场景足够可靠
	gbkBytes := []byte{0xC4, 0xBF, 0xC2, 0xBC, 0xC3, 0xFB} // "目录名"
	got := DecodeConsoleBytes(gbkBytes)
	if got != "目录名" {
		t.Errorf("GBK 应解码: got %q, want \"目录名\"", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("解码结果应为合法 UTF-8")
	}

	// 3) 空：空
	if got := DecodeConsoleBytes(nil); got != "" {
		t.Errorf("空输入应返回空: got %q", got)
	}
}

// TestRunCommandEcho 真实执行链路：echo 输出可见、cwd 不变
func TestRunCommandEcho(t *testing.T) {
	base := t.TempDir()
	out, newCwd, err := RunCommand(base, "", "echo hello_cmdshell")
	if err != nil {
		t.Fatalf("echo 执行失败: %v", err)
	}
	if !strings.Contains(out, "hello_cmdshell") {
		t.Errorf("echo 输出应包含文本: got %q", out)
	}
	if newCwd != base {
		t.Errorf("echo 不应改变 cwd: got %q", newCwd)
	}
}

// TestRunCommandCd 真实链路：cd 切换会话目录并持久（下一次命令继承）
func TestRunCommandCd(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "subdir")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}

	// cd 进入子目录
	out, cwd, err := RunCommand(base, "", "cd "+sub)
	if err != nil || cwd != sub {
		t.Fatalf("cd 失败: out=%q cwd=%q err=%v", out, cwd, err)
	}

	// 后续命令应在新目录执行：echo 一个文件再 dir 应能看到
	if _, _, err := RunCommand(cwd, "", "echo hi > proof.txt"); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	out, _, _ = RunCommand(cwd, "", "dir /b")
	if !strings.Contains(out, "proof.txt") {
		t.Errorf("cd 后的命令应在子目录执行（应看到 proof.txt）: got %q", out)
	}

	// cd 到不存在路径：报错且目录不变
	out, cwd2, _ := RunCommand(cwd, "", "cd Z:/no/such/dir")
	if cwd2 != cwd || !strings.Contains(out, "找不到") {
		t.Errorf("cd 失败应保持 cwd 并报错: cwd=%q out=%q", cwd2, out)
	}
}

// TestRunCommandEmpty 空命令直接返回不执行
func TestRunCommandEmpty(t *testing.T) {
	out, cwd, err := RunCommand("C:/", "", "   ")
	if err != nil || out != "" || cwd != "C:/" {
		t.Errorf("空命令应直接返回: out=%q cwd=%q err=%v", out, cwd, err)
	}
}

// TestRunCommandInjectsPath 黑盒验证 PATH 注入：临时目录放一个唯一定义的
// 批处理，注入该目录后 cmd 应能按名解析执行；不注入（adbDir 为空）则找不到。
// 这正是面板 adb 命令的场景——adb 目录不在进程 PATH 时靠注入命中
func TestRunCommandInjectsPath(t *testing.T) {
	base := t.TempDir()
	toolDir := filepath.Join(base, "fake-adb-tools")
	if err := os.MkdirAll(toolDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 唯一命令名，避免碰巧命中系统 PATH 里的同名程序
	const toolName = "zz_cmdshell_inject_probe"
	batPath := filepath.Join(toolDir, toolName+".bat")
	// 批处理用 CRLF 行尾，规避 LF-only 批处理在个别 cmd 版本下的解析怪癖
	if err := os.WriteFile(batPath, []byte("@echo inject_probe_ok\r\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// 1) 注入目录：命令按名解析成功，输出标记文本
	out, _, err := RunCommand(base, toolDir, toolName)
	if err != nil {
		t.Fatalf("注入 PATH 后执行失败: %v", err)
	}
	if !strings.Contains(out, "inject_probe_ok") {
		t.Errorf("注入 PATH 后应执行到批处理: got %q", out)
	}

	// 2) 不注入：同样的命令名应解析失败（找不到程序，输出不含标记文本）
	out, _, err = RunCommand(base, "", toolName)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if strings.Contains(out, "inject_probe_ok") {
		t.Errorf("未注入 PATH 不应执行到批处理: got %q", out)
	}
}

// TestBuildChildEnv 白盒验证子进程环境构造：PATH 只剩一条且注入目录在最前，
// 其余环境变量原样保留
func TestBuildChildEnv(t *testing.T) {
	const fakeDir = `D:\fake-adb-dir`
	env := buildChildEnv(fakeDir)

	pathCount := 0
	pathValue := ""
	for _, e := range env {
		if len(e) >= 5 && strings.EqualFold(e[:5], "PATH=") {
			pathCount++
			pathValue = e[len("PATH="):]
		}
	}
	if pathCount != 1 {
		t.Errorf("子进程环境应只含一条 PATH, got %d", pathCount)
	}
	if !strings.HasPrefix(pathValue, fakeDir+";") {
		t.Errorf("PATH 应以注入目录开头: got %q", pathValue)
	}
	if !strings.HasSuffix(pathValue, os.Getenv("PATH")) {
		t.Errorf("PATH 应保留原值在后: got %q", pathValue)
	}
}
