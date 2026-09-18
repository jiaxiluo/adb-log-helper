package ui

// ============================================================================
// 文件名称 : prompt.go
// 功    能 : 控制台日志输出工具（Success / Info / Warn 三个级别）。
//            GUI 模式下没有控制台，这些输出会被系统丢弃，仅用于
//            wails dev 调试时在终端留痕。
//            （历史版本中的交互输入函数 ReadInput/ReadIP/ReadPort、
//             横幅/分割线打印等终端交互遗留代码已随 CLI 版本一起移除。）
// ============================================================================

import "fmt"

// ANSI 颜色常量（仅保留实际使用的级别对应的颜色）
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorBlue   = "\033[34m"
	colorYellow = "\033[33m"
)

// Success 打印成功消息（绿色）
func Success(format string, args ...interface{}) {
	fmt.Println(string(colorGreen) + "[OK] " + string(colorReset) + fmt.Sprintf(format, args...))
}

// Info 打印信息消息（蓝色）
func Info(format string, args ...interface{}) {
	fmt.Println(string(colorBlue) + "[INFO] " + string(colorReset) + fmt.Sprintf(format, args...))
}

// Warn 打印警告消息（黄色）
func Warn(format string, args ...interface{}) {
	fmt.Println(string(colorYellow) + "[WARN] " + string(colorReset) + fmt.Sprintf(format, args...))
}
