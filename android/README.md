# ADB 助手 · 安卓版（adb-helper-android）

桌面版 [adb-log-helper](../README.md) 的手机版：装在安卓手机上的**现场应急工具**——
手边没有电脑时，用手机通过 Wi-Fi 直连电视/盒子（ADB 5555 端口），完成两件事：

1. **装 APK**：从手机里选一个 APK（微信/QQ 收的包），一键推到电视/盒子安装
2. **抓日志**：开始/结束两键式抓全量 logcat，结束后直接拉起系统分享（微信/钉钉），当场发回群里

全程无需电脑、无需命令行。

> 版本线说明：安卓版独立版本线（V1.0.0 起），与桌面版 V1.8.x 互不影响，代码互不共用
> （桌面版为 Go + Wails，安卓版为 Kotlin 原生）。

## 功能（V1.0.0）

- **设备连接**：输入 IP 连接电视/盒子（缺省端口 5555），显示连接状态，历史设备一键重连
- **安装 APK**：系统文件选择器选 APK，一键 `adb install -r` 推装，失败原因中文化
- **一键抓日志**：抓取期间可退到后台/息屏继续（前台服务）；日志落手机文件，结束后直接分享
- **崩溃自报告**：App 崩溃时自动留存崩溃日志供回传

## 技术要点

- Kotlin + Jetpack Compose（Material 3），无第三方业务依赖，`minSdk 26` / `targetSdk 34`
- **手机内置 adb 二进制**：`app/src/main/jniLibs/` 打包 arm64-v8a / armeabi-v7a 两个 ABI 的
  adb 及其依赖库（libprotobuf 等，取自 Termux 环境提取），App 自身充当 adb 客户端，
  不依赖系统 adb、无需 root
- 抓日志用前台服务保活，规避厂商 ROM 息屏杀后台

## 构建

环境要求：JDK 17、Android SDK（build-tools 34）、本机安装 Gradle（工程未带 wrapper）。

```bash
cd android
gradle assembleDebug                    # 常规构建
python package_apk.py                   # 发布流水线（推荐）
```

`package_apk.py` 一条龙：构建 → 补回 AGP 静默丢弃的带版本号 .so（libz.so.1 等）→
zipalign → 正式签名 → 依赖闭包终验（递归核对每个库的 DT_NEEDED，缺失即拒绝发布）→
发布 APK 到 `../adb-helper/`。

> **签名说明**：正式 keystore 不在本仓库。签名口令通过环境变量 `ADB_HELPER_KS_PASS`
> 或本目录 `signing.env` 文件（已被 .gitignore 忽略）提供，仓库中不含任何口令。
> keystore 文件请本地妥善保管——丢失则新版无法覆盖安装。

## 目录结构

```
android/
├── app/
│   ├── src/main/java/com/jiaxluo/adbhelper/   # Kotlin 源码（12 个类）
│   │   ├── MainActivity.kt                    # 界面与交互
│   │   ├── DeviceManager.kt                   # 设备连接/历史管理
│   │   ├── InstallManager.kt                  # APK 选档与安装
│   │   ├── LogCaptureService.kt               # 抓日志前台服务
│   │   ├── AdbCore.kt / AdbParser.kt          # adb 进程调用与输出解析
│   │   ├── HistoryStore.kt / AppGraph.kt      # 历史存储 / 应用信息
│   │   └── CrashReporter.kt                   # 崩溃日志留存
│   ├── src/main/jniLibs/                      # 内置 adb 二进制（两 ABI）
│   ├── src/test/                              # 单元测试（AdbParser 等）
│   └── build.gradle.kts
├── package_apk.py                             # 发布流水线（见上）
└── build.gradle.kts / settings.gradle.kts / gradle.properties
```

## 文档

- [需求规格说明书 V1.0.0](../docs/requirement-android-1.0.0.md)
- [UI 原型（浏览器可交互演示）](../docs/ui-mockup-android-1.0.0.html)
