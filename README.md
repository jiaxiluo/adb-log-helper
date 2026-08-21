# ADB 工具 — 桌面图形化 ADB 操作助手

一个零前端依赖的 Windows 桌面工具（基于 Wails v2 + WebView2），用图形界面完成常用的 ADB 操作与日志抓取，无需手动敲命令。

> 📦 仓库地址：https://github.com/jiaxiluo/adb-log-helper （可执行文件见 [Releases](https://github.com/jiaxiluo/adb-log-helper/releases)，无需自行编译）

## 功能特性

- ✅ 桌面图形化前台页面，双击即开，面向非技术用户设计
- ✅ **ADB 环境检测引导页**：启动时自动检测本机 ADB —— 已安装直接进入主界面；未安装则先自动尝试从程序同目录的压缩包解压安装（无需联网），不可用时提供两种手动方式：**选择本地压缩包** / **联网下载安装**（Google 官方源，带实时进度条），安装完成自动进入主界面
- ✅ 自动配置系统环境变量 PATH（安装后新开终端也可直接使用 adb）
- ✅ **设备切换联动**：切换目标设备时自动停止旧设备的日志抓取、清空应用列表并恢复默认状态；后台检测到选中设备断开时同样自动清理并提示重新选择
- ✅ **设备连接一体化**：TCP 设备输入 IP（端口默认 5555）即可连接/断开；设备列表**后台每 3 秒自动刷新 + 窗口聚焦即刷新**，下拉框展开即可看到最新设备（USB 设备即插即用），另有手动「刷新设备」按钮
- ✅ **查看设备列表弹窗**：一键查看每台设备的**序列号 / 连接方式（USB / TCP / 模拟器）/ 状态 / 设备型号 / 设备品牌 / 安卓版本**，可直接「选为当前设备」或断开 TCP 设备（设备属性并行查询、单台 4 秒超时，一台异常不拖垮整个列表）
- ✅ **应用管理**：一键查询设备应用列表，支持包名关键字过滤与**查询范围切换（全部 / 仅第三方应用**，排除系统预装），每个应用右侧直接提供「启动（am start，旧系统自动回退 monkey）/ 强停（am force-stop）/ 清缓存 / 卸载」按钮（危险操作均有二次确认）
- ✅ **文件与设备操作**（2×2 紧凑网格，默认窗口一屏可见）：**截图零输入一键保存**（自动存到程序目录 `screenshots/`）、安装 APK（-r 覆盖）、pull 拉取、push 推送（所有命令后台静默执行，**不弹出任何 cmd 窗口**）
- ✅ **自定义确认弹窗**：危险操作（清理/卸载）使用深色科技感弹窗二次确认，替代原生 confirm
- ✅ **一键日志抓取**：只有「开始 / 结束」两个按钮 + 状态提示，无实时终端；日志自动保存为带时间戳的文件，目录默认在程序目录 `logs/` 下，也可自选电脑上的任意目录
- ✅ **底部三模式面板（V1.8）**：窗口底部固定面板支持三种模式互斥切换（分段按钮）——
  - **操作反馈**（默认）：所有操作结果带时间戳实时显示，切到其他模式期间数据持续积累、切回即可见；保留清空与收起
  - **命令行**：本机 cmd 终端，直接输入 Windows 命令（dir / ipconfig / ping 等）回车执行，输出打在面板里；支持 `cd` 持久化、↑↓ 翻命令历史、提示符显示当前目录；中文输出自动转码（GBK/UTF-8 自适应）
  - **实时日志**（参考 Android Studio Logcat）：设备日志实时流式展示——**级别过滤**（Verbose~Fatal，设备端原生过滤）、**关键字即时过滤**、**包名过滤**（输入包名回车，只看该应用日志，等价 AS 的 package 过滤；应用需运行中）、**暂停 / 继续**、**清空**、**「⬇ 滚动到最新」图标按钮**（点亮=自动跟随最新一行，向上翻看自动熄灭、滚回底部自动点亮）；打开即自动滚到最新打印流；级别着色 V 灰 / D 蓝 / I 绿 / W 黄 / E 红 / F 深红
  - **智能高度**：切到命令行/实时日志模式自动升高到最大（约半屏），「展开最大/收起」两档切换（默认 ↔ 最大），也可拖拽把手自由调整
  - 高性能设计：视口外行跳过渲染（浏览器级虚拟化）、展示上限 1000 行 + 内存缓冲 5000 行、合帧批量渲染——高频设备日志洪峰下仍满帧不卡（8000 行/8 秒压测实测帧间隔 ~6ms）
  - 拖拽把手与清空/收起按钮三模式共用
- ✅ 单个 `.exe` 文件，前端资源全部内嵌，无运行时依赖（除系统自带 WebView2）

## 环境要求

- Windows 10 / Windows 11（系统自带 WebView2 运行时）
  - 极少数精简版系统若提示缺少 WebView2，请安装 [Microsoft Edge WebView2 Evergreen Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)
- ADB Platform Tools：首次运行时会自动从同目录的 `platform-tools.zip` 安装，也可预先配置好系统 PATH

## 使用方式（使用者）

1. 将 `adb-log-helper.exe` 与 `platform-tools.zip` 放在同一个文件夹
2. 双击 `adb-log-helper.exe`
3. USB 设备直接在「设备连接」的下拉框选择（列表自动保持最新）；TCP 设备先输入 IP 点击「连接」；需要查看设备型号/安卓版本等完整信息时点击「查看设备列表」
4. 所有操作的结果实时显示在窗口底部的「操作反馈」栏

### 设备端准备（需先通过 USB 连接一次开启 TCP/IP）

```bash
adb tcpip 5555        # 开启 TCP/IP 模式
```
之后在工具页面输入设备 IP 连接即可。

## 分发包结构

```
分发包/
├── adb-log-helper.exe          # 主程序（单文件，前端已内嵌）
└── platform-tools.zip          # ADB 安装包（可选，已装 ADB 可省略）
```

**获取 platform-tools.zip**（在有网络的电脑上下载）：
- 下载地址：https://dl.google.com/android/repository/platform-tools-latest-windows.zip
- 下载后重命名为 `platform-tools.zip`（或保持原名也可）

**支持的安装包文件名**（放在 exe 同目录即可自动识别）：
- `platform-tools.zip`
- `platform-tools-latest-windows.zip`
- `adb-tools.zip`

## 编译（开发者）

需要 Go 1.22+ 环境。注意：wails 最新版（v2.14.0）要求 Go 1.25，会触发 go1.25 工具链下载；本项目固定使用 **v2.11.0**（仅需 Go 1.22），配合 `GOTOOLCHAIN=local` 避免下载额外工具链。

```bash
# 1. 安装 wails CLI（固定 v2.11.0）
go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0

# 2. 构建方式一：直接双击 build.bat（已内置版本锁定与工具链设置）
#    构建方式二：手动执行
cd tools/adb-log-helper
set GOTOOLCHAIN=local
go get github.com/wailsapp/wails/v2@v2.11.0
go mod tidy
wails build
```

`build.bat` 已内置上述全部步骤（含自动安装 CLI、固定库版本），双击即可。

构建成功后，产物会自动发布到 `adb-helper/adb-log-helper.exe`（项目内仅保留这一份程序副本，`build/bin/` 下的原始产物会被清理）。各步骤日志仅在有失败时保留。

开发调试（支持前端热更新）：

```bash
wails dev
```

## 项目结构

```
adb-log-helper/
├── main.go                 # 入口，wails.Run 启动窗口
├── app.go                  # App 绑定结构体，前端可调用的 Go 方法
├── go.mod / go.sum
├── wails.json              # Wails 构建配置
├── build.bat               # 一键构建脚本（成功即删日志、产物发布到 adb-helper/）
├── check.bat               # 自动化检查（vet/fmt/build/test/wails 构建）
├── internal/
│   ├── adb/
│   │   ├── detect.go       # 检测 ADB 是否已安装
│   │   ├── install.go      # ADB 安装：本地压缩包解压 / 联网下载，配置 PATH
│   │   ├── connect.go      # TCP 设备连接/断开（adb connect / disconnect）
│   │   ├── logcat.go       # 一键日志抓取会话（写文件，无实时终端）
│   │   ├── livelog.go      # 实时日志会话（级别过滤 + 批量推送 + 行解析，V1.6）
│   │   ├── ops.go          # 常用 adb 操作封装（设备/设备详情/应用/截图/安装/pull/push）
│   │   ├── cmd_windows.go  # hiddenCmd / hiddenCmdContext：统一创建不弹窗的子进程（可选超时）
│   │   ├── extract_test.go # 单元测试（解压安全/设备解析/地址校验）
│   │   └── devices_detail_test.go # 单元测试（连接方式判定/设备属性解析）
│   │   └── download_test.go# 单元测试（联网下载核心，httptest 离线验证）
│   ├── cmdshell/           # 命令行模式内核（V1.8）：cmd 执行 + cd 持久化 + 输出转码
│   ├── pathx/registry.go   # Windows 注册表 PATH 操作
│   └── ui/prompt.go        # 控制台日志输出（GUI 下被丢弃，仅调试用）
└── frontend/
    └── dist/
        ├── index.html      # 前台页面结构（环境准备向导 + 单图层主界面）
        ├── main.js         # 前端交互逻辑
        └── style.css       # 页面样式（固定视口，面板内部滚动）
```

## 日志文件格式

日志文件保存在 `logs/<设备标识>/` 目录，命名规则：
```
adb_log_20260623_150405.log
```

文件内容包含设备地址、抓取开始/结束时间、完整 logcat 输出。
