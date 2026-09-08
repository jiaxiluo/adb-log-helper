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
	out, newCwd, err := RunCommand(base, "echo hello_cmdshell")
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
	out, cwd, err := RunCommand(base, "cd "+sub)
	if err != nil || cwd != sub {
		t.Fatalf("cd 失败: out=%q cwd=%q err=%v", out, cwd, err)
	}

	// 后续命令应在新目录执行：echo 一个文件再 dir 应能看到
	if _, _, err := RunCommand(cwd, "echo hi > proof.txt"); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	out, _, _ = RunCommand(cwd, "dir /b")
	if !strings.Contains(out, "proof.txt") {
		t.Errorf("cd 后的命令应在子目录执行（应看到 proof.txt）: got %q", out)
	}

	// cd 到不存在路径：报错且目录不变
	out, cwd2, _ := RunCommand(cwd, "cd Z:/no/such/dir")
	if cwd2 != cwd || !strings.Contains(out, "找不到") {
		t.Errorf("cd 失败应保持 cwd 并报错: cwd=%q out=%q", cwd2, out)
	}
}

// TestRunCommandEmpty 空命令直接返回不执行
func TestRunCommandEmpty(t *testing.T) {
	out, cwd, err := RunCommand("C:/", "   ")
	if err != nil || out != "" || cwd != "C:/" {
		t.Errorf("空命令应直接返回: out=%q cwd=%q err=%v", out, cwd, err)
	}
}
