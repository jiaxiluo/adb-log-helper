# -*- coding: utf-8 -*-
"""
文件名称 : android_shots.py
功    能 : 生成安卓版（ADB 助手 V1.0.0）图解操作手册所需的全部界面截图。
             驱动已评审的 UI 原型（ui-mockup-android-1.0.0.html，与实机界面一致），
             走完 连接 → 装APK → 抓日志 → 分享 全流程，按状态截取手机画面。
用    法 : python android_shots.py
输    出 : ../android-shots/a01-*.png ~ a10-*.png（手机外框整体，2 倍缩放）
说    明 : 仅用于生成文档截图，不属于产品代码。原型更新后重跑即可重出图。
"""

import sys
from pathlib import Path

from playwright.sync_api import sync_playwright

sys.stdout.reconfigure(encoding="utf-8")

HERE = Path(__file__).resolve().parent
MOCKUP = HERE.parents[1] / "ui-mockup-android-1.0.0.html"
OUT = HERE.parent / "android-shots"

TV_IP = "192.168.90.31"   # 现场演示主设备（与原型默认历史一致）


def add_anno_css(page):
    """标注层样式：绿色序号圆圈 + 虚线框（与安卓手册主题色一致）。"""
    page.add_style_tag(content="""
        .shot-anno{position:fixed;width:26px;height:26px;border-radius:50%;
            background:#059669;color:#fff;font:bold 15px/26px 'Segoe UI',sans-serif;
            text-align:center;z-index:99999;pointer-events:none;
            box-shadow:0 0 0 3px #fff,0 2px 6px rgba(0,0,0,.35)}
        .shot-anno-box{position:fixed;border:2.5px dashed #059669;border-radius:10px;
            z-index:99998;pointer-events:none;
            box-shadow:0 0 0 2px rgba(255,255,255,.85),inset 0 0 0 1px rgba(255,255,255,.5)}
    """)


def annotate(page, annos):
    """在目标元素上画序号圈（页面坐标系 → fixed 定位）。"""
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
                c.style.cssText = 'left:' + (r.left - 13) + 'px;top:'
                    + (r.top - 13) + 'px;';
                document.body.appendChild(c);
            });
        }""",
        annos)
    page.wait_for_timeout(120)


def shot_phone(page, name, scale=2, annos=None):
    """只截手机外框（含真实感边框），高清 2 倍；annos 同 PC 版。"""
    if annos:
        annotate(page, annos)
    page.wait_for_timeout(350)
    page.locator(".phone").screenshot(
        path=str(OUT / f"{name}.png"), scale="device")
    print(f"  [ok] {name}.png")


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(
            viewport={"width": 1100, "height": 1400},
            device_scale_factor=2)          # 手机框 400px 截出来 800px 宽
        page = ctx.new_page()
        page.goto(MOCKUP.as_uri())
        page.wait_for_load_state("networkidle")
        add_anno_css(page)

        # ---- a00 界面总览：三大卡片标注（手册"点哪是干嘛的"地图页用） ----
        shot_phone(page, "a00-overview",
                   annos=[{"n": "1", "sel": "#devCard"},
                          {"n": "2", "sel": "#instCard"},
                          {"n": "3", "sel": "#logCard"}])

        # ---- a01 初始态：未连接，装APK/抓日志置灰，历史 2 条 ----
        shot_phone(page, "a01-initial")

        # ---- a02 手输 IP，准备连接 ----
        page.fill("#ipInput", TV_IP)
        shot_phone(page, "a02-ip-input",
                   annos=[{"n": "1", "sel": "#ipInput"},
                          {"n": "2", "sel": "#devCard button.btn-primary"}])

        # ---- a03 连接成功：设备信息卡 + 两卡解锁 ----
        page.click("#devCard button.btn-primary")
        page.wait_for_selector(".dev-ok", timeout=4000)
        shot_phone(page, "a03-connected")

        # ---- a04 APK 文件选择面板（从微信/QQ 收到的包） ----
        page.click("#instCard button.btn-primary")
        page.wait_for_selector("#fileSheet.show")
        shot_phone(page, "a04-file-sheet",
                   annos=[{"n": "1", "sel": "#instCard button.btn-primary"}])
        page.click("#fileSheet >> text=iptv-release_v2.3.1.apk")
        page.wait_for_timeout(200)

        # ---- a05 已选文件，待安装 ----
        shot_phone(page, "a05-apk-selected",
                   annos=[{"n": "2", "sel": "#instCard"}])

        # ---- a06 安装成功（进度走完 + 结果卡） ----
        page.click("#instCard >> text=开始安装")
        page.wait_for_selector(".result.ok", timeout=8000)
        shot_phone(page, "a06-install-ok")

        # ---- a07 安装失败中文化（旧版本包演示） ----
        page.click("#instCard >> text=再装一个")
        page.click("#instCard button.btn-primary")
        page.wait_for_selector("#fileSheet.show")
        page.click("#fileSheet >> text=test_v1.0.apk")
        page.click("#instCard >> text=开始安装")
        page.wait_for_selector(".result.fail", timeout=8000)
        shot_phone(page, "a07-install-fail")

        # ---- a08 抓取中：红点 + 计时 + 后台保活提示 ----
        page.click("#logCard >> text=开始抓取")
        page.wait_for_selector("#runTime")
        page.wait_for_timeout(2500)          # 让计时走到 00:02~00:03
        shot_phone(page, "a08-logging",
                   annos=[{"n": "1", "sel": "#logCard"}])

        # ---- a09 结束并分享：分享面板 ----
        page.click("#logCard >> text=结束并分享")
        page.wait_for_selector("#shareSheet.show")
        shot_phone(page, "a09-share-sheet",
                   annos=[{"n": "2", "sel": "#shareSheet"}])

        # ---- a10 分享完成：日志文件卡（可再发/再抓） ----
        page.click(".share-item >> text=微信")
        page.wait_for_selector(".toast.show")
        page.wait_for_timeout(600)           # toast 显示中
        shot_phone(page, "a10-shared-done")

        browser.close()
    print("全部完成。")


if __name__ == "__main__":
    main()
