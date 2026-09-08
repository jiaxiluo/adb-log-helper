/* ============================================================================
   文件名称 : main.js
   功    能 : ADB 工具前台页面的交互逻辑。
             通过 Wails 生成的绑定 window.go.main.App 调用 Go 后端方法。
   设计思路: 面向非技术用户 ——
             1. 设备列表由后台自动刷新（3 秒轮询 + 窗口聚焦即刷新），
                下拉框展开即可看到最新设备，无需任何手动操作
             2. 「查看设备列表」弹窗展示完整信息（连接方式/状态/型号/安卓版本），
                支持直接选为当前设备、断开 TCP 设备
             3. 应用查询结果渲染为列表，每行直接挂「启动 / 强停 / 清缓存 / 卸载」按钮
             4. 日志相关三处入口：
                a) 一键抓取（日志卡片）：「开始 / 结束」两个动作，自动落文件
                b) 实时日志（底部面板模式之一，V1.8 迁入）：参考 Android Studio
                   Logcat，级别过滤（后端 logcat 原生）+ 关键字过滤（前端即时）
                   + 暂停/继续 + 滚动到最新（图标按钮，点亮=跟随）
                c) 命令行（底部面板模式之一，V1.8 新增）：本机 cmd 终端，
                   回车执行、↑↓ 翻历史、cd 持久化
             5. 底部面板三模式互斥切换（操作反馈/命令行/实时日志），
                清空按模式分发，收起为面板级折叠
             5. 所有操作反馈统一写入底部固定栏，带时间戳追加，始终可见
   ============================================================================ */

"use strict";

/* ---------------------------------------------------------------------------
 * 工具函数区
 * ------------------------------------------------------------------------ */

// 获取指定 id 的 DOM 元素（简写）
function $(id) {
    return document.getElementById(id);
}

// 操作反馈历史（数组，最新在末尾），用于底部固定栏渲染
const outputHistory = [];

// 反馈历史最大保留条数：超限丢弃最旧的，避免长时间使用内存无限增长
const MAX_OUTPUT_ITEMS = 200;

// 追加一条操作反馈到底部固定栏（带时间戳），并自动滚动到最新一条。
// 与旧的 setCmdOutput（覆盖式）不同：追加式让用户能看到最近多次操作的结果
function pushOutput(text) {
    const now = new Date();
    // 时间戳格式 HH:MM:SS
    const stamp = [now.getHours(), now.getMinutes(), now.getSeconds()]
        .map(function (n) { return (n < 10 ? "0" : "") + n; })
        .join(":");
    outputHistory.push("[" + stamp + "] " + text);
    if (outputHistory.length > MAX_OUTPUT_ITEMS) {
        outputHistory.shift(); // 丢弃最旧一条
    }

    const outEl = $("cmd-output");
    outEl.textContent = outputHistory.join("\n");
    // 自动滚动到底部，保持最新反馈可见
    outEl.scrollTop = outEl.scrollHeight;
}

// 清空操作反馈栏
function clearOutput() {
    outputHistory.length = 0;
    $("cmd-output").textContent = "";
}

// 获取当前选中的设备序列号；未选中时提示并返回空字符串
function requireDevice() {
    const serial = $("device-select").value;
    if (!serial) {
        pushOutput("⚠️ 请先在「设备连接」的下拉框中选择一台设备。");
        return "";
    }
    return serial;
}

// 统一调用 Go 绑定方法，捕获成功/失败，返回 { ok, result, err }
async function run(promise) {
    try {
        const result = await promise;
        return { ok: true, result: result };
    } catch (err) {
        // Wails 将 Go 返回的 error 以 Error 对象 reject，取其 message
        return { ok: false, err: err.message || String(err) };
    }
}

/* ---------------------------------------------------------------------------
 * 设备连接（后台自动刷新 + TCP 连接 / 断开 + 查看设备列表弹窗）
 *
 * 刷新机制说明（修复旧版"点击下拉框刷新"的缺陷）：
 * 旧版在下拉框 mousedown 时触发异步刷新，等 adb devices 返回后重建选项；
 * 但此时下拉弹窗往往已经展开 —— 修改选项会让弹窗立即收起（表现为
 * "点开就闪退、要点第二次才能选"），且清空选项的瞬间选中值丢失。
 * 现改为：
 *   1. 后台每 3 秒轮询一次（adb devices 开销小，桌面场景无压力）
 *   2. 窗口重新获得焦点时立即刷新（用户可能在窗口失焦期间插拔了设备）
 *   3. 列表内容（序列号+状态）与上次一致时完全不碰 DOM，
 *      从根本上避免"下拉弹窗被刷新关掉"与选中值被瞬时清空
 * ------------------------------------------------------------------------ */

// 设备列表缓存：最近一次成功获取到的设备列表。
// 用途：后台轮询时对比增删设备、弹窗「选为当前设备」时同步下拉框
let deviceCache = [];

// 下拉框最近一次渲染的列表签名（"serial|state" 以 ";" 拼接）。
// 签名相同 = 列表内容没变 = 跳过 DOM 重建
let deviceSelectSignature = "";

// 刷新并发保护：true 表示一次刷新正在进行中。
// 防止轮询、手动刷新、连接后刷新等多个来源交错执行导致下拉框闪烁
let deviceRefreshing = false;

// 后台自动轮询间隔（毫秒）
const DEVICE_POLL_INTERVAL = 3000;

// 生成设备列表的签名：内容相同则签名相同，用于判断是否需要重建下拉框。
// 前缀设备数量：空列表的签名应为 "0:" 而不是 ""，
// 否则会与初始值/错误态的 "" 相同，导致"从错误占位恢复为空列表"时误判为无变化
function deviceListSignature(devices) {
    return devices.length + ":"
        + devices.map(function (d) { return d.serial + "|" + d.state; }).join(";");
}

// 把设备列表渲染到下拉框。核心原则：内容没变不动 DOM。
// 入参: devices 设备数组（[{serial, state}, ...]）
// 返回: selectionLost —— 原选中的设备是否已从列表中消失（调用方据此做联动清理）
function renderDeviceSelect(devices) {
    const select = $("device-select");
    const signature = deviceListSignature(devices);
    if (signature === deviceSelectSignature) {
        return false; // 列表无变化：不重建，避免打断用户操作下拉框
    }
    deviceSelectSignature = signature;

    const previous = select.value; // 记录当前选中项，重建后尽量恢复
    select.innerHTML = "";

    // 判断原选中项是否仍在列表中
    const stillThere = previous !== ""
        && devices.some(function (d) { return d.serial === previous; });

    // 占位符出现的两种情况（都保持"未选中"状态）：
    // 1) 没有任何设备；2) 原选中设备已断开（避免静默跳到别的设备造成误操作）
    let placeholderText = "";
    if (devices.length === 0) {
        placeholderText = "（暂无设备，请确认已连接）";
    } else if (!stillThere && previous !== "") {
        placeholderText = "（原选中设备已断开，请重新选择）";
    }
    if (placeholderText !== "") {
        const ph = document.createElement("option");
        ph.value = "";
        ph.textContent = placeholderText;
        select.appendChild(ph);
    }

    // 逐台设备生成下拉选项
    devices.forEach(function (dev) {
        const opt = document.createElement("option");
        opt.value = dev.serial;
        opt.textContent = dev.serial + "（" + dev.state + "）";
        select.appendChild(opt);
    });

    // 恢复选中状态：
    // - 原选中仍在 → 恢复；
    // - 原选中消失 → 保持未选中（值为 ""，后续操作会提示先选设备）；
    // - 从未选过   → 浏览器默认选中第一台（首次加载的便捷行为）
    if (stillThere) {
        select.value = previous;
        return false;
    }
    if (previous !== "") {
        select.value = "";
        return true; // 选中丢失，需要调用方做联动清理
    }
    return false;
}

// 对比刷新前后的设备列表，把接入/断开的设备输出到底部反馈栏。
// 仅后台轮询使用：列表没变时静默，有设备增删时才提示，避免刷屏
function reportDeviceChanges(before, after) {
    const beforeSet = new Set(before.map(function (d) { return d.serial; }));
    const afterSet = new Set(after.map(function (d) { return d.serial; }));

    before.forEach(function (d) {
        if (!afterSet.has(d.serial)) {
            pushOutput("ℹ️ 设备已断开：" + d.serial);
        }
    });
    after.forEach(function (d) {
        if (!beforeSet.has(d.serial)) {
            pushOutput("ℹ️ 设备已接入：" + d.serial + "（" + d.state + "）");
        }
    });
}

// 刷新设备列表（对应 adb devices）并更新下拉框。
// 入参: userInitiated true = 用户手动触发（按钮/连接后），刷新结果反馈到底部栏；
//                    false = 后台轮询/焦点触发，仅在有设备增删时提示
async function refreshDevices(userInitiated) {
    if (deviceRefreshing) {
        return; // 已有刷新在进行，跳过本次（避免并发交错）
    }
    deviceRefreshing = true;
    try {
        const res = await run(window.go.main.App.GetDevices());
        if (!res.ok) {
            // 获取失败：保留上一次的好数据不打扰用户（后台瞬时失败常见且无害）；
            // 手动刷新才提示错误。仅在界面还没有任何列表数据时展示错误占位
            if (userInitiated) {
                pushOutput("❌ 获取设备列表失败：" + res.err);
            }
            if (deviceSelectSignature === "") {
                const select = $("device-select");
                select.innerHTML = "";
                const opt = document.createElement("option");
                opt.value = "";
                opt.textContent = "（获取设备失败，请点击「刷新设备」重试）";
                select.appendChild(opt);
            }
            return;
        }

        const devices = res.result || [];
        const before = deviceCache;
        const selectionLost = renderDeviceSelect(devices);
        deviceCache = devices;

        // 选中设备已断开：联动清理（停日志抓取、清应用列表），保证界面状态一致。
        // 正常情况下 change 事件不会对程序化改动触发，必须在这里显式处理
        if (selectionLost) {
            await resetDeviceDependentUI();
            pushOutput("⚠️ 原选中的设备已断开，请重新选择目标设备。");
        }

        if (userInitiated) {
            pushOutput("✅ 设备列表已刷新，共 " + devices.length + " 台。");
        } else {
            // 后台轮询：仅在有设备接入/断开时提示
            reportDeviceChanges(before, devices);
        }
    } finally {
        deviceRefreshing = false;
    }
}

// 启动设备列表的自动刷新：立即刷一次 + 周期轮询 + 窗口聚焦即刷新。
// 在环境向导结束进入主界面时调用（ADB 就绪后才有意义）
function startDevicePolling() {
    refreshDevices(false);

    // 周期轮询：保持下拉框始终最新，用户展开即可直接选择
    setInterval(function () {
        refreshDevices(false);
    }, DEVICE_POLL_INTERVAL);

    // 窗口重新获得焦点时立即刷新：
    // 用户最常见的操作路径是"切出去插/拔设备 → 切回工具"，聚焦刷新让列表即时生效
    window.addEventListener("focus", function () {
        refreshDevices(false);
    });
}

// 组装连接地址：IP 必填；端口留空时由 Go 侧补默认 5555
function buildConnectAddress() {
    const ip = $("connect-ip").value.trim();
    if (!ip) {
        pushOutput("⚠️ 请输入设备 IP 地址。");
        return "";
    }
    const port = $("connect-port").value.trim();
    // 端口留空 → 只传 IP，Go 侧自动补 5555；填了则拼 "ip:port"
    return port ? ip + ":" + port : ip;
}

// 连接 TCP 设备：adb connect <ip:port>，成功后自动刷新设备列表
async function doConnect() {
    const address = buildConnectAddress();
    if (!address) return;

    pushOutput("正在连接设备：" + address + " …");
    const res = await run(window.go.main.App.Connect(address));
    if (!res.ok) {
        pushOutput("❌ 连接失败：" + res.err);
        return;
    }
    // 无论新连接还是已连接，都刷新列表让设备出现在下拉框
    await refreshDevices(true);
    pushOutput("✅ " + res.result);
}

/* ---------------------------------------------------------------------------
 * 「查看设备列表」弹窗：完整展示每台设备的序列号 / 连接方式 / 状态 /
 * 型号 / 品牌 / 安卓版本，并支持直接「选为当前设备」「断开 TCP 设备」。
 * 数据来自后端 GetDevicesDetail（含 getprop 属性查询，打开时取一次）
 * ------------------------------------------------------------------------ */

// 弹窗最近一次拿到的设备详情（「选为当前设备」时用于同步下拉框）
let deviceModalCache = [];

// 状态文本映射：把 adb 的英文状态翻译为用户可读的中文说明
const DEVICE_STATE_TEXT = {
    device: "可用",
    offline: "离线",
    unauthorized: "未授权（请在设备上允许 USB 调试）"
};

// 取设备状态对应的中文说明（未收录的状态原样展示）
function deviceStateText(state) {
    return DEVICE_STATE_TEXT[state] || state;
}

// 取设备状态对应的指示灯 CSS 类（绿=可用 / 灰=离线 / 黄=未授权或其他）
function deviceDotClass(state) {
    if (state === "device") {
        return "device-dot device-dot-ok";
    }
    if (state === "offline") {
        return "device-dot device-dot-off";
    }
    return "device-dot device-dot-warn";
}

// 打开「查看设备列表」弹窗并加载数据
async function openDeviceModal() {
    $("device-modal").style.display = "flex";
    await loadDeviceModal();
}

// 关闭弹窗
function closeDeviceModal() {
    $("device-modal").style.display = "none";
}

// 拉取设备详情并渲染到弹窗（打开弹窗与弹窗内「刷新」按钮共用）
async function loadDeviceModal() {
    const statusEl = $("device-modal-status");
    const listEl = $("device-modal-list");

    statusEl.textContent = "正在获取设备信息（含设备属性查询，约 1~2 秒）…";
    listEl.innerHTML = "";

    // 历史区与当前设备列表互不影响：无论下方走到哪个分支都加载
    // （无设备/获取失败时历史记录仍有参考价值）
    loadDeviceHistory();

    const res = await run(window.go.main.App.GetDevicesDetail());
    if (!res.ok) {
        statusEl.textContent = "❌ 获取设备列表失败：" + res.err;
        return;
    }

    const devices = res.result || [];
    deviceModalCache = devices;
    if (devices.length === 0) {
        statusEl.textContent = "当前没有已连接的设备。USB 设备插上即可；TCP 设备请先在上方输入 IP 连接。";
        return;
    }
    statusEl.textContent = "共 " + devices.length
        + " 台设备（型号/品牌/安卓为「未知」或「-」表示设备不可用或查询超时）";
    renderDeviceModalList(devices);
}

// 拉取并渲染历史设备区（最近连接过但当前已断开的设备，最多 3 条）。
// 历史记录展示失败不影响上方已渲染的当前设备列表
async function loadDeviceHistory() {
    const titleEl = $("device-history-title");
    const listEl = $("device-history-list");
    listEl.innerHTML = "";

    const res = await run(window.go.main.App.GetRecentDevices());
    const entries = res.ok ? (res.result || []) : [];

    if (entries.length === 0) {
        titleEl.style.display = "none";
        return;
    }
    titleEl.style.display = "block";
    entries.forEach(function (entry) {
        const row = document.createElement("div");
        row.className = "device-modal-row device-history-row";

        // 灰色状态灯：历史设备均为已断开状态
        const dot = document.createElement("span");
        dot.className = "device-dot device-dot-off";
        dot.title = "已断开";
        row.appendChild(dot);

        // 左列：序列号 + 断开时间
        const mainEl = document.createElement("div");
        mainEl.className = "device-main";
        const serialEl = document.createElement("div");
        serialEl.className = "device-serial";
        serialEl.textContent = entry.serial;
        serialEl.title = entry.serial;
        mainEl.appendChild(serialEl);
        const stateEl = document.createElement("div");
        stateEl.className = "device-state";
        stateEl.textContent = "已断开 · " + formatHistoryTime(entry.disconnectedAt);
        mainEl.appendChild(stateEl);
        row.appendChild(mainEl);

        // TCP 设备给一键重连按钮（USB 设备重新插上即出现在当前列表）
        if (entry.serial.indexOf(":") >= 0) {
            const btns = document.createElement("div");
            btns.className = "device-actions";
            const btn = document.createElement("button");
            btn.className = "btn btn-sm btn-primary";
            btn.textContent = "重新连接";
            btn.dataset.action = "reconnect";
            btn.dataset.serial = entry.serial;
            btns.appendChild(btn);
            row.appendChild(btns);
        }

        listEl.appendChild(row);
    });
}

// 格式化历史时间：把后端 RFC3339 时间转为 "MM-dd HH:MM" 展示。
// 解析失败时返回 "-"（历史时间仅作参考，不值得报错打断用户）
function formatHistoryTime(iso) {
    const d = new Date(iso);
    if (isNaN(d.getTime())) {
        return "-";
    }
    const p = function (n) { return (n < 10 ? "0" : "") + n; };
    return (d.getMonth() + 1) + "-" + p(d.getDate()) + " "
        + p(d.getHours()) + ":" + p(d.getMinutes());
}

// 渲染弹窗内的设备列表：每行 = 状态灯 + 序列号 + 连接方式 + 属性 + 操作按钮。
// 按钮通过 data-* 携带序列号，点击统一由 onDeviceModalClick 分发
function renderDeviceModalList(devices) {
    const listEl = $("device-modal-list");
    listEl.innerHTML = "";

    devices.forEach(function (dev) {
        const row = document.createElement("div");
        row.className = "device-modal-row";

        // 状态指示灯（绿/灰/黄）
        const dot = document.createElement("span");
        dot.className = deviceDotClass(dev.state);
        dot.title = dev.state;
        row.appendChild(dot);

        // 左列：序列号（等宽字体）+ 状态中文说明
        const mainEl = document.createElement("div");
        mainEl.className = "device-main";
        const serialEl = document.createElement("div");
        serialEl.className = "device-serial";
        serialEl.textContent = dev.serial;
        serialEl.title = dev.serial; // 悬停显示完整序列号
        mainEl.appendChild(serialEl);
        const stateEl = document.createElement("div");
        stateEl.className = "device-state";
        stateEl.textContent = deviceStateText(dev.state);
        mainEl.appendChild(stateEl);
        row.appendChild(mainEl);

        // 连接方式徽标（USB / TCP / 模拟器）
        const tp = document.createElement("span");
        tp.className = "device-transport";
        tp.textContent = dev.transport;
        row.appendChild(tp);

        // 属性列：型号 / 品牌 / 安卓版本 三行带标签展示。
        // 标签用更暗的样式弱化，值为主视觉；缺失/超时字段由后端给占位符
        const infoEl = document.createElement("div");
        infoEl.className = "device-info";
        const propDefs = [
            { label: "型号", value: dev.model },
            { label: "品牌", value: dev.brand },
            { label: "安卓", value: dev.version }
        ];
        propDefs.forEach(function (p) {
            // 行容器不产生盒子（display:contents），标签/值直接成为
            // .device-info 网格的两个单元格 —— 这是"按 key 对齐"的关键：
            // 标签列宽度自动取最宽标签，所有值从同一列起点左对齐
            const line = document.createElement("div");
            line.className = "device-prop";
            const labelEl = document.createElement("span");
            labelEl.className = "device-prop-label";
            labelEl.textContent = p.label + "：";
            line.appendChild(labelEl);
            const valueEl = document.createElement("span");
            valueEl.className = "device-prop-value";
            valueEl.textContent = p.value;
            valueEl.title = p.value; // 悬停可见被截断的完整值
            line.appendChild(valueEl);
            infoEl.appendChild(line);
        });
        row.appendChild(infoEl);

        // 操作按钮：选为当前设备（仅可用状态）；断开（仅 TCP 设备）
        const btns = document.createElement("div");
        btns.className = "device-actions";
        if (dev.state === "device") {
            const btnPick = document.createElement("button");
            btnPick.className = "btn btn-sm btn-primary";
            btnPick.textContent = "选为当前设备";
            btnPick.dataset.action = "pick";
            btnPick.dataset.serial = dev.serial;
            btns.appendChild(btnPick);
        }
        if (dev.transport === "TCP") {
            const btnDisc = document.createElement("button");
            btnDisc.className = "btn btn-sm";
            btnDisc.textContent = "断开";
            btnDisc.dataset.action = "disconnect";
            btnDisc.dataset.serial = dev.serial;
            btns.appendChild(btnDisc);
        }
        row.appendChild(btns);

        listEl.appendChild(row);
    });
}

// 弹窗列表行内按钮点击分发（事件委托）
async function onDeviceModalClick(event) {
    const action = event.target.dataset.action;
    if (!action) {
        return; // 点的不是按钮
    }
    const serial = event.target.dataset.serial;

    if (action === "pick") {
        pickDeviceFromModal(serial);
    } else if (action === "disconnect") {
        await disconnectFromModal(serial);
    } else if (action === "reconnect") {
        await reconnectFromHistory(serial);
    }
}

// 从历史区重连一台 TCP 设备（adb connect），成功后刷新弹窗与主界面下拉框
async function reconnectFromHistory(serial) {
    pushOutput("正在重新连接设备：" + serial + " …");
    const res = await run(window.go.main.App.Connect(serial));
    pushOutput(res.ok ? "✅ " + res.result : "❌ 连接失败：" + res.err);
    await loadDeviceModal();
    await refreshDevices(false);
}

// 从弹窗把某台设备设为当前目标设备：
// 用弹窗刚取到的数据同步下拉框 → 选中该设备 → 触发与手动切换相同的联动逻辑
function pickDeviceFromModal(serial) {
    // 先把弹窗数据渲染进下拉框（若列表有变化会重建，没有变化则跳过）
    renderDeviceSelect(deviceModalCache.map(function (d) {
        return { serial: d.serial, state: d.state };
    }));
    deviceCache = deviceModalCache.map(function (d) {
        return { serial: d.serial, state: d.state };
    });

    // 选中目标设备。若瞬间设备又掉线导致选项不存在，明确提示
    const select = $("device-select");
    select.value = serial;
    if (select.value !== serial) {
        pushOutput("⚠️ 设备 " + serial + " 已不可用，请刷新后重试。");
        return;
    }

    closeDeviceModal();
    // 复用手动切换设备的联动逻辑（停日志抓取、清应用列表 + 提示）
    onDeviceChanged();
}

// 从弹窗断开一台 TCP 设备，然后刷新弹窗列表与主界面下拉框
async function disconnectFromModal(serial) {
    pushOutput("正在断开设备：" + serial + " …");
    const res = await run(window.go.main.App.Disconnect(serial));
    if (!res.ok) {
        pushOutput("❌ 断开失败：" + res.err);
    } else {
        pushOutput("✅ " + res.result);
    }
    // 同时刷新弹窗列表与主界面下拉框
    await loadDeviceModal();
    await refreshDevices(false);
}

// （说明：buildConnectAddress / doConnect / doDisconnect 此处曾有一份
//  与上文完全重复的定义（旧版遗留，后者覆盖前者），已清理，仅保留上文一份）

/* ---------------------------------------------------------------------------
 * 应用管理（查询 → 列表渲染 → 行内操作）
 * ------------------------------------------------------------------------ */

// 查询设备上的应用列表（可按关键字过滤、可选仅第三方应用），渲染到应用列表区
async function queryApps() {
    const serial = requireDevice();
    if (!serial) return;
    const filter = $("pkg-filter").value.trim();
    const thirdOnly = $("pkg-third-only").checked; // 勾选 = 仅第三方应用（排除系统预装）

    const statusEl = $("app-list-status");
    statusEl.textContent = "正在查询应用列表…";
    $("app-list").innerHTML = "";

    const res = await run(window.go.main.App.ListPackages(serial, filter, thirdOnly));
    if (!res.ok) {
        statusEl.textContent = "查询失败，结果见下方操作反馈。";
        pushOutput("❌ 查询应用失败：" + res.err);
        return;
    }

    const pkgs = res.result;
    if (pkgs.length === 0) {
        statusEl.textContent = filter
            ? "未找到匹配「" + filter + "」的应用。"
            : "设备上没有应用。";
        return;
    }

    // 状态行标注查询范围（全部/仅第三方）与过滤条件
    statusEl.textContent = "共 " + pkgs.length + " 个应用（范围："
        + (thirdOnly ? "仅第三方" : "全部") + ")"
        + (filter ? "，过滤：「" + filter + "」" : "")
        + "。每行右侧：启动 / 强停 / 清缓存 / 卸载。";
    renderAppList(pkgs);
}

// 渲染应用列表：每行 = 包名 + [启动] [强停] [清缓存] [卸载] 按钮。
// 按钮通过 data-* 属性携带包名，点击统一由 onAppListClick 分发
function renderAppList(pkgs) {
    const listEl = $("app-list");
    listEl.innerHTML = "";

    pkgs.forEach(function (pkg) {
        const row = document.createElement("div");
        row.className = "app-row";

        // 包名文本
        const nameEl = document.createElement("span");
        nameEl.className = "app-pkg";
        nameEl.textContent = pkg;
        nameEl.title = pkg; // 悬停显示完整包名（长包名可能被截断）
        row.appendChild(nameEl);

        // 按钮容器
        const btns = document.createElement("div");
        btns.className = "app-actions";

        // 按钮统一定义：文案 / 样式类 / 分发动作名
        // 启动=绿（正向操作），强停/清缓存=白（中性），卸载=红（危险）
        const buttonDefs = [
            { text: "启动", cls: "btn btn-sm btn-success", action: "start" },
            { text: "强停", cls: "btn btn-sm", action: "forcestop" },
            { text: "清缓存", cls: "btn btn-sm", action: "clear" },
            { text: "卸载", cls: "btn btn-sm btn-danger", action: "uninstall" }
        ];
        buttonDefs.forEach(function (def) {
            const btn = document.createElement("button");
            btn.className = def.cls;
            btn.textContent = def.text;
            btn.dataset.action = def.action;
            btn.dataset.pkg = pkg;
            btns.appendChild(btn);
        });

        row.appendChild(btns);
        listEl.appendChild(row);
    });
}

// 应用列表行内按钮点击分发（事件委托：列表只挂一个监听器）
async function onAppListClick(event) {
    const action = event.target.dataset.action;
    if (!action) {
        return; // 点击的不是操作按钮
    }

    const serial = requireDevice();
    if (!serial) return;
    const pkg = event.target.dataset.pkg;

    if (action === "start") {
        await startApp(serial, pkg);
    } else if (action === "forcestop") {
        await forceStopApp(serial, pkg);
    } else if (action === "clear") {
        await clearAppCache(serial, pkg);
    } else if (action === "uninstall") {
        await uninstallApp(serial, pkg);
    }
}

// 启动应用：把应用拉起到前台（等价于在设备上点了一下桌面图标）。
// 后端优先 am start，旧系统自动回退 monkey 拉起，无需二次确认（非破坏性操作）
async function startApp(serial, pkg) {
    pushOutput("正在启动 " + pkg + " …");
    const res = await run(window.go.main.App.StartApp(serial, pkg));
    pushOutput(res.ok
        ? "✅ 启动 " + pkg + "：" + res.result
        : "❌ 启动 " + pkg + " 失败：" + res.err);
}

// 强制停止应用：立即杀掉应用进程（am force-stop）。
// 未保存的运行中状态会丢失，但磁盘数据不受影响——属于可恢复操作，不弹二次确认
async function forceStopApp(serial, pkg) {
    pushOutput("正在强制停止 " + pkg + " …");
    const res = await run(window.go.main.App.ForceStop(serial, pkg));
    pushOutput(res.ok
        ? "✅ 已强制停止 " + pkg + (res.result ? "：" + res.result : "（进程已结束）")
        : "❌ 停止 " + pkg + " 失败：" + res.err);
}

// 清理应用数据：adb shell pm clear <pkg>（不可逆，自定义弹窗二次确认）
async function clearAppCache(serial, pkg) {
    const confirmed = await showConfirm(
        "清理应用数据",
        "确认对 [" + pkg + "] 执行清理？清理会清除该应用的「全部数据」（含缓存、登录账号、数据库、设置），相当于「清除存储」，且不可恢复。",
        "确认清理"
    );
    if (!confirmed) {
        pushOutput("已取消清理 " + pkg);
        return;
    }

    pushOutput("正在清理 " + pkg + " …");
    const res = await run(window.go.main.App.ClearCache(serial, pkg));
    pushOutput(res.ok ? "✅ 清理 " + pkg + "：" + res.result : "❌ 清理 " + pkg + " 失败：" + res.err);
}

// 卸载应用：adb uninstall <pkg>（不可恢复，自定义弹窗二次确认）
async function uninstallApp(serial, pkg) {
    const confirmed = await showConfirm(
        "卸载应用",
        "确认从设备卸载 [" + pkg + "]？卸载会连同该应用的全部数据一起删除，且不可恢复。如不确定包名，可先重新查询应用列表核对。",
        "确认卸载"
    );
    if (!confirmed) {
        pushOutput("已取消卸载 " + pkg);
        return;
    }

    pushOutput("正在卸载 " + pkg + " …");
    const res = await run(window.go.main.App.Uninstall(serial, pkg));
    pushOutput(res.ok ? "✅ 卸载 " + pkg + "：" + res.result : "❌ 卸载 " + pkg + " 失败：" + res.err);
}

/* ---------------------------------------------------------------------------
 * 自定义确认弹窗（危险操作二次确认，替代原生 window.confirm）
 * 返回 Promise<boolean>：确认=true，取消/关闭=false
 * ------------------------------------------------------------------------ */

function showConfirm(title, message, okText) {
    return new Promise(function (resolve) {
        $("confirm-title").textContent = title;
        $("confirm-message").textContent = message;
        $("confirm-ok").textContent = okText || "确认执行";
        $("confirm-modal").style.display = "flex";

        // 一次性监听器：弹窗关闭后立即解绑，避免多次打开后叠加触发
        function finish(result) {
            $("confirm-modal").style.display = "none";
            $("confirm-ok").removeEventListener("click", onOk);
            $("confirm-cancel").removeEventListener("click", onCancel);
            resolve(result);
        }
        function onOk() { finish(true); }
        function onCancel() { finish(false); }

        $("confirm-ok").addEventListener("click", onOk);
        $("confirm-cancel").addEventListener("click", onCancel);
    });
}

/* ---------------------------------------------------------------------------
 * 文件与设备操作（截图 / 安装 APK / pull / push）
 * ------------------------------------------------------------------------ */

// 截图：adb exec-out screencap -p。
// 目录输入框留空时走后端默认（程序目录下 screenshots/），填了则保存到所选目录
async function doScreenshot() {
    const serial = requireDevice();
    if (!serial) return;
    const dir = $("shot-dir").value.trim(); // 空字符串 = 默认目录

    pushOutput("正在截图…");
    const res = await run(window.go.main.App.Screenshot(serial, dir));
    pushOutput(res.ok ? "✅ 截图已保存：" + res.result : "❌ 截图失败：" + res.err);
}

// 安装 APK：adb install -r <apk>
async function doInstall() {
    const serial = requireDevice();
    if (!serial) return;
    const apk = $("apk-path").value.trim();
    if (!apk) {
        pushOutput("⚠️ 请选择或填写 APK 文件路径。");
        return;
    }

    pushOutput("正在安装 APK：" + apk + " …");
    const res = await run(window.go.main.App.InstallAPK(serial, apk));
    pushOutput(res.ok ? "✅ 安装完成：" + res.result : "❌ 安装失败：" + res.err);
}

// pull 拉取：adb pull <remote> <local>
async function doPull() {
    const serial = requireDevice();
    if (!serial) return;
    const remote = $("pull-remote").value.trim();
    const local = $("pull-dir").value.trim();
    if (!remote || !local) {
        pushOutput("⚠️ 请填写设备上的路径并选择本机保存目录。");
        return;
    }

    pushOutput("正在拉取：" + remote + " → " + local + " …");
    const res = await run(window.go.main.App.Pull(serial, remote, local));
    pushOutput(res.ok ? "✅ 拉取完成：" + res.result : "❌ 拉取失败：" + res.err);
}

// push 推送：adb push <local> <remote>
async function doPush() {
    const serial = requireDevice();
    if (!serial) return;
    const local = $("push-file").value.trim();
    const remote = $("push-remote").value.trim();
    if (!local || !remote) {
        pushOutput("⚠️ 请选择本机文件并填写设备上的目标路径。");
        return;
    }

    pushOutput("正在推送：" + local + " → " + remote + " …");
    const res = await run(window.go.main.App.Push(serial, local, remote));
    pushOutput(res.ok ? "✅ 推送完成：" + res.result : "❌ 推送失败：" + res.err);
}

/* ---------------------------------------------------------------------------
 * 文件 / 目录选择（调用 Go 侧的系统对话框）
 * ------------------------------------------------------------------------ */

// 选择 APK 文件，回填到 apk-path 输入框
async function pickApkFile() {
    const res = await run(window.go.main.App.SelectFile());
    if (res.ok && res.result) {
        $("apk-path").value = res.result;
    }
}

// 选择截图保存目录，回填到 shot-dir 输入框（留空 = 默认 screenshots/）
async function pickShotDir() {
    const res = await run(window.go.main.App.SelectDirectory());
    if (res.ok && res.result) {
        $("shot-dir").value = res.result;
    }
}

// 选择 pull 本地目标目录
async function pickPullDir() {
    const res = await run(window.go.main.App.SelectDirectory());
    if (res.ok && res.result) {
        $("pull-dir").value = res.result;
    }
}

// 选择 push 本地文件
async function pickPushFile() {
    const res = await run(window.go.main.App.SelectFile());
    if (res.ok && res.result) {
        $("push-file").value = res.result;
    }
}

// 选择日志保存目录（留空 = 默认程序目录下 logs/）
async function pickLogDir() {
    const res = await run(window.go.main.App.SelectDirectory());
    if (res.ok && res.result) {
        $("log-dir").value = res.result;
    }
}

/* ---------------------------------------------------------------------------
 * 日志抓取（一键开始 / 结束，无实时终端）
 * ------------------------------------------------------------------------ */

// 开始抓取：调用后端 StartLogcat，界面只更新状态行与反馈，不展示日志内容。
// 勾选「抓取前清空设备日志缓冲」时后端先执行 adb logcat -c，
// 本次抓取只记录新产生的日志
async function doStartLogcat() {
    const serial = requireDevice();
    if (!serial) return;
    const dir = $("log-dir").value.trim(); // 留空时后端使用默认 logs/ 目录
    const clearBefore = $("log-clear-before").checked; // 可选：先清空设备端日志缓冲

    $("logcat-status").textContent = "当前状态：正在启动…";
    const res = await run(window.go.main.App.StartLogcat(serial, dir, clearBefore));
    if (!res.ok) {
        $("logcat-status").textContent = "当前状态：启动失败";
        pushOutput("❌ 日志抓取启动失败：" + res.err);
        return;
    }
    // 后端返回本次日志文件的完整路径，展示给用户便于定位
    $("logcat-status").textContent = "当前状态：抓取中（设备 " + serial + "）";
    pushOutput("✅ 日志抓取已开始，文件：" + res.result
        + (clearBefore ? "（已先清空设备日志缓冲）" : ""));
}

// 结束抓取：StopLogcat 幂等，直接调用即可
async function doStopLogcat() {
    await window.go.main.App.StopLogcat();
    $("logcat-status").textContent = "当前状态：未抓取";
    pushOutput("✅ 日志抓取已结束，文件已保存。");
}

/* ---------------------------------------------------------------------------
 * 实时日志（V1.6 引入，V1.8 迁移到底部面板的模式之一）
 * 入口：底部面板「实时日志」模式按钮（旧版日志卡片上的开关已移除）。
 * 日志经 "live-log-lines" 事件批量推送（后端 200ms/200行聚合）。
 * 前端三级性能设计：
 *   1. 缓冲数组环形上限 5000 行（超出丢最旧，内存恒定）
 *   2. 暂停时不做任何 DOM 更新，仅入缓冲；恢复时全量补齐
 *   3. 渲染用 DocumentFragment 批量插入 + rAF 合帧，避免逐行回流
 * 交互细节（对齐 AS Logcat 习惯）：
 *   - 级别过滤走后端（logcat 原生 filter，切换 = 重启会话）
 *   - 关键字过滤走前端（即时，200ms 防抖全量重渲）
 *   - 用户向上滚离底部自动停止跟随，滚回底部自动恢复
 *   - 进入实时日志模式即强制开启"滚动到最新"（打开就看到最新打印流）
 * ------------------------------------------------------------------------ */

// 实时日志缓冲（最新在末尾）。上限环形：超出丢最旧
let liveLogLines = [];

// ---------- 上限与性能参数（V1.7 性能修复） ----------
// 内存缓冲上限：供关键字过滤全量重建与暂停补齐用（数据层，不在 DOM 里）
const LIVE_MAX_LINES = 5000;
// DOM 展示上限：视图内最多保留的行数（原版 5000 行 DOM 是"打开即卡死"的
// 根因之一——2.5 万节点全量布局；浏览器控制台同类方案也只保留约 1000 条）
const LIVE_DOM_MAX = 1000;
// DOM 裁剪滞回目标：超过 LIVE_DOM_MAX 时一次性砍到该值。
// 滞回（1000→800）避免每批日志都触发 removeChild 来回抖动
const LIVE_TRIM_TO = 800;
// 单条消息 DOM 渲染截断阈值：超长日志（堆栈/大 JSON）整行渲染成几十视觉行，
// 截断后只显示前 N 字符 + 提示（内存里保留全文，不影响过滤）
const LIVE_MSG_MAX = 2000;

// 关键字过滤缓存（原版每行过滤都读 input.value，高频批次下是纯浪费；
// 输入事件时更新本变量，过滤函数只读变量）
let liveKeyword = "";

// 暂停标记：true = 后台继续接收进缓冲，但不渲染界面
let livePaused = false;

// 自动滚动跟随标记（与「自动滚动」勾选框、滚动位置双向同步）
let liveFollow = true;

// rAF 合帧渲染状态：待渲染行暂存 + 是否已排帧。
// 多批日志在两帧之间到达时合并为一次 DOM 更新（渲染频率 ≤ 帧率）
let liveRenderPending = null;
let liveRafScheduled = false;

// 包名过滤当前值（输入框回车时更新并重启会话；V1.9，参考 AS package 过滤）
let livePkg = "";

// 更新实时日志工具栏右侧的状态文字
function setLiveStatus(text) {
    $("live-status").textContent = text;
}

// 判断一行日志是否通过当前关键字过滤（匹配 Tag 或正文，不区分大小写）。
// 只读缓存的 liveKeyword 变量——每行都去查 input.value 是高频批次的隐性开销
function liveLineMatches(line) {
    if (!liveKeyword) {
        return true; // 无关键字：全部通过
    }
    const tag = (line.tag || "").toLowerCase();
    const msg = (line.message || "").toLowerCase();
    return tag.indexOf(liveKeyword) >= 0 || msg.indexOf(liveKeyword) >= 0;
}

// 构造单条日志行的 DOM 元素（时间 / 级别徽标 / Tag(PID) / 正文）。
// 级别 class（log-v ~ log-f、log-q 无级别）在 CSS 中按 AS 配色定义
function buildLogLineEl(line) {
    const row = document.createElement("div");
    // 级别 class：V/D/I/W/E/F → log-v ~ log-f；
    // 空级别与后端的 "?"（非标准行，如 "--------- beginning of main"）
    // 统一映射 log-q 暗灰样式（直接拼会得到未定义的 "log-?"，压测发现的真 bug）
    const lv = (line.level && line.level !== "?") ? line.level.toLowerCase() : "q";
    row.className = "log-line log-" + lv;

    // 时间戳（弱化灰色）
    const timeEl = document.createElement("span");
    timeEl.className = "log-time";
    timeEl.textContent = line.time || "";
    row.appendChild(timeEl);

    // 级别徽标（单字符，着色由级别 class 控制）；无级别行显示 "·" 而不是 "?"
    const lvEl = document.createElement("span");
    lvEl.className = "log-level";
    lvEl.textContent = (line.level && line.level !== "?") ? line.level : "·";
    row.appendChild(lvEl);

    // Tag（浅蓝色，带 PID；长 Tag 截断，悬停可见全名）
    const tagEl = document.createElement("span");
    tagEl.className = "log-tag";
    const tagText = line.tag ? line.tag + (line.pid ? "(" + line.pid + ")" : "") : "";
    tagEl.textContent = tagText;
    tagEl.title = tagText;
    row.appendChild(tagEl);

    // 正文（可换行，着色由级别 class 控制）。
    // 超长消息截断：整行渲染几十视觉行是布局灾难（配合 content-visibility
    // 仍在视口内时昂贵）；截断只影响展示，过滤仍用内存中的全文
    const msgEl = document.createElement("span");
    msgEl.className = "log-msg";
    const fullMsg = line.message || "";
    if (fullMsg.length > LIVE_MSG_MAX) {
        msgEl.textContent = fullMsg.slice(0, LIVE_MSG_MAX)
            + " …（已截断，完整长度 " + fullMsg.length + " 字符）";
    } else {
        msgEl.textContent = fullMsg;
    }
    row.appendChild(msgEl);

    return row;
}

// 同步「滚动到最新」按钮的点亮/熄灭状态（视觉即功能状态）
function setLiveFollowButton(active) {
    const btn = $("btn-live-follow");
    if (btn) {
        btn.classList.toggle("live-follow-active", active);
    }
}

// 把日志视图滚到真正的最底部。
// 双保险策略（V1.8）：先 scrollTop 赋超大值交给浏览器钳制（免读 scrollHeight），
// 再对最后一行 scrollIntoView —— content-visibility 的视口外行高度是估算值，
// 纯 scrollTop 钳制可能停在"估算最大"而非真实底部；scrollIntoView 强制
// 定位到最后一行真实位置，保证打开实时日志即刻看到最新打印流
function scrollLiveToBottom() {
    const view = $("live-log-view");
    view.scrollTop = 1e9;
    if (view.lastElementChild) {
        view.lastElementChild.scrollIntoView({ block: "end" });
    }
}

// 全量重建日志视图（清空 / 关键字变化 / 暂停恢复时使用）。
// 只重建缓冲中"最新的 LIVE_DOM_MAX 行"（DOM 上限与增量渲染保持一致——
// 重建出 5000 行再立刻裁掉 4000 行毫无意义），一次性 Fragment 追加
function renderLiveLogFull() {
    // 全量重建意味着 pending 队列作废（缓冲已包含其全部内容），
    // 不清会导致下一帧把已重建过的行重复追加（脏数据闪回）
    liveRenderPending = null;
    const view = $("live-log-view");
    const start = Math.max(0, liveLogLines.length - LIVE_DOM_MAX);
    const frag = document.createDocumentFragment();
    for (let i = start; i < liveLogLines.length; i++) {
        if (liveLineMatches(liveLogLines[i])) {
            frag.appendChild(buildLogLineEl(liveLogLines[i]));
        }
    }
    view.innerHTML = "";
    view.appendChild(frag);
    if (liveFollow) {
        scrollLiveToBottom();
    }
}

// 裁剪视图头部多余的 DOM 行（滞回批量：超过 LIVE_DOM_MAX 一次砍到 LIVE_TRIM_TO）。
// 原版逐行 while + removeChild 且上限 5000，高频批次下每批都在删行；
// 滞回后 200 行才触发一次批量删除，删除量固定 200，开销恒定
function trimLiveDom() {
    const view = $("live-log-view");
    const n = view.children.length;
    if (n <= LIVE_DOM_MAX) {
        return;
    }
    const removeCount = n - LIVE_TRIM_TO;
    for (let i = 0; i < removeCount; i++) {
        view.removeChild(view.firstChild);
    }
}

// 接收一批日志（后端 "live-log-lines" 事件，lines 为结构化数组）。
// V1.7 性能修复后的处理链：
//   1. 全量入内存缓冲（环形 5000）—— 数据永不因渲染策略丢行
//   2. 暂停中：完全不碰 DOM，仅更新计数
//   3. 运行中：过滤通过的行先积攒到 liveRenderPending，
//      由 requestAnimationFrame 在下一帧统一渲染（合帧）——
//      两帧之间到达的多批日志合并为一次 DOM 更新，
//      渲染频率被钳制在帧率内，事件再密集也不会连续布局打满主线程
function onLiveLines(lines) {
    if (!Array.isArray(lines) || lines.length === 0) {
        return;
    }
    // 逐行入缓冲（不用 push(...lines) 展开，避免超大数组调用栈风险）
    for (let i = 0; i < lines.length; i++) {
        liveLogLines.push(lines[i]);
    }
    // 缓冲环形裁剪：超出上限丢最旧（splice 一次移除，避免循环 shift 低效）
    if (liveLogLines.length > LIVE_MAX_LINES) {
        liveLogLines.splice(0, liveLogLines.length - LIVE_MAX_LINES);
    }

    // 暂停中：不碰 DOM，仅更新状态计数（恢复时全量补齐）
    if (livePaused) {
        setLiveStatus("已暂停 · 缓冲 " + liveLogLines.length + " 行");
        return;
    }

    // 过滤通过的行进入待渲染队列（合帧）
    if (!liveRenderPending) {
        liveRenderPending = [];
    }
    for (let i = 0; i < lines.length; i++) {
        if (liveLineMatches(lines[i])) {
            liveRenderPending.push(lines[i]);
        }
    }
    scheduleLiveRender();
}

// 安排下一帧渲染（同一帧内多次到达只排一次）
function scheduleLiveRender() {
    if (liveRafScheduled) {
        return;
    }
    liveRafScheduled = true;
    requestAnimationFrame(flushLiveRender);
}

// 帧回调：把积攒的待渲染行一次性追加进视图。
// DOM 操作集中在一帧内：Fragment 批量构建 → 一次 appendChild →
// 滞回裁剪 → scrollTop 直接赋超大值（免读 scrollHeight 的强制布局）
function flushLiveRender() {
    liveRafScheduled = false;
    const pending = liveRenderPending;
    liveRenderPending = null;
    if (!pending || pending.length === 0) {
        return;
    }

    const view = $("live-log-view");
    const frag = document.createDocumentFragment();
    for (let i = 0; i < pending.length; i++) {
        frag.appendChild(buildLogLineEl(pending[i]));
    }
    view.appendChild(frag);
    trimLiveDom();
    if (liveFollow) {
        scrollLiveToBottom(); // scrollTop 钳制 + 最后一行 scrollIntoView 双保险
    }
    setLiveStatus(liveStatusRunning());
}

// 会话结束处理（后端 "live-log-ended" 事件：用户停止 / 设备断开 / adb 退出）。
// 不强制切走模式：保留 live 视图与已收到的日志供回看，
// 状态行明示结束原因；再次进入实时日志模式会重新启动会话
function onLiveEnded(ev) {
    const reason = ev && ev.reason ? ev.reason : "未知原因";
    setLiveStatus("已结束：" + reason + "（内容可回看，重进此模式重新开始）");
    // 仅在实时日志模式可见时提示（切走模式触发的停止不再刷反馈）
    if (panelMode === "live") {
        pushOutput("ℹ️ 实时日志已结束：" + reason + "（面板内容仍可回看）");
    }
}

// 启动实时日志会话（进入实时日志模式 / 切级别 / 改包名过滤 复用同一入口）。
// 每次启动都强制开启"滚动到最新"——打开即看到最新打印流（V1.8 需求），
// 不继承上次翻看历史时留下的熄灭状态。
// 包名过滤（V1.9，参考 Android Studio 的 package 过滤）：后端把包名解析成
// pid 后以 --pid 过滤，只看该应用的日志；应用未运行会返回友好错误
async function startLiveSession() {
    const serial = requireDevice();
    if (!serial) {
        return false; // 无设备：由调用方负责退回操作反馈模式
    }

    // 复位会话状态：清缓冲、清待渲染队列、清视图、解除暂停、强制跟随最新
    liveLogLines = [];
    liveRenderPending = null;
    livePaused = false;
    liveFollow = true;
    setLiveFollowButton(true);
    $("live-log-view").innerHTML = "";
    $("btn-live-pause").textContent = "暂停";
    setLiveStatus("启动中…");

    const level = $("live-level").value;
    const pkg = livePkg.trim(); // 包名过滤（空 = 不过滤全部日志）
    const res = await run(window.go.main.App.StartLiveLog(serial, level, pkg));
    if (!res.ok) {
        setLiveStatus("启动失败");
        pushOutput("❌ 实时日志启动失败：" + res.err);
        return false;
    }
    // 状态行带上包名过滤标记，让过滤生效与否一目了然
    setLiveStatus(pkg ? "运行中 · 包 " + pkg + " · 0 行" : "运行中 · 0 行");
    pushOutput("✅ 实时日志已开始（设备 " + serial + "，最低级别 " + level
        + (pkg ? "，包名过滤 " + pkg : "") + "）");
    return true;
}

// 停止实时日志会话（切出实时日志模式时调用；幂等）
async function stopLiveSession() {
    await window.go.main.App.StopLiveLog();
    setLiveStatus("未启动");
}

// 运行中状态行的统一文案（带包名过滤标记）
function liveStatusRunning() {
    return livePkg.trim()
        ? "运行中 · 包 " + livePkg.trim() + " · " + liveLogLines.length + " 行"
        : "运行中 · 已接收 " + liveLogLines.length + " 行";
}

// 级别下拉切换：后端 logcat 原生过滤需重启会话生效（后端先停旧会话，幂等）。
// 缓冲清空（新级别下旧行语义不一致），保持当前模式
async function onLiveLevelChange() {
    if (panelMode !== "live") {
        return; // 不在实时日志模式：仅记住选择，下次进入生效
    }
    const level = $("live-level").value;
    // 复用统一启动入口（内部含清缓冲/复位状态/带包名过滤）
    const ok = await startLiveSession();
    if (ok) {
        pushOutput("ℹ️ 实时日志级别已切换为 " + level + "（日志已重新开始）");
    }
}

// 包名过滤应用（输入框回车触发，V1.9）：
// 非空 = 只看该应用日志（后端解析 pid 过滤）；清空回车 = 取消过滤恢复全量。
// 应用未运行时后端返回友好错误，会话保持停止——修正输入或清空后重试
async function onLivePkgApply() {
    if (panelMode !== "live") {
        livePkg = $("live-pkg").value; // 不在实时日志模式：记住输入，下次进入生效
        return;
    }
    livePkg = $("live-pkg").value;
    const ok = await startLiveSession();
    if (ok) {
        pushOutput(livePkg.trim()
            ? "ℹ️ 实时日志已按包名过滤：" + livePkg.trim() + "（仅显示该应用日志）"
            : "ℹ️ 实时日志包名过滤已取消（恢复全量日志）");
    }
}

// 暂停 / 继续：暂停时后台继续缓冲不渲染；继续时全量补齐展示
function onLivePauseToggle() {
    livePaused = !livePaused;
    $("btn-live-pause").textContent = livePaused ? "继续" : "暂停";
    if (!livePaused) {
        renderLiveLogFull(); // 恢复：把暂停期间缓冲的内容全部补上
    }
}

// 清空：缓冲、待渲染队列与视图一起清（会话继续运行，后续日志继续追加）
function onLiveClear() {
    liveLogLines = [];
    liveRenderPending = null; // 不清会被下一帧 rAF 把待渲染行重新加回来
    $("live-log-view").innerHTML = "";
    setLiveStatus(livePaused ? "已暂停 · 0 行" : "运行中 · 0 行");
}

/* ---------------------------------------------------------------------------
 * 底部面板三模式（V1.8：操作反馈 / 命令行 / 实时日志）
 * 面板标题左侧分段按钮互斥切换；「清空」按当前模式清对应内容；
 * 「收起」为面板级折叠不受模式影响。
 * 排他规则（用户需求）：选择操作反馈或命令行时，实时日志会话自动停止；
 * 操作反馈数据始终后台积累（切回即可见），命令行输出与历史保留。
 * ------------------------------------------------------------------------ */

// 当前面板模式：feedback（操作反馈，默认）/ cmd（命令行）/ live（实时日志）
let panelMode = "feedback";

// 面板模式切换主入口（分段按钮点击调用）。
// 切出 live 停会话；切入 live 需要设备（无设备提示并退回 feedback）；
// 切入 cmd 聚焦输入框（立即可以敲命令）
async function setPanelMode(mode) {
    if (mode === panelMode) {
        return; // 重复点击同一模式：无操作
    }

    // 离开实时日志模式：停止会话（缓冲与视图保留，重进时重新开始）
    if (panelMode === "live" && mode !== "live") {
        await stopLiveSession();
        pushOutput("ℹ️ 实时日志已停止（切换到"
            + (mode === "cmd" ? "命令行" : "操作反馈") + "模式）。");
    }

    panelMode = mode;

    // ---- 分段按钮高亮 & 内容区互斥显示 ----
    document.querySelectorAll("#panel-mode-switch .mode-btn").forEach(function (btn) {
        btn.classList.toggle("mode-btn-active", btn.dataset.mode === mode);
    });
    $("cmd-output").style.display = mode === "feedback" ? "block" : "none";
    $("cmd-terminal").style.display = mode === "cmd" ? "flex" : "none";
    $("live-log-panel").style.display = mode === "live" ? "flex" : "none";

    // ---- 进入模式的联动 ----
    if (mode === "cmd") {
        // 命令行：需要大窗口 → 默认档时自动升到最大；刷新提示符并聚焦输入框
        autoExpandPanelForMode();
        await updateTermPrompt();
        $("cmd-term-input").focus();
    } else if (mode === "live") {
        // 实时日志：需要大窗口 → 默认档时自动升到最大；
        // 需要设备；无设备退回操作反馈模式
        autoExpandPanelForMode();
        const serial = $("device-select").value;
        if (!serial) {
            pushOutput("⚠️ 实时日志需要先在「设备连接」中选择一台设备。");
            await setPanelMode("feedback");
            return;
        }
        const ok = await startLiveSession();
        if (!ok) {
            await setPanelMode("feedback"); // 启动失败（含无设备）：退回
            return;
        }
    }
}

/* ---------------------------------------------------------------------------
 * 命令行模式（本机 cmd 终端）
 * 行为对齐真实终端：回显输入命令 → 输出结果；cd 持久化（后端会话目录）；
 * ↑/↓ 翻命令历史。输出上限 200 条防无限增长。
 * ------------------------------------------------------------------------ */

// 命令行输出历史（含回显行与结果行，最新在末尾），上限环形
let cmdTermLines = [];
const CMD_TERM_MAX_LINES = 200;

// 命令历史（仅用户输入过的命令）与浏览位置（↑↓ 翻历史用）
let cmdHistory = [];
const CMD_HISTORY_MAX = 50;
let cmdHistIdx = -1; // -1 = 不在历史浏览态（正在输入新命令）

// 追加一行到命令行输出并渲染（自动滚到底部）
function appendCmdTermLine(text) {
    cmdTermLines.push(text);
    if (cmdTermLines.length > CMD_TERM_MAX_LINES) {
        cmdTermLines.shift(); // 环形丢弃最旧
    }
    const out = $("cmd-term-out");
    out.textContent = cmdTermLines.join("\n");
    out.scrollTop = out.scrollHeight; // 命令行输出量小，直接滚底即可
}

// 更新命令提示符（显示后端会话的当前目录，形如 "C:\path>"）
async function updateTermPrompt() {
    const res = await run(window.go.main.App.GetCmdCwd());
    if (res.ok && res.result) {
        $("cmd-term-prompt").textContent = res.result + ">";
    }
}

// 执行一条命令：回显 → 调后端 RunCmd → 输出结果 → 刷新提示符（cd 可能改目录）
async function execCmdTerm(command) {
    const trimmed = command.trim();
    if (!trimmed) {
        return; // 空命令不执行（也不进历史）
    }

    // 进历史（去重：与上一条相同则不重复记录）
    if (cmdHistory[cmdHistory.length - 1] !== trimmed) {
        cmdHistory.push(trimmed);
        if (cmdHistory.length > CMD_HISTORY_MAX) {
            cmdHistory.shift();
        }
    }
    cmdHistIdx = -1; // 退出历史浏览态

    // 回显：提示符 + 命令（与真实终端一致）
    appendCmdTermLine($("cmd-term-prompt").textContent + " " + trimmed);

    const res = await run(window.go.main.App.RunCmd(trimmed));
    if (!res.ok) {
        appendCmdTermLine("❌ 命令启动失败：" + res.err);
        return;
    }
    // 输出（可能为空，如 cd 成功只回显新目录）
    if (res.result) {
        appendCmdTermLine(res.result.replace(/\r\n$/, ""));
    }
    // cd 可能改变了会话目录：刷新提示符
    await updateTermPrompt();
}

// 命令输入框按键处理：Enter 执行并清空输入；↑/↓ 翻历史
async function onCmdInputKey(e) {
    const input = $("cmd-term-input");
    if (e.key === "Enter") {
        const command = input.value;
        input.value = "";
        await execCmdTerm(command);
        return; // 执行完保持焦点，继续敲下一条
    }
    if (e.key === "ArrowUp") {
        // 向上翻更早的历史；无历史则忽略
        if (cmdHistory.length === 0) {
            return;
        }
        if (cmdHistIdx === -1) {
            cmdHistIdx = cmdHistory.length - 1; // 从最新一条开始
        } else if (cmdHistIdx > 0) {
            cmdHistIdx--;
        }
        input.value = cmdHistory[cmdHistIdx];
        e.preventDefault();
        return;
    }
    if (e.key === "ArrowDown") {
        // 向下翻更近的历史；翻到底退出浏览态（清空输入）
        if (cmdHistIdx === -1) {
            return;
        }
        if (cmdHistIdx < cmdHistory.length - 1) {
            cmdHistIdx++;
            input.value = cmdHistory[cmdHistIdx];
        } else {
            cmdHistIdx = -1;
            input.value = "";
        }
        e.preventDefault();
    }
}

// 清空命令行输出（命令历史保留——清屏不清历史，与终端习惯一致）
function clearCmdTerm() {
    cmdTermLines = [];
    $("cmd-term-out").textContent = "";
}

// 「清空」按钮按当前模式分发（操作反馈 / 命令行输出 / 实时日志）
function onClearCurrentMode() {
    if (panelMode === "feedback") {
        clearOutput();
    } else if (panelMode === "cmd") {
        clearCmdTerm();
    } else {
        onLiveClear();
    }
}

/* ---------------------------------------------------------------------------
 * ADB 环境准备向导（三步：检测 → 安装 → 完成）
 * 每一步都由用户点击按钮显式推进，不自动跳过，
 * 确保用户清楚看到每一步的成功/失败结果。
 * ------------------------------------------------------------------------ */

// 向导结束时拿到的 ADB 路径（点击「进入主界面」时使用）
let envReadyPath = "";

// 更新 ADB 状态徽标
function updateAdbBadge(path) {
    const badge = $("adb-status");
    if (path) {
        badge.textContent = "ADB 就绪";
        badge.className = "badge badge-ok";
    } else {
        badge.textContent = "ADB 未就绪";
        badge.className = "badge badge-error";
    }
}

// 设置向导步骤指示器状态。
// current=当前进行中的步骤号(1~3)，doneUpTo=已完成到的步骤号(0 表示还没完成任何一步)
function envSetSteps(current, doneUpTo) {
    [1, 2, 3].forEach(function (i) {
        const el = $("env-step-" + i);
        let cls = "env-step";
        if (i <= doneUpTo) {
            cls += " env-step-done";     // 已完成：绿色
        } else if (i === current) {
            cls += " env-step-active";   // 当前步：蓝色高亮
        }
        el.className = cls;
    });
}

// 显示/隐藏向导按钮区中的按钮。
// ids: 要显示的按钮 id 列表；其余按钮全部隐藏。安装选项出现时同步显示说明文字。
function envShowButtons(ids) {
    ["btn-env-install", "btn-env-local-zip", "btn-env-online", "btn-env-enter"].forEach(function (id) {
        $(id).style.display = ids.indexOf(id) >= 0 ? "inline-block" : "none";
    });
    // 手动安装选项出现时才展示两种方式的说明
    $("env-hint").style.display = ids.indexOf("btn-env-local-zip") >= 0 ? "block" : "none";
}

// 第 ① 步：检测本机 ADB 环境（结果停留在此页，由用户选择下一步）
async function runEnvCheck() {
    $("env-overlay").style.display = "flex";
    envSetSteps(1, 0);
    $("env-status").textContent = "正在检测本机 ADB 环境…";
    $("env-detail").textContent = "";
    envShowButtons([]);

    let path = "";
    try {
        path = await window.go.main.App.RecheckAdb();
    } catch (err) {
        path = ""; // 检测异常按未安装处理
    }

    if (path) {
        // 检测通过：① ③ 两步直接完成（无需安装），等用户点击进入主界面
        envSetSteps(3, 2);
        $("env-status").textContent = "✅ 已检测到本机 ADB，环境就绪。";
        $("env-detail").textContent = "ADB 路径：" + path;
        envReadyPath = path;
        envShowButtons(["btn-env-enter"]);
    } else {
        // 未安装：停留在此页，等用户点击「开始安装」
        envSetSteps(1, 0);
        $("env-status").textContent = "❌ 未检测到 ADB，需要安装后才能使用本工具。";
        $("env-detail").textContent = "点击「开始安装」继续：将优先使用程序同目录的安装包（无需联网）。";
        envShowButtons(["btn-env-install"]);
    }
}

// 第 ② 步：安装 ADB（先自动尝试本地压缩包，失败再让用户选手动方式）
async function envStartInstall() {
    envSetSteps(2, 1);
    $("env-status").textContent = "正在尝试从程序同目录的安装包安装（无需联网）…";
    $("env-detail").textContent = "";
    envShowButtons([]);

    const res = await run(window.go.main.App.InstallAdbLocal(""));
    if (res.ok) {
        envFinished("✅ ADB 安装完成（使用本地安装包）。", res.result);
        return;
    }

    // 本地自动安装不可用：停留在此页，给出两种手动方式
    $("env-status").textContent = "❌ 本地自动安装不可用：" + res.err;
    $("env-detail").textContent = "请选择下方任一方式手动安装。";
    envShowButtons(["btn-env-local-zip", "btn-env-online"]);
}

// 第 ③ 步：安装成功，等用户点击「进入主界面」
function envFinished(msg, path) {
    envSetSteps(3, 2);
    $("env-status").textContent = msg;
    $("env-detail").textContent = "ADB 路径：" + path + "（已自动配置到系统 PATH）";
    envReadyPath = path;
    envShowButtons(["btn-env-enter"]);
}

// 用户点击「进入主界面」：关闭向导，进入主界面。
// 此时 ADB 已就绪，启动设备列表的自动刷新（轮询 + 聚焦刷新）
function envEnterMain() {
    $("env-overlay").style.display = "none";
    updateAdbBadge(envReadyPath);
    startDevicePolling();
    pushOutput("✅ ADB 环境就绪：" + envReadyPath);
}

// 手动选择本地压缩包安装
async function envPickLocalZip() {
    const zipRes = await run(window.go.main.App.SelectZipFile());
    if (!zipRes.ok || !zipRes.result) {
        return; // 用户取消了选择，保持当前页
    }

    $("env-status").textContent = "正在从所选压缩包安装…";
    envShowButtons([]);
    const res = await run(window.go.main.App.InstallAdbLocal(zipRes.result));
    if (res.ok) {
        envFinished("✅ ADB 安装完成（使用所选压缩包）。", res.result);
    } else {
        // 失败：停留在安装步骤，允许换一种方式或重试
        $("env-status").textContent = "❌ 安装失败：" + res.err;
        $("env-detail").textContent = "请换另一种方式或重试。";
        envShowButtons(["btn-env-local-zip", "btn-env-online"]);
    }
}

// 联网下载安装（进度条实时更新）
async function envInstallOnline() {
    $("env-status").textContent = "正在联网下载 ADB 安装包…";
    $("env-detail").textContent = "";
    envShowButtons([]);
    $("env-progress-wrap").style.display = "block";
    $("env-progress-bar").style.width = "0%";

    const res = await run(window.go.main.App.InstallAdbOnline());
    $("env-progress-wrap").style.display = "none";
    if (res.ok) {
        envFinished("✅ ADB 联网下载并安装完成。", res.result);
    } else {
        // 失败：停留在安装步骤，允许改用本地压缩包或重试
        $("env-status").textContent = "❌ 联网安装失败：" + res.err;
        $("env-detail").textContent = "请检查网络后重试，或改用本地压缩包安装。";
        envShowButtons(["btn-env-local-zip", "btn-env-online"]);
    }
}

// 把字节数格式化为可读文本（如 5.3 MB）
function formatBytes(n) {
    if (n >= 1024 * 1024) {
        return (n / 1024 / 1024).toFixed(1) + " MB";
    }
    if (n >= 1024) {
        return (n / 1024).toFixed(0) + " KB";
    }
    return n + " B";
}

/* ---------------------------------------------------------------------------
 * 设备切换联动：切换目标设备（或选中设备断开）时，
 * 所有依赖设备的数据刷新或恢复默认
 * ------------------------------------------------------------------------ */

// 清空所有"属于某台设备"的界面数据：停止日志抓取、清空应用列表与过滤条件。
// 两个调用场景：
//   1. 用户切换目标设备（onDeviceChanged）
//   2. 后台轮询发现原选中设备已断开（refreshDevices 的 selectionLost 分支）
async function resetDeviceDependentUI() {
    // 1. 停止旧设备上的日志抓取会话（StopLogcat 幂等，未在抓取时调用无害）
    await window.go.main.App.StopLogcat();
    $("logcat-status").textContent = "当前状态：未抓取";

    // 2. 停止实时日志：若正处于实时日志模式则退回操作反馈模式
    //    （setPanelMode 内部会停会话并复位界面；其他模式下会话本就未运行，
    //     显式停一次做兜底——幂等无害）
    if (panelMode === "live") {
        await setPanelMode("feedback");
    } else {
        await window.go.main.App.StopLiveLog();
    }

    // 3. 清空应用列表、过滤条件与查询范围勾选（应用列表属于旧设备的数据）
    $("app-list").innerHTML = "";
    $("app-list-status").textContent = "尚未查询。选择设备后点击「查询应用」。";
    $("pkg-filter").value = "";
    $("pkg-third-only").checked = false;
}

async function onDeviceChanged() {
    const serial = $("device-select").value;
    if (!serial) {
        return; // 空选项（如"暂无设备"）不触发
    }

    // 清理旧设备的关联数据，保证界面状态与当前选中设备一致
    await resetDeviceDependentUI();

    // 反馈提示，让用户明确当前操作对象已切换
    pushOutput("ℹ️ 已切换目标设备：" + serial + "（应用列表已重置，请重新查询）");
}

/* ---------------------------------------------------------------------------
 * 底部面板高度管理（V1.9 重设计）
 * 命令行和实时日志需要大窗口才有意义（用户反馈），新交互：
 *   1. 「展开最大」：一键升到最大高度（480px，约半屏——沿用既有上限）
 *   2. 「收起」：从最大恢复默认高度（180px），不再是折叠成标题条
 *   3. 切换到命令行/实时日志模式时，若面板还在默认高度则自动升到最大；
 *      用户手动拖拽过（>默认高度）则尊重用户尺寸不自动改
 *   4. 拖拽把手仍可自由调整（70~480px），按钮文案随当前高度联动
 * ------------------------------------------------------------------------ */

// 面板高度档位：默认（收起目标）与最大（展开目标）
const OUTPUT_DEFAULT_HEIGHT = 180;
const OUTPUT_MAX_HEIGHT = 480;

// 读取面板当前像素高度（未设置过时取默认值）
function outputPanelHeight() {
    const h = parseFloat($("output-panel").style.height);
    return isNaN(h) ? OUTPUT_DEFAULT_HEIGHT : h;
}

// 更新「展开/收起」按钮文案（高度接近最大 → 显示"收起"，否则显示"展开最大"）
function refreshOutputToggleText() {
    const nearMax = outputPanelHeight() >= OUTPUT_MAX_HEIGHT - 20;
    $("btn-toggle-output").textContent = nearMax ? "收起 ▾" : "展开最大 ▴";
}

// 切到命令行/实时日志模式时的自动增高：
// 仅当面板还处于默认档（≤默认高度+20）时才升到最大——
// 用户手动拖到过别的尺寸则不动，尊重用户选择
function autoExpandPanelForMode() {
    if (outputPanelHeight() <= OUTPUT_DEFAULT_HEIGHT + 20) {
        $("output-panel").style.height = OUTPUT_MAX_HEIGHT + "px";
    }
    refreshOutputToggleText();
}

function initOutputPanel() {
    const panel = $("output-panel");

    // ---- 展开最大 / 收起（两档切换）----
    $("btn-toggle-output").addEventListener("click", function () {
        const nearMax = outputPanelHeight() >= OUTPUT_MAX_HEIGHT - 20;
        // 当前接近最大 → 收回到默认档；否则（默认/拖小的尺寸）→ 升到最大
        panel.style.height = (nearMax ? OUTPUT_DEFAULT_HEIGHT : OUTPUT_MAX_HEIGHT) + "px";
        refreshOutputToggleText();
    });

    // ---- 拖拽把手调整高度（拖完同步按钮文案）----
    let dragging = false;

    $("output-resize").addEventListener("mousedown", function (e) {
        dragging = true;
        e.preventDefault(); // 防止拖拽时选中文本
    });

    window.addEventListener("mousemove", function (e) {
        if (!dragging) {
            return;
        }
        // 面板贴着窗口底部：高度 = 窗口高度 - 鼠标Y - 底部留白(12px padding)
        const h = window.innerHeight - e.clientY - 12;
        // 限制高度范围：最小 70（只剩标题+一行），最大 480（约半屏）
        panel.style.height = Math.max(70, Math.min(OUTPUT_MAX_HEIGHT, h)) + "px";
    });

    window.addEventListener("mouseup", function () {
        if (dragging) {
            dragging = false;
            refreshOutputToggleText(); // 拖拽结束按新高度刷新按钮文案
        }
    });

    refreshOutputToggleText(); // 初始文案
}

/* ---------------------------------------------------------------------------
 * 初始化：绑定事件、加载状态
 * ------------------------------------------------------------------------ */

function init() {
    // ---- 顶部按钮 ----
    // 手动刷新设备列表（后台 3 秒轮询 + 窗口聚焦刷新之外的手动入口）
    $("btn-refresh-devices").addEventListener("click", function () {
        refreshDevices(true);
    });

    // ---- 设备连接 ----
    $("btn-connect").addEventListener("click", doConnect);
    // 「查看设备列表」弹窗：完整设备信息（连接方式/状态/型号/安卓版本）
    $("btn-view-devices").addEventListener("click", openDeviceModal);
    // 弹窗内：刷新按钮重新拉取，关闭按钮收起；列表行内按钮事件委托分发
    $("btn-device-modal-refresh").addEventListener("click", loadDeviceModal);
    $("btn-device-modal-close").addEventListener("click", closeDeviceModal);
    $("device-modal-list").addEventListener("click", onDeviceModalClick);
    // 历史区与当前设备列表是两个容器，「重新连接」按钮也要走同一套事件委托
    $("device-history-list").addEventListener("click", onDeviceModalClick);

    // 注意：设备下拉框不再挂 mousedown 自动刷新 ——
    // 旧实现会在下拉展开期间异步重建选项，导致弹窗被收起、选中值丢失；
    // 现在列表由后台轮询保持最新，展开即可看到当前设备
    // 切换目标设备时联动刷新：停日志抓取、清空应用列表、恢复默认状态
    $("device-select").addEventListener("change", onDeviceChanged);

    // ---- 应用管理 ----
    $("btn-packages").addEventListener("click", queryApps);
    // 应用列表行内按钮：事件委托统一分发
    $("app-list").addEventListener("click", onAppListClick);

    // ---- 文件与设备操作 ----
    $("btn-shot").addEventListener("click", doScreenshot);
    $("btn-install").addEventListener("click", doInstall);
    $("btn-pull").addEventListener("click", doPull);
    $("btn-push").addEventListener("click", doPush);

    // ---- 文件 / 目录选择按钮 ----
    $("btn-apk-file").addEventListener("click", pickApkFile);
    $("btn-shot-dir").addEventListener("click", pickShotDir);
    $("btn-pull-dir").addEventListener("click", pickPullDir);
    $("btn-push-file").addEventListener("click", pickPushFile);

    // ---- 日志抓取 ----
    $("btn-log-dir").addEventListener("click", pickLogDir);
    // 「默认」按钮：清空自定义目录，恢复为程序目录下 logs/
    $("btn-log-dir-reset").addEventListener("click", function () {
        $("log-dir").value = "";
        pushOutput("ℹ️ 日志目录已恢复为默认（程序目录下 logs/）。");
    });
    $("btn-start-logcat").addEventListener("click", doStartLogcat);
    $("btn-stop-logcat").addEventListener("click", doStopLogcat);

    // ---- 底部面板三模式（V1.8）----
    // 模式切换分段按钮：事件委托统一分发（data-mode 携带目标模式）
    $("panel-mode-switch").addEventListener("click", function (e) {
        const mode = e.target.dataset.mode;
        if (mode) {
            setPanelMode(mode);
        }
    });
    // 命令行输入框：Enter 执行 / ↑↓ 翻历史
    $("cmd-term-input").addEventListener("keydown", onCmdInputKey);

    // ---- 实时日志（面板模式之一）----
    // 级别切换：重启实时会话（logcat 原生过滤在设备端生效）
    $("live-level").addEventListener("change", onLiveLevelChange);
    // 包名过滤：回车应用/取消（后端解析 pid 过滤，参考 AS 的 package 过滤）
    $("live-pkg").addEventListener("keydown", function (e) {
        if (e.key === "Enter") {
            onLivePkgApply();
            e.preventDefault();
        }
    });
    // 暂停/继续：暂停界面刷新（后台继续缓冲），继续时全量补齐
    $("btn-live-pause").addEventListener("click", onLivePauseToggle);
    // 清空（工具栏内，位于暂停与滚动到最新之间——V1.9 调整）
    $("btn-live-clear").addEventListener("click", onLiveClear);
    // 「滚动到最新」图标按钮：点击切换跟随状态（点亮=自动跟随最新一行）
    $("btn-live-follow").addEventListener("click", function () {
        liveFollow = !liveFollow;
        setLiveFollowButton(liveFollow);
        if (liveFollow) {
            scrollLiveToBottom();
        }
    });
    // 关键字过滤：输入即更新缓存变量（增量过滤用），
    // 全量重渲 200ms 防抖（避免每敲一个字重建上千行 DOM）
    let liveKeywordTimer = null;
    $("live-keyword").addEventListener("input", function () {
        liveKeyword = this.value.trim().toLowerCase(); // 同步缓存，下一批过滤即生效
        clearTimeout(liveKeywordTimer);
        liveKeywordTimer = setTimeout(renderLiveLogFull, 200);
    });
    // 滚动位置与自动跟随联动（对齐 AS Logcat 习惯）：
    // 用户向上滚离底部 → 自动停止跟随（按钮熄灭）；滚回底部 → 自动恢复（点亮）
    $("live-log-view").addEventListener("scroll", function () {
        const atBottom = this.scrollHeight - this.scrollTop - this.clientHeight < 40;
        if (atBottom !== liveFollow) {
            liveFollow = atBottom;
            setLiveFollowButton(atBottom); // 按钮状态即跟随状态，一眼可辨
        }
    });
    // 订阅实时日志批量推送事件（Go 侧 200ms/200 行聚合后发出）
    window.runtime.EventsOn("live-log-lines", onLiveLines);
    // 订阅会话结束事件（用户停止 / 设备断开 / adb 退出）
    window.runtime.EventsOn("live-log-ended", onLiveEnded);

    // ---- 操作反馈栏（固定高度面板：收起/展开/拖拽调高） ----
    // 清空按钮按当前模式分发（操作反馈 / 命令行输出 / 实时日志）
    $("btn-clear-output").addEventListener("click", onClearCurrentMode);
    initOutputPanel();

    // ---- ADB 环境准备向导 ----
    $("btn-env-install").addEventListener("click", envStartInstall);
    $("btn-env-local-zip").addEventListener("click", envPickLocalZip);
    $("btn-env-online").addEventListener("click", envInstallOnline);
    $("btn-env-enter").addEventListener("click", envEnterMain);
    // 订阅联网下载进度事件（Go 侧推送 percent/received/total）
    window.runtime.EventsOn("adb-setup-progress", function (p) {
        const bar = $("env-progress-bar");
        const text = $("env-progress-text");
        if (p.total > 0) {
            bar.style.width = Math.max(0, Math.min(100, p.percent)) + "%";
            text.textContent = "已下载 " + formatBytes(p.received) + " / " + formatBytes(p.total)
                + "（" + p.percent + "%）";
        } else {
            // 服务端未返回总大小时只展示已下载量
            bar.style.width = "100%";
            text.textContent = "已下载 " + formatBytes(p.received);
        }
    });

    // ---- 启动入口：总是先进入环境准备向导（检测结果由用户确认后进入主界面）----
    window.go.main.App.GetAdbStatus().then(function (path) {
        updateAdbBadge(path);
        runEnvCheck();
    });
}

// 页面加载完成后初始化
window.addEventListener("DOMContentLoaded", init);
