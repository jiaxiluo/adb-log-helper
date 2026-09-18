package main

// ============================================================================
// 文件名称 : main.go（stubadb —— 黑盒测试用的假 adb）
// 功    能 : 把收到的命令行参数逐行写入 STUB_OUT 环境变量指定的文件，
//            并把 STUB_REPLY 指定的文本打印到 stdout，模拟真实 adb 的行为。
//            由 blackbox_test.go 在测试时编译为临时 exe 使用。
// ============================================================================

import (
	"os"
	"strings"
)

func main() {
	out := os.Getenv("STUB_OUT")
	if out != "" {
		_ = os.WriteFile(out, []byte(strings.Join(os.Args[1:], "\n")), 0644)
	}
	reply := os.Getenv("STUB_REPLY")
	if reply != "" {
		os.Stdout.WriteString(reply)
	}
}
