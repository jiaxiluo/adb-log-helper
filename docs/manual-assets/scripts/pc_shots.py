# -*- coding: utf-8 -*-
"""
文件名称 : pc_shots.py
功    能 : 生成 PC 版（adb-log-helper V1.8.2）图解操作手册所需的全部界面截图。
             加载 code/frontend/dist 里的真实前端代码（与 exe 打包的同一份），
             用 wails_mock.js 替换 Wails 绑定脚本，把界面驱动到指定状态后截图。
             视口 1180×880 与程序主窗口一致，2 倍缩放保证 PDF 印刷清晰。
用    法 : python pc_shots.py
输    出 : ../pc-shots/01-*.png ~ 19-*.png
说    明 : 仅用于生成文档截图，不属于产品代码。界面更新后重跑本脚本即可重出图。
"""

import json
import threading
from functools import partial
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler
from pathlib import Path

from playwright.sync_api import sync_playwright

HERE = Path(__file__).resolve().parent
DIST = HERE.parents[2] / "code" / "frontend" / "dist"
OUT = HERE.parent / "pc-shots"
MOCK = (HERE / "wails_mock.js").read_text(encoding="utf-8")

PORT = 8791
URL = f"http://127.0.0.1:{PORT}/index.html"
VIEW_W, VIEW_H = 1180, 880          # 与 main.go 中窗口尺寸一致
MAIN_TV = "172.31.15.4:5555"       # 主场景：网线连接的小米电视

# ----------------------------------------------------------------------------
# 模拟数据（电视/盒子主场景）
# ----------------------------------------------------------------------------

DEVICES = [
    {"serial": MAIN_TV, "state": "device"},
    {"serial": "9Y2LX10W5F", "state": "device"},        # USB 手机
    {"serial": "172.31.20.8:5555", "state": "offline"}, # 一台离线的电视
]

DEVICES_DETAIL = [
    {"serial": MAIN_TV, "state": "device", "transport": "TCP",
     "model": "L55M7-ES", "brand": "Xiaomi", "version": "11"},
    {"serial": "9Y2LX10W5F", "state": "device", "transport": "USB",
     "model": "LIO-AN00", "brand": "HUAWEI", "version": "12"},
    {"serial": "172.31.20.8:5555", "state": "offline", "transport": "TCP",
     "model": "-", "brand": "-", "version": "-"},
]

RECENT = [
    {"serial": "172.31.15.4:5555", "disconnectedAt": "2026-09-29T18:42:10+08:00"},
    {"serial": "172.31.20.8:5555", "disconnectedAt": "2026-09-28T09:15:33+08:00"},
]

PACKAGES = [
    "com.tencent.qqlivestb",
    "tv.danmaku.bili",
    "com.duokan.videodaily",
    "com.xiaomi.mitv.tvassistant",
    "com.gitv.live.gdmobile",
    "com.hunantv.mtv",
    "com.ktcp.video",
    "com.mango.tv.danmaku",
    "com.snm.SNMJ",
    "com.tianhe.movie",
]

CMD_OUTPUTS = {
    "adb devices": "List of devices attached\r\n"
                   "172.31.15.4:5555    device\r\n"
                   "9Y2LX10W5F          device\r\n",
}

SHELL_OUTPUTS = {
    "ls /sdcard": "Alarms   DCIM      Download  Movies   Music\r\n"
                  "Pictures Podcats   Ringtones",
    "getprop ro.build.version.release": "11",
}

# 实时日志样例（覆盖 V/D/I/W/E/F 各级别 + 无级别分隔行）
LIVE_LOG = [
    ("09-30 10:23:41.128", "?", "", "", "--------- beginning of main"),
    ("09-30 10:23:41.208", "I", "ActivityManager", "2103",
     "Start proc 8421:com.tencent.qqlivestb/u0a126 for activity com.tencent.qqlivestb/.SplashActivity"),
    ("09-30 10:23:41.355", "D", "OpenGLRenderer", "8421", "Initialized EGL, version 1.5"),
    ("09-30 10:23:41.512", "V", "ViewRootImpl", "8421", "resumed=true, reportDrawFinished"),
    ("09-30 10:23:41.688", "W", "Choreographer", "8421", "Frame is skipping, 42 frames dropped"),
    ("09-30 10:23:42.001", "I", "TvRemote", "1120", "HDMI-CEC device added: MiTV-Soundbar"),
    ("09-30 10:23:42.310", "D", "NetworkMonitor", "512", "Wi-Fi link validated on wlan0"),
    ("09-30 10:23:42.774", "E", "AudioFlinger", "318", "AudioTrack write timeout, buffer underrun"),
    ("09-30 10:23:43.090", "I", "LauncherModel", "1580", "Package updated: com.gitv.live.gdmobile"),
    ("09-30 10:23:43.456", "W", "BatteryStatsService", "940", "battery current now 312mA"),
    ("09-30 10:23:43.820", "D", "MediaPlayer", "8421", "setDataSource: rtsp://175.30.1.16/live/ch03"),
    ("09-30 10:23:44.105", "I", "STBPlayer", "8421", "Video decoder init: hevc 1920x1080 @25fps"),
    ("09-30 10:23:44.512", "F", "libc", "8533", "Fatal signal 11 (SIGSEGV) in tid 8533 (RenderThread)"),
    ("09-30 10:23:44.890", "E", "DEBUG", "8550", "Abort message: 'unconditional branch to null'"),
    ("09-30 10:23:45.201", "W", "ActivityManager", "2103", "Process com.gitv.live.gdmobile exited with 6"),
    ("09-30 10:23:45.633", "I", "WifiService", "940", "Scan results: 12 networks found"),
    ("09-30 10:23:46.010", "D", "TvInputHardware", "705", "tune to frequency 626000 kHz"),
    ("09-30 10:23:46.388", "I", "LauncherModel", "1580", "All packages loaded in 2314ms"),
    ("09-30 10:23:46.774", "W", "GraphicsEnv", "8421", "Vulkan driver outdated, fallback to GL"),
    ("09-30 10:23:47.155", "E", "SoftapController", "501", "Failed to get valid channels"),
    ("09-30 10:23:47.502", "I", "PowerManagerService", "940", "Waking up from suspend"),
    ("09-30 10:23:47.860", "D", "BufferQueue", "8421", "queueBuffer: fps=24.8, dur=1010.52"),
    ("09-30 10:23:48.233", "V", "TvRemote", "1120", "keycode 23 delivered in 12ms"),
    ("09-30 10:23:48.601", "I", "STBPlayer", "8421", "Audio track: aac 48000Hz stereo"),
    ("09-30 10:23:48.975", "W", "InputDispatcher", "918", "channel expired, dropping gesture"),
    ("09-30 10:23:49.320", "E", "OMXMaster", "8421", "Failed to init OMX.SEC codec"),
    ("09-30 10:23:49.688", "I", "ActivityTaskManager", "2103", "Displayed com.tencent.qqlivestb/.MainActivity"),
]


def live_payload():
    return [
        {"time": t, "level": lv, "tag": tag, "pid": pid, "message": msg}
        for (t, lv, tag, pid, msg) in LIVE_LOG
    ]


# ----------------------------------------------------------------------------
# 基础设施
# ----------------------------------------------------------------------------

def start_server():
    handler = partial(SimpleHTTPRequestHandler, directory=str(DIST))
    srv = ThreadingHTTPServer(("127.0.0.1", PORT), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def new_page(browser, state):
    """打开新页面：注入初始 mock 状态 + 拦截 Wails 绑定脚本。"""
    ctx = browser.new_context(
        viewport={"width": VIEW_W, "height": VIEW_H}, device_scale_factor=2)
    page = ctx.new_page()
    page.add_init_script(f"window.__initialMockState = {json.dumps(state)};")
    for pat in ("**/wailsjs/go/main/App.js", "**/wailsjs/runtime/runtime.js"):
        page.route(
            pat,
            lambda route: route.fulfill(
                status=200, content_type="application/javascript", body=MOCK))
    # 标注层样式：红色序号圆圈 + 虚线框（手册图解标注，画在截图上）
    page.add_style_tag(content="""
        .shot-anno{position:fixed;width:24px;height:24px;border-radius:50%;
            background:#e11d48;color:#fff;font:bold 14px/24px 'Segoe UI',sans-serif;
            text-align:center;z-index:99999;pointer-events:none;
            box-shadow:0 0 0 3px #fff,0 2px 6px rgba(0,0,0,.35)}
        .shot-anno-box{position:fixed;border:2.5px dashed #e11d48;border-radius:8px;
            z-index:99998;pointer-events:none;
            box-shadow:0 0 0 2px rgba(255,255,255,.85),inset 0 0 0 1px rgba(255,255,255,.5)}
    """)
    return ctx, page


def shot(page, name, annos=None):
    """截图。annos: [{n: 序号, sel: 目标元素选择器}]，先画标注再截。"""
    if annos:
        page.evaluate(
            """annos => {
                document.querySelectorAll('.shot-anno,.shot-anno-box')
                    .forEach(e => e.remove());
                annos.forEach(a => {
                    const el = document.querySelector(a.sel);
                    if (!el) { console.warn('anno missing:', a.sel); return; }
                    const r = el.getBoundingClientRect();
                    const box = document.createElement('div');
                    box.className = 'shot-anno-box';
                    box.style.cssText = 'left:' + (r.left - 4) + 'px;top:'
                        + (r.top - 4) + 'px;width:' + (r.width + 8) + 'px;height:'
                        + (r.height + 8) + 'px;';
                    document.body.appendChild(box);
                    const c = document.createElement('div');
                    c.className = 'shot-anno';
                    c.textContent = a.n;
                    c.style.cssText = 'left:' + (r.left - 12) + 'px;top:'
                        + (r.top - 12) + 'px;';
                    document.body.appendChild(c);
                });
            }""",
            annos)
        page.wait_for_timeout(120)
    page.wait_for_timeout(300)   # 等界面稳定
    path = OUT / f"{name}.png"
    page.screenshot(path=str(path))
    print(f"  [ok] {name}.png")


def enter_main(page):
    """已装好 ADB 的启动路径：向导点「进入主界面」。"""
    page.goto(URL)
    page.wait_for_selector("#btn-env-enter", state="visible")
    page.click("#btn-env-enter")
    page.wait_for_selector("#env-overlay", state="hidden")
    page.wait_for_timeout(400)   # 等首次设备轮询完成


def connect_tv(page):
    """把主电视设为当前设备（走真实切换入口，产生真实反馈文案）。"""
    page.evaluate("refreshDevices(false)")
    page.wait_for_timeout(300)
    page.evaluate(f'onDeviceChanged("{MAIN_TV}")')
    page.wait_for_timeout(200)


def fire_live(page):
    """分三批推送实时日志，模拟真实滚动流。"""
    rows = live_payload()
    for i in range(0, len(rows), 9):
        page.evaluate(
            "rows => window.__fireMockEvent('live-log-lines', rows)",
            rows[i:i + 9])
        page.wait_for_timeout(120)


# ----------------------------------------------------------------------------
# 截图场景
# ----------------------------------------------------------------------------

def main():
    OUT.mkdir(parents=True, exist_ok=True)
    srv = start_server()
    print(f"静态服务: {URL}  (dist: {DIST})")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)

        # ---- 01 向导·未检测到 ADB（方式A：开始安装） ----
        ctx, page = new_page(browser, {"adbPath": ""})
        page.goto(URL)
        page.wait_for_selector("#btn-env-install", state="visible")
        shot(page, "01-env-step1-no-adb",
             annos=[{"n": "1", "sel": "#btn-env-install"}])

        # ---- 02 向导·本地安装失败 → 两种手动方式（方式B/C） ----
        page.evaluate("window.__mockState.installLocalFails = true")
        page.click("#btn-env-install")
        page.wait_for_selector("#btn-env-online", state="visible")
        shot(page, "02-env-install-options",
             annos=[{"n": "1", "sel": "#btn-env-local-zip"},
                    {"n": "2", "sel": "#btn-env-online"}])

        # ---- 03 向导·联网下载进度条（方式C 进行中） ----
        page.click("#btn-env-online")
        page.wait_for_timeout(500)
        shot(page, "03-env-online-progress")
        page.wait_for_selector("#btn-env-enter", state="visible")
        ctx.close()

        # ---- 04 向导·安装完成（③ 完成 → 进入主界面） ----
        ctx, page = new_page(browser, {"adbPath": ""})
        page.goto(URL)
        page.wait_for_selector("#btn-env-install", state="visible")
        page.click("#btn-env-install")
        page.wait_for_selector("#btn-env-enter", state="visible")
        shot(page, "04-env-step3-done",
             annos=[{"n": "1", "sel": "#btn-env-enter"}])
        ctx.close()

        # ---- 05 主界面总览（设备已连 + 应用已查 + 若干操作反馈） ----
        # 总览图标注 6 大区域（卡片级虚线框）
        base = {"adbPath": "D:\\ADB工具\\adb-tools\\platform-tools\\adb.exe",
                "devices": DEVICES, "devicesDetail": DEVICES_DETAIL,
                "packages": PACKAGES}
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.click("#btn-packages")
        page.wait_for_selector(".app-row")
        page.click("#btn-shot")          # 一键截图 → 反馈面板多一条结果
        page.click("#btn-refresh-devices")
        shot(page, "05-main-overview", annos=[
            {"n": "1", "sel": "#device-combo"},
            {"n": "2", "sel": "#seg-capture"},
            {"n": "3", "sel": "#seg-transfer"},
            {"n": "4", "sel": ".card-app .row"},
            {"n": "5", "sel": "#btn-start-logcat"},
            {"n": "6", "sel": "#panel-mode-switch"},
        ])

        # ---- 06 连接·输入 IP（未连接） ----
        page.fill("#device-combo-input", "")
        page.evaluate('onDeviceChanged("")')
        page.wait_for_timeout(200)
        page.fill("#device-combo-input", "172.31.15.4")
        shot(page, "06-connect-ip-input",
             annos=[{"n": "1", "sel": "#device-combo-input"},
                    {"n": "2", "sel": "#btn-connect"}])

        # ---- 07 连接·成功（复合框选中态 + 反馈） ----
        page.click("#btn-connect")
        page.wait_for_selector("#device-combo.has-device")
        page.wait_for_timeout(400)
        shot(page, "07-connect-success")

        # ---- 08 设备列表浮层展开（三种状态 + 连接方式徽标） ----
        page.click("#device-combo-toggle")
        page.wait_for_selector(".combo-pop", state="visible")
        page.wait_for_timeout(500)      # 等展开后的即时刷新
        shot(page, "08-device-pop-open",
             annos=[{"n": "1", "sel": "#device-combo-toggle"}])
        page.keyboard.press("Escape")
        page.click("body", position={"x": 600, "y": 120})
        ctx.close()

        # ---- 09 查看设备与历史（完整信息弹窗 + 历史区） ----
        ctx, page = new_page(browser, {**base, "recent": RECENT})
        enter_main(page)
        connect_tv(page)
        page.click("#btn-view-devices")
        page.wait_for_selector(".device-modal-row")
        page.wait_for_timeout(300)
        shot(page, "09-device-modal")
        page.click("#btn-device-modal-close")
        ctx.close()

        # ---- 10 屏幕采集·截图模式 + 结果反馈 ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.click("#btn-shot")
        page.wait_for_timeout(300)
        shot(page, "10-capture-shot",
             annos=[{"n": "1", "sel": "#seg-capture-shot"},
                    {"n": "2", "sel": "#btn-shot"}])
        ctx.close()

        # ---- 11 屏幕采集·录屏中（计时 00:42 + 抓取互斥置灰） ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.click("#seg-capture-rec")
        page.evaluate("window.__mockState.recState = "
                      "{recording: true, elapsed: 42, phase: 'recording'}")
        page.click("#btn-rec")
        page.wait_for_timeout(1400)     # 等轮询 tick 刷新计时显示
        shot(page, "11-capture-recording",
             annos=[{"n": "1", "sel": "#seg-capture-rec"},
                    {"n": "2", "sel": "#btn-rec"},
                    {"n": "3", "sel": "#rec-timer"}])
        ctx.close()

        # ---- 12 文件传输·拉取（已填路径） ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.fill("#pull-remote", "/sdcard/DCIM")
        page.evaluate("window.__mockState.pickDir = 'D:\\\\拉取保存'")
        page.click("#btn-pull-dir")
        page.wait_for_timeout(200)
        shot(page, "12-transfer-pull",
             annos=[{"n": "1", "sel": "#pull-remote"},
                    {"n": "2", "sel": "#btn-pull-dir"},
                    {"n": "3", "sel": "#btn-pull"}])
        ctx.close()

        # ---- 13 文件传输·推送（已选文件 + 目标路径） ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.click("#seg-transfer-push")
        page.evaluate("window.__mockState.pickFile = "
                      "'D:\\\\安装包\\\\云视听TV_v2.3.0.apk'")
        page.click("#btn-push-file")
        page.wait_for_timeout(200)
        page.fill("#push-remote", "/sdcard/Download/")
        shot(page, "13-transfer-push",
             annos=[{"n": "1", "sel": "#btn-push-file"},
                    {"n": "2", "sel": "#push-remote"},
                    {"n": "3", "sel": "#btn-push"}])
        ctx.close()

        # ---- 14 应用管理·查询结果（过滤 + 仅第三方 + 行按钮） ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.check("#pkg-third-only")
        page.click("#btn-packages")
        page.wait_for_selector(".app-row")
        shot(page, "14-apps-queried",
             annos=[{"n": "1", "sel": "#pkg-filter"},
                    {"n": "2", "sel": "#pkg-third-only"},
                    {"n": "3", "sel": "#btn-packages"}])

        # ---- 15 卸载二次确认弹窗 ----
        page.locator(".app-row").first.locator(
            "button[data-action='uninstall']").click()
        page.wait_for_selector("#confirm-modal", state="visible")
        shot(page, "15-uninstall-confirm")
        page.click("#confirm-cancel")
        ctx.close()

        # ---- 16 日志抓取·抓取中 ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.check("#log-clear-before")
        page.click("#btn-start-logcat")
        page.wait_for_timeout(300)
        shot(page, "16-logcat-running",
             annos=[{"n": "1", "sel": "#btn-start-logcat"},
                    {"n": "2", "sel": "#log-clear-before"},
                    {"n": "3", "sel": "#btn-stop-logcat"}])
        ctx.close()

        # ---- 17 底部面板·操作反馈模式 ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.click("#btn-refresh-devices")
        page.click("#btn-shot")
        page.click("#btn-packages")
        page.wait_for_selector(".app-row")
        shot(page, "17-panel-feedback",
             annos=[{"n": "1", "sel": "button[data-mode='feedback']"},
                    {"n": "2", "sel": "#cmd-output"}])
        ctx.close()

        # ---- 18 底部面板·命令行（adb shell 交互终端态） ----
        shell_base = {**base, "cmdOutputs": CMD_OUTPUTS,
                      "shellOutputs": SHELL_OUTPUTS}
        ctx, page = new_page(browser, shell_base)
        enter_main(page)
        connect_tv(page)
        page.click("button[data-mode='cmd']")
        page.wait_for_timeout(300)
        page.fill("#cmd-term-input", "adb devices")
        page.press("#cmd-term-input", "Enter")
        page.wait_for_timeout(200)
        page.fill("#cmd-term-input", "adb shell")
        page.press("#cmd-term-input", "Enter")
        page.wait_for_selector("text=已进入设备 shell")
        page.fill("#cmd-term-input", "ls /sdcard")
        page.press("#cmd-term-input", "Enter")
        page.wait_for_timeout(300)
        shot(page, "18-panel-cmd-shell",
             annos=[{"n": "1", "sel": "button[data-mode='cmd']"},
                    {"n": "2", "sel": "#cmd-term-input"}])
        ctx.close()

        # ---- 19 底部面板·实时日志（多级别着色 + 工具栏） ----
        ctx, page = new_page(browser, base)
        enter_main(page)
        connect_tv(page)
        page.click("button[data-mode='live']")
        page.wait_for_timeout(400)
        fire_live(page)
        shot(page, "19-panel-live-log",
             annos=[{"n": "1", "sel": "button[data-mode='live']"},
                    {"n": "2", "sel": "#live-level"},
                    {"n": "3", "sel": "#live-keyword"},
                    {"n": "4", "sel": "#btn-live-pause"}])
        ctx.close()

        browser.close()
    srv.shutdown()
    print("全部完成。")


if __name__ == "__main__":
    main()
