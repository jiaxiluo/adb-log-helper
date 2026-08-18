package main

// 本文件是程序的入口，负责启动 Wails 桌面窗口。
// 前端页面资源嵌入在 frontend/dist 目录中，通过 go:embed 打包进单个 exe。

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// main 启动 Wails 应用窗口。
func main() {
	// 创建应用绑定实例
	app := NewApp()

	// 运行 Wails 应用
	err := wails.Run(&options.App{
		Title:  "ADB 工具", // 窗口标题
		Width:  1180,     // 窗口宽度（默认尺寸下所有功能一屏可见）
		Height: 880,      // 窗口高度（预留第一栏列表 + 第二栏操作 + 日志 + 反馈面板的完整空间）
		// 前端静态资源服务：从内嵌的 frontend/dist 提供页面
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// 启动与关闭回调
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		// 绑定到前端的结构体，前端通过 window.go.main.App 调用其导出方法
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
