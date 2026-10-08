# -*- coding: utf-8 -*-
"""
文件名称 : verify_pdfs.py
功    能 : 逐页渲染两份手册 PDF 为 PNG，供逐页人工/视觉校验
             （检查溢出、标注对齐、页码目录一致性）。
用    法 : python verify_pdfs.py   → 输出到 ../pdf-pages/pc/*.png 与 ../pdf-pages/android/*.png
"""

import subprocess
import sys
from pathlib import Path

sys.stdout.reconfigure(encoding="utf-8")

import pymupdf  # pip install pymupdf

HERE = Path(__file__).resolve().parent
ASSETS = HERE.parent
DOCS = ASSETS.parent
OUT = ASSETS / "pdf-pages"

JOBS = [
    (DOCS / "操作手册-图解版-PC-V1.9.pdf", "pc"),
    (DOCS / "操作手册-图解版-安卓-V1.0.0.pdf", "android"),
]


def main():
    OUT.mkdir(exist_ok=True)
    for pdf, tag in JOBS:
        outdir = OUT / tag
        outdir.mkdir(exist_ok=True)
        doc = pymupdf.open(pdf)
        print(f"{pdf.name}: {len(doc)} 页")
        for i, page in enumerate(doc, 1):
            pix = page.get_pixmap(dpi=72)
            pix.save(outdir / f"p{i:02d}.png")
        doc.close()
        print(f"  → {outdir}")
    print("完成。")


if __name__ == "__main__":
    main()
