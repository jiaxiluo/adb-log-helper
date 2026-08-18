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
             4. 日志抓取只有「开始 / 结束」两个动作 + 状态行，无实时终端
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

// 断开 TCP 设备：adb disconnect <ip:port>，成功后自动刷新设备列表
async function doDisconnect() {
    const address = buildConnectAddress();
    if (!address) return;

    pushOutput("正在断开设备：" + address + " …");
    const res = await run(window.go.main.App.Disconnect(address));
    if (!res.ok) {
        pushOutput("❌ 断开失败：" + res.err);
        return;
    }
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
    }
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
    await refreshDevices();
    pushOutput("✅ " + res.result);
}

// 断开 TCP 设备：adb disconnect <ip:port>，成功后自动刷新设备列表
async function doDisconnect() {
    const address = buildConnectAddress();
    if (!address) return;

    pushOutput("正在断开设备：" + address + " …");
    const res = await run(window.go.main.App.Disconnect(address));
    if (!res.ok) {
        pushOutput("❌ 断开失败：" + res.err);
        return;
    }
    await refreshDevices();
    pushOutput("✅ " + res.result);
}

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

// 开始抓取：调用后端 StartLogcat，界面只更新状态行与反馈，不展示日志内容
async function doStartLogcat() {
    const serial = requireDevice();
    if (!serial) return;
    const dir = $("log-dir").value.trim(); // 留空时后端使用默认 logs/ 目录

    $("logcat-status").textContent = "当前状态：正在启动…";
    const res = await run(window.go.main.App.StartLogcat(serial, dir));
    if (!res.ok) {
        $("logcat-status").textContent = "当前状态：启动失败";
        pushOutput("❌ 日志抓取启动失败：" + res.err);
        return;
    }
    // 后端返回本次日志文件的完整路径，展示给用户便于定位
    $("logcat-status").textContent = "当前状态：抓取中（设备 " + serial + "）";
    pushOutput("✅ 日志抓取已开始，文件：" + res.result);
}

// 结束抓取：StopLogcat 幂等，直接调用即可
async function doStopLogcat() {
    await window.go.main.App.StopLogcat();
    $("logcat-status").textContent = "当前状态：未抓取";
    pushOutput("✅ 日志抓取已结束，文件已保存。");
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

    // 2. 清空应用列表、过滤条件与查询范围勾选（应用列表属于旧设备的数据）
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
 * 底部操作反馈面板（VS Code 终端面板模式）
 * 1. 面板固定高度，文本在面板内部滚动 —— 无论反馈积累多少条，
 *    都不会挤压上方功能区（此前"面板被撑高、功能区被压扁"问题的根治方案）
 * 2. 顶部把手可拖拽调整面板高度（限制在 70~480px）
 * 3. 标题栏「收起/展开」按钮可一键折叠为标题条
 * ------------------------------------------------------------------------ */

// 收起状态标记（收起时忽略拖拽调高）
let outputCollapsed = false;

function initOutputPanel() {
    // ---- 一键收起 / 展开 ----
    $("btn-toggle-output").addEventListener("click", function () {
        outputCollapsed = !outputCollapsed;
        $("output-panel").classList.toggle("collapsed", outputCollapsed);
        this.textContent = outputCollapsed ? "展开 ▴" : "收起 ▾";
    });

    // ---- 拖拽把手调整高度 ----
    const panel = $("output-panel");
    let dragging = false;

    $("output-resize").addEventListener("mousedown", function (e) {
        if (outputCollapsed) {
            return; // 收起状态下不允许拖拽
        }
        dragging = true;
        e.preventDefault(); // 防止拖拽时选中文本
    });

    window.addEventListener("mousemove", function (e) {
        if (!dragging) {
            return;
        }
        // 面板贴着窗口底部：高度 = 窗口高度 - 鼠标Y - 底部留白(12px padding)
        const h = window.innerHeight - e.clientY - 12;
        // 限制高度范围：最小 70（只剩标题+一行），最大 480（不超过半屏）
        panel.style.height = Math.max(70, Math.min(480, h)) + "px";
    });

    window.addEventListener("mouseup", function () {
        dragging = false;
    });
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
    $("btn-disconnect").addEventListener("click", doDisconnect);
    // 「查看设备列表」弹窗：完整设备信息（连接方式/状态/型号/安卓版本）
    $("btn-view-devices").addEventListener("click", openDeviceModal);
    // 弹窗内：刷新按钮重新拉取，关闭按钮收起；列表行内按钮事件委托分发
    $("btn-device-modal-refresh").addEventListener("click", loadDeviceModal);
    $("btn-device-modal-close").addEventListener("click", closeDeviceModal);
    $("device-modal-list").addEventListener("click", onDeviceModalClick);

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

    // ---- 操作反馈栏（固定高度面板：收起/展开/拖拽调高） ----
    $("btn-clear-output").addEventListener("click", clearOutput);
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
