# manual-assets —— 图解操作手册制作素材

本目录存放 ADB 工具（PC 版 + 安卓版）**图解 PDF 操作手册的全部制作素材**。
PDF 产物在上级 `docs/` 目录：

| 产物 | 对应版本 |
|---|---|
| `../操作手册-图解版-PC-V1.9.pdf` | PC 桌面版 V1.9 |
| `../操作手册-图解版-安卓-V1.0.0.pdf` | 安卓版 V1.0.0 |

## 目录结构

```
manual-assets/
├── scripts/                     # 全部脚本（Python + Playwright + Chrome headless）
│   ├── wails_mock.js            # PC 版 Wails 绑定的 mock（驱动真实前端）
│   ├── pc_shots.py              # PC 版截图脚本（19 张，含红色序号标注）
│   ├── android_shots.py         # 安卓版截图脚本（10 张，驱动 UI 原型）
│   └── make_pdfs.py             # HTML → PDF（Chrome headless）
├── pc-shots/                    # PC 版界面截图（2 倍缩放，1180×880 视口）
└── android-shots/               # 安卓版截图（UI 原型手机框，2 倍缩放）
```

排版 HTML（生成 PDF 的源文件）：

- `../manual-assets/manual-pc-v1.9.html` —— PC 版手册
- `../manual-assets/manual-android-v1.0.0.html` —— 安卓版手册

## 界面更新后如何重新出册（三步）

```bash
cd docs/manual-assets/scripts

python pc_shots.py          # 1. 重出 PC 截图（加载 code/frontend/dist 真实前端 + mock 后端）
python android_shots.py     # 2. 重出安卓截图（驱动 ui-mockup-android-1.0.0.html 原型）
python make_pdfs.py         # 3. 重出两份 PDF
```

截图上红色序号标注（❶❷❸）由 `pc_shots.py` 里的 `annos=[{n, sel}]` 控制，
`sel` 是前端元素选择器——**界面改版后标注自动跟着元素走**，无需手调坐标。

## 依赖

- Python 3 + Playwright（`pip install playwright` + `playwright install chromium`）
- Chrome 或 Edge（make_pdfs.py 自动在常见路径查找）

## 注意

- `wails_mock.js` 只在截图时替换 Wails 绑定脚本，**不属于产品代码**，不参与构建。
- 截图数据（设备 IP、包名、日志样例等）均为演示用的虚构数据。
- 排版 HTML 里图片用相对路径 `../pc-shots/`、`../android-shots/` 引用，移动目录时保持相对结构。
