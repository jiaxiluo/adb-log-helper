/* ============================================================================
 * 文件名称 : wails_mock.js
 * 功    能 : 操作手册截图专用 —— 拦截并替换 Wails 生成的两个绑定脚本
 *            （/wailsjs/go/main/App.js 与 /wailsjs/runtime/runtime.js），
 *            在无 Go 后端的环境下驱动真实前端界面进入指定状态。
 * 使用方式 : Playwright route 拦截上述两个 URL，用本文件内容应答；
 *            截图脚本通过 window.__mockState 控制返回数据，
 *            通过 window.__fireMockEvent(name, data) 模拟后端事件推送。
 * 说    明 : 仅用于生成文档截图，不属于产品代码。
 * ==========================================================================*/
(function () {
    "use strict";

    // 可控状态（截图脚本用 page.add_init_script 写入 __initialMockState，
    // 保证在页面脚本启动前生效——main.js 在 DOMContentLoaded 即查询 ADB 状态）
    window.__mockState = Object.assign({
        adbPath: "",              // "" = 未安装（向导显示安装流程）
        installLocalFails: false, // true = 本地自动安装失败（向导展示手动两种方式）
        devices: [],              // [{serial, state}]
        devicesDetail: [],        // [{serial, state, transport, model, brand, version}]
        recent: [],               // [{serial, disconnectedAt}]
        packages: [],             // ["com.xxx", ...]
        recState: { recording: false, elapsed: 0, phase: "idle" },
        pickFile: "",             // SelectFile 返回值
        pickDir: "",              // SelectDirectory 返回值
        cmdOutputs: {},           // 本机命令 → 输出
        shellOutputs: {},         // 设备 shell 命令 → 输出
        cwd: "D:\\ADB工具"
    }, window.__initialMockState || {});

    var ADB_PATH = "D:\\ADB工具\\adb-tools\\platform-tools\\adb.exe";

    function S() { return window.__mockState; }
    function ok(v) { return Promise.resolve(v); }
    function fail(m) { return Promise.reject(new Error(m)); }

    window.go = {
        main: {
            App: {
                /* ---- ADB 环境向导 ---- */
                GetAdbStatus: function () { return ok(S().adbPath || ""); },
                RecheckAdb: function () { return ok(S().adbPath || ""); },
                InstallAdbLocal: function () {
                    if (S().installLocalFails) {
                        return fail("程序目录下未找到 platform-tools 压缩包");
                    }
                    S().adbPath = ADB_PATH;
                    return ok(ADB_PATH);
                },
                InstallAdbOnline: function () {
                    // 模拟下载进度条（约 1 秒走完，中途截图可得半程进度）
                    return new Promise(function (resolve) {
                        var total = 8385300, recv = 0;
                        var timer = setInterval(function () {
                            recv += total * 0.12;
                            var pct = Math.min(100, Math.round(recv * 100 / total));
                            window.__fireMockEvent("adb-setup-progress", {
                                percent: pct,
                                received: Math.min(recv, total),
                                total: total
                            });
                            if (pct >= 100) {
                                clearInterval(timer);
                                S().adbPath = ADB_PATH;
                                resolve(ADB_PATH);
                            }
                        }, 100);
                    });
                },
                SelectZipFile: function () { return ok("E:\\安装包\\platform-tools-latest-windows.zip"); },

                /* ---- 设备 ---- */
                GetDevices: function () { return ok(S().devices); },
                GetDevicesDetail: function () { return ok(S().devicesDetail); },
                GetRecentDevices: function () { return ok(S().recent); },
                Connect: function (addr) {
                    var full = addr.indexOf(":") >= 0 ? addr : addr + ":5555";
                    return ok("已连接到 " + full);
                },
                Disconnect: function (serial) { return ok("已断开 " + serial); },

                /* ---- 文件 / 目录选择 ---- */
                SelectFile: function () { return ok(S().pickFile); },
                SelectDirectory: function () { return ok(S().pickDir); },

                /* ---- 屏幕采集 ---- */
                Screenshot: function (serial, dir) {
                    var base = dir || "D:\\ADB工具\\screenshots";
                    return ok(base + "\\screenshot_20260930_103052.png");
                },
                StartScreenRecord: function () { return ok(""); },
                IsRecording: function () { return ok(S().recState); },
                StopScreenRecord: function () { return ok("D:\\ADB工具\\videos\\screenrecord_20260930_101500.mp4"); },
                AbortScreenRecord: function () { return ok(""); },

                /* ---- 文件传输 / 应用 ---- */
                Pull: function (serial, remote, local) { return ok("已保存到 " + local + "\\DCIM"); },
                Push: function (serial, local, remote) { return ok("1 个文件已推送到 " + remote); },
                ListPackages: function () { return ok(S().packages); },
                InstallAPK: function (serial, apk) { return ok("Success"); },
                StartApp: function (serial, pkg) { return ok("已启动 " + pkg); },
                ForceStop: function (serial, pkg) { return ok("进程已结束"); },
                ClearCache: function (serial, pkg) { return ok("Success"); },
                Uninstall: function (serial, pkg) { return ok("Success"); },

                /* ---- 日志 ---- */
                StartLogcat: function (serial, dir) {
                    var base = dir || ("D:\\ADB工具\\logs\\" + serial);
                    return ok(base + "\\adb_log_20260930_102401.log");
                },
                StopLogcat: function () { return ok(""); },
                StartLiveLog: function () { return ok(""); },
                StopLiveLog: function () { return ok(""); },

                /* ---- 命令行 ---- */
                RunCmd: function (cmd) { return ok(S().cmdOutputs[cmd.trim()] || ""); },
                GetCmdCwd: function () { return ok(S().cwd); },
                StartShell: function () { return ok(""); },
                WriteShell: function (cmd) {
                    var key = cmd.trim();
                    if (key === "exit") {
                        setTimeout(function () { window.__fireMockEvent("cmd-shell-ended", {}); }, 40);
                        return ok("");
                    }
                    var out = S().shellOutputs[key];
                    if (out !== undefined) {
                        setTimeout(function () { window.__fireMockEvent("cmd-shell-output", out); }, 40);
                    }
                    return ok("");
                },
                StopShellTerm: function () { return ok(""); }
            }
        }
    };

    /* ---- Wails runtime 事件订阅的 mock ---- */
    var listeners = {};
    window.runtime = {
        EventsOn: function (name, cb) {
            (listeners[name] = listeners[name] || []).push(cb);
        },
        EventsOff: function (name) { delete listeners[name]; }
    };
    window.__fireMockEvent = function (name, data) {
        (listeners[name] || []).forEach(function (cb) { cb(data); });
    };
})();
