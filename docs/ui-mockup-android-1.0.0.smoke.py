# -*- coding: utf-8 -*-
"""ui-mockup-android-1.0.0.html 交互闭环自测（Playwright）

用法: python ui-mockup-android-1.0.0.smoke.py
覆盖: 置灰联动/连接(手输+历史)/安装成功/安装失败中文化/抓取计时/
      分享面板/抓取中断开保护/历史删除/重置
截图: 输出到 %TEMP%/adb-mockup-shots/ 供人工复核
"""
import pathlib
import sys
import tempfile

sys.stdout.reconfigure(encoding="utf-8")

from playwright.sync_api import sync_playwright

MOCKUP = pathlib.Path(__file__).with_name("ui-mockup-android-1.0.0.html").resolve()
SHOTS = pathlib.Path(tempfile.gettempdir()) / "adb-mockup-shots"
SHOTS.mkdir(exist_ok=True)

PASSED = 0
errors = []


def ok(name):
    global PASSED
    PASSED += 1
    print("  [PASS] %s" % name)


def assert_(cond, name):
    if not cond:
        print("  [FAIL] %s" % name)
        if errors:
            print("  页面报错: %s" % errors)
        sys.exit(1)
    ok(name)


def shot(page, name):
    page.screenshot(path=str(SHOTS / ("%s.png" % name)), clip={"x": 0, "y": 0, "width": 1080, "height": 1100})


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1080, "height": 1100})
        page.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
        page.on("pageerror", lambda e: errors.append(str(e)))
        page.goto(MOCKUP.as_uri())
        page.wait_for_load_state("networkidle")

        # --- 1. 初始态：未连接，安装/日志卡置灰 ---
        assert_(page.locator("#instCard.disabled").count() == 1, "初始: 安装卡置灰")
        assert_(page.locator("#logCard.disabled").count() == 1, "初始: 日志卡置灰")
        assert_(page.locator(".hist-row").count() == 2, "初始: 历史2条")
        shot(page, "01-initial")

        # --- 2. 手输 IP 连接 → 解锁 ---
        page.fill("#ipInput", "192.168.90.31")
        page.click("#devCard button.btn-primary")
        page.wait_for_selector(".dev-ok", timeout=4000)
        assert_("192.168.90.31" in page.locator(".dev-ok .ip").inner_text(), "连接: 显示IP")
        assert_(page.locator("#instCard.disabled").count() == 0, "连接后: 安装卡解锁")
        assert_(page.locator("#logCard.disabled").count() == 0, "连接后: 日志卡解锁")
        shot(page, "02-connected")
        # 断开后验证新 IP 已进历史（连接态下历史列表被设备信息卡替换，属正常设计）
        page.click("#devCard >> text=断开")
        page.wait_for_selector("#ipInput", timeout=3000)
        assert_(page.locator(".hist-row").count() == 3, "断开后: 历史3条(新IP入列)")
        assert_("192.168.90.31" in page.locator(".hist-row").first.inner_text(), "新IP排在历史首位")
        # 从历史重新连回，继续后续流程
        page.click("#devCard .hist-row:has-text('192.168.90.31')")
        page.wait_for_selector(".dev-ok", timeout=4000)

        # --- 3. 安装成功流程 ---
        page.click("#instCard button.btn-primary")
        page.wait_for_selector("#fileSheet.show")
        page.click("#fileSheet >> text=iptv-release_v2.3.1.apk")
        assert_(page.locator("text=开始安装").count() == 1, "选文件: 显示开始安装")
        page.click("#instCard >> text=开始安装")
        page.wait_for_selector(".result.ok", timeout=8000)
        assert_("安装成功" in page.locator(".result.ok").inner_text(), "安装: 成功结果")
        shot(page, "03-install-ok")

        # --- 4. 安装失败中文化（test 包） ---
        page.click("#instCard >> text=再装一个")
        page.click("#instCard button.btn-primary")
        page.wait_for_selector("#fileSheet.show")
        page.click("#fileSheet >> text=test_v1.0.apk")
        page.click("#instCard >> text=开始安装")
        page.wait_for_selector(".result.fail", timeout=8000)
        fail_txt = page.locator(".result.fail").inner_text()
        assert_("版本号比设备上的低" in fail_txt, "失败: 中文原因")
        assert_("INSTALL_FAILED_VERSION_DOWNGRADE" in fail_txt, "失败: 保留原始码")
        shot(page, "04-install-fail")

        # --- 5. 抓取计时 + 结束分享 ---
        page.click("#logCard >> text=开始抓取")
        page.wait_for_selector("#runTime")
        t1 = page.locator("#runTime").inner_text()
        page.wait_for_timeout(2300)
        t2 = page.locator("#runTime").inner_text()
        assert_(t1 != t2, "抓取: 计时在走 (%s→%s)" % (t1, t2))
        shot(page, "05-logging")
        page.click("#logCard >> text=结束并分享")
        page.wait_for_selector("#shareSheet.show", timeout=3000)
        shot(page, "06-share-sheet")
        page.click(".share-item >> text=微信")
        page.wait_for_selector(".toast.show")
        assert_("已拉起微信" in page.locator("#toast").inner_text(), "分享: 微信Toast")
        assert_(page.locator("text=adb_log_").count() >= 1, "结果: 日志文件卡")

        # --- 6. 再抓一份 → 抓取中断开设备（自动保护） ---
        page.click("#logCard >> text=再抓一份")
        page.wait_for_selector("#runTime")
        page.click("#devCard >> text=断开")
        page.wait_for_selector(".toast.show")
        assert_("自动停止" in page.locator("#toast").inner_text(), "断开: 抓取自动停止提示")
        assert_(page.locator("#instCard.disabled").count() == 1, "断开后: 安装卡重新置灰")
        assert_(page.locator("#logCard.disabled").count() == 1, "断开后: 日志卡重新置灰")

        # --- 7. 历史点击直连 + 删除 ---
        page.click("#devCard .hist-row:has-text(\"192.168.90.25\")")
        page.wait_for_selector(".dev-ok", timeout=4000)
        assert_("192.168.90.25" in page.locator(".dev-ok .ip").inner_text(), "历史: 点击直连")
        # 断开后才能看到历史列表，再验证删除
        page.click("#devCard >> text=断开")
        page.wait_for_selector("#ipInput", timeout=3000)
        page.locator(".hist-row .del").first.click()
        assert_(page.locator(".hist-row").count() == 2, "历史: 删除一条")

        # --- 8. 重置 ---
        page.click("#resetBtn")
        page.wait_for_timeout(300)
        assert_(page.locator("#instCard.disabled").count() == 1, "重置: 回到初始态")
        assert_(page.locator(".hist-row").count() == 2, "重置: 历史恢复")

        browser.close()

    if errors:
        print("  [FAIL] 控制台报错: %s" % errors)
        sys.exit(1)
    ok("无控制台报错")
    print("\n全部通过: %d 项" % PASSED)


if __name__ == "__main__":
    main()
