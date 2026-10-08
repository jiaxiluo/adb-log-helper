# -*- coding: utf-8 -*-
"""
文件名称 : make_pdfs.py
功    能 : 把两份手册 HTML 用 Edge/Chrome headless 打印成 PDF，输出到 docs/ 目录。
用    法 : python make_pdfs.py
说    明 : 界面更新后重出手册的三步之一（先跑 pc_shots.py / android_shots.py）。
             成功判定：本次输出文件 + 大小 > 500KB（含图）+ 页数与预期一致。
"""

import subprocess
import sys
import time
from pathlib import Path

sys.stdout.reconfigure(encoding="utf-8")

HERE = Path(__file__).resolve().parent
ASSETS = HERE.parent                      # manual-assets/
DOCS = ASSETS.parent                      # docs/

# 常见 Chrome/Edge 安装位置
CANDIDATES = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
]

# (源 HTML, 目标 PDF, 预期页数)
JOBS = [
    (ASSETS / "manual-pc-v1.9.html",
     DOCS / "操作手册-图解版-PC-V1.9.pdf", 9),
    (ASSETS / "manual-android-v1.0.0.html",
     DOCS / "操作手册-图解版-安卓-V1.0.0.pdf", 6),
]


def find_browser():
    for p in CANDIDATES:
        if Path(p).exists():
            return p
    raise SystemExit("未找到 Chrome/Edge，请把浏览器路径加入 make_pdfs.py CANDIDATES")


def count_pages(pdf):
    import pymupdf
    with pymupdf.open(pdf) as d:
        return len(d)


def main():
    browser = find_browser()
    print(f"浏览器: {browser}")
    ok_all = True
    for src, dst, expect in JOBS:
        if dst.exists():
            dst.unlink()                  # 删旧文件，防止误判旧产物
        url = "file:///" + str(src.resolve()).replace("\\", "/")
        cmd = [browser, "--headless", "--disable-gpu",
               "--no-pdf-header-footer",
               "--print-to-pdf=" + str(dst), url]
        print(f"生成: {dst.name} ...", flush=True)
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=180,
                           encoding="utf-8", errors="replace")

        # 校验：文件存在、够大（含图）、页数符合预期
        problems = []
        if not dst.exists():
            problems.append("文件未生成")
        else:
            kb = dst.stat().st_size // 1024
            if kb < 500:
                problems.append(f"仅 {kb}KB，疑似图片未加载")
            pages = count_pages(dst)
            if pages != expect:
                problems.append(f"页数 {pages} != 预期 {expect}")
        if problems:
            ok_all = False
            print(f"  [FAIL] {'; '.join(problems)}")
            tail = (r.stderr or "").strip().splitlines()[-3:]
            for line in tail:
                print("    " + line)
        else:
            print(f"  [ok] {dst.name}  ({dst.stat().st_size // 1024} KB, "
                  f"{count_pages(dst)} 页)")

    if not ok_all:
        sys.exit(1)
    print("全部完成。")


if __name__ == "__main__":
    main()
