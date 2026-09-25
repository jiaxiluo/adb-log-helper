# -*- coding: utf-8 -*-
"""M1 APK 打包流水线：构建 → 补版本号库 → 对齐 → 签名 → 依赖完整性终验 → 发布

用法: python package_apk.py            （在 android/ 目录下执行，需先 export JAVA_HOME）
产物: ../adb-helper/adb-helper-debug.apk

为什么存在本脚本（坑与对策，缺一不可）：
  1. AGP 只打包 lib*.so 命名模式的库，libz.so.1 / libzstd.so.1 会被【静默丢弃】
     → 构建后 zip 级注入补回
  2. zip 级修改破坏 APK 签名 → zipalign + apksigner 重签（debug.keystore）
  3. 依赖分析必须【递归闭包】：adb → libprotobuf → 46 个 libabsl_*，
     只看一层依赖永远在挤牙膏 → 终验对 APK 解出的每个库递归核对 DT_NEEDED，
     任何缺失（且不在系统库白名单）直接拒绝发布
"""
import os
import shutil
import struct
import subprocess
import sys
import zipfile

sys.stdout.reconfigure(encoding="utf-8")

ROOT = os.path.dirname(os.path.abspath(__file__))
APK = os.path.join(ROOT, "app/build/outputs/apk/debug/app-debug.apk")
TERMUX_USR = "D:/Work/Dev/termux-extract/data/data/com.termux/files/usr"
TERMUX_ARM = "D:/Work/Dev/termux-extract/arm/data/data/com.termux/files/usr"
BT = "D:/Work/Dev/android-sdk/build-tools/34.0.0"
KEYSTORE = os.path.expanduser("~/.android/debug.keystore")
# M5 正式签名（30 年有效期；此 keystore 是关键资产，丢失则新版无法覆盖安装）
RELEASE_KEYSTORE = "D:/Work/Dev/adb-helper-release.keystore"
# 签名口令不入库（仓库公开）：优先环境变量 ADB_HELPER_KS_PASS，其次同目录 signing.env（已 .gitignore）
RELEASE_PASS = os.environ.get("ADB_HELPER_KS_PASS", "")
if not RELEASE_PASS:
    _SIGNING_ENV = os.path.join(ROOT, "signing.env")
    if os.path.exists(_SIGNING_ENV):
        with open(_SIGNING_ENV, "r", encoding="utf-8") as f:
            RELEASE_PASS = f.readline().strip()
PUBLISH = os.path.normpath(os.path.join(ROOT, "../adb-helper/adb-helper-v1.0.0.apk"))
# 安卓系统全局命名空间一定提供的库（bionic + liblog），出现在 DT_NEEDED 属正常
SYSTEM_LIBS = {"libc.so", "libdl.so", "libm.so", "liblog.so"}
# AGP 命名过滤会丢弃的带版本号库名 → 构建后手工注入（abi 子目录 → 库名）
EXTRA_INJECT = {
    "arm64-v8a": ["libz.so.1", "libzstd.so.1"],
    "armeabi-v7a": ["libz.so.1", "libzstd.so.1"],
}
ELF_ARCH = {"arm64-v8a": 2, "armeabi-v7a": 3}  # e_machine: AArch64 / ARM


def parse_needed(data, bits=64):
    """解析 ELF(64/32位 LE) 的 DT_NEEDED 完整列表（含 LOAD 段 vaddr→file offset 映射）。"""
    if data[:4] != b"\x7fELF" or data[5] != 1 or data[4] not in (1, 2):
        raise ValueError("not ELF LE")
    if (data[4] == 2) != (bits == 64):
        raise ValueError("ELF%d, expect ELF%d" % (32 if data[4] == 1 else 64, bits))
    E = "<Q" if bits == 64 else "<I"          # e_phoff 字段
    ENT = 16 if bits == 64 else 8             # 动态段表项大小
    DYN = "<qQ" if bits == 64 else "<iI"      # 动态段表项
    e_phoff, = struct.unpack_from(E, data, 28 if bits == 32 else 32)
    e_phentsize, e_phnum = struct.unpack_from("<HH", data, 42 if bits == 32 else 54)
    loads, dyn = [], None
    for i in range(e_phnum):
        off = e_phoff + i * e_phentsize
        p_type, = struct.unpack_from("<I", data, off)
        if bits == 64:
            p_offset, p_vaddr = struct.unpack_from("<QQ", data, off + 8)
            p_filesz, = struct.unpack_from("<Q", data, off + 32)
        else:
            p_offset, p_vaddr = struct.unpack_from("<II", data, off + 4)
            p_filesz, = struct.unpack_from("<I", data, off + 16)
        if p_type == 1:
            loads.append((p_vaddr, p_filesz, p_offset))
        elif p_type == 2:
            dyn = (p_offset, p_filesz)
    if not dyn:
        return []
    def v2o(v):
        for vaddr, filesz, offset in loads:
            if vaddr <= v < vaddr + filesz:
                return v - vaddr + offset
        raise ValueError("vaddr unmapped: 0x%x" % v)
    off, size = dyn
    entries, strtab = [], None
    i = off
    while i + ENT <= off + size:
        tag, val = struct.unpack_from(DYN, data, i)
        i += ENT
        if tag == 0:
            break
        if tag == 5 and strtab is None:
            strtab = val
        entries.append((tag, val))
    so = v2o(strtab)
    out = []
    for tag, val in entries:
        if tag == 1:
            end = data.index(b"\0", so + val)
            out.append(data[so + val:end].decode())
    return out


def verify_closure(apk_path):
    """按架构对 APK 内 lib/<abi>/ 的 ELF 递归核对依赖闭包；缺失即失败。"""
    z = zipfile.ZipFile(apk_path)
    pools = {}  # abi -> {libname: bytes}
    for n in z.namelist():
        parts = n.split("/")
        if len(parts) == 3 and parts[0] == "lib" and parts[2]:
            pools.setdefault(parts[1], {})[parts[2]] = z.read(n)
    z.close()
    ok = True
    for abi, pool in sorted(pools.items()):
        bits = 64 if abi == "arm64-v8a" else 32
        checked, missing = 0, []
        queue, seen = ["libadb.so"], set()
        while queue:
            name = queue.pop(0)
            if name in seen or name in SYSTEM_LIBS:
                continue
            if name not in pool:
                missing.append(name)
                continue
            seen.add(name)
            checked += 1
            try:
                queue.extend(parse_needed(pool[name], bits))
            except ValueError as e:
                missing.append("%s(%s)" % (name, e))
        if missing:
            print("  [FAIL] %s 依赖缺失: %s" % (abi, sorted(set(missing))))
            ok = False
        else:
            print("  [PASS] %s: %d 库闭包完整" % (abi, checked))
    return ok


def main():
    os.chdir(ROOT)
    # 1. gradle 构建
    print("[1/5] gradle assembleDebug ...")
    env = dict(os.environ, JAVA_HOME="D:/Work/Dev/jdk-17.0.20.1+1")
    subprocess.run([r"D:/Work/Dev/gradle-8.7/bin/gradle.bat", "assembleDebug", "--no-daemon", "-q"],
                   check=True, env=env)
    # 2. 注入 AGP 丢弃的带版本号库
    inject_flat = [(abi, n) for abi, ns in EXTRA_INJECT.items() for n in ns]
    print("[2/5] 注入 %s ..." % ", ".join("%s/%s" % t for t in inject_flat))
    tmp = APK + ".patched"
    zin = zipfile.ZipFile(APK)
    zout = zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED)
    skip = {"lib/%s/%s" % t for t in inject_flat}
    for item in zin.namelist():
        if item in skip:
            continue
        zout.writestr(zin.getinfo(item), zin.read(item))
    for abi, n in inject_flat:
        src = (TERMUX_USR if abi == "arm64-v8a" else TERMUX_ARM) + "/lib/" + n
        zout.write(src, "lib/%s/%s" % (abi, n))
    zout.close()
    zin.close()
    shutil.move(tmp, APK)
    # 3. 对齐 + 正式签名（release keystore；M5 起用正式包，覆盖安装链路依赖此签名稳定）
    print("[3/5] zipalign + apksigner (release) ...")
    if not RELEASE_PASS:
        print("[FAIL] 缺少签名口令：设置环境变量 ADB_HELPER_KS_PASS，或在 android/signing.env 写入口令（该文件不进仓库）")
        sys.exit(1)
    subprocess.run([BT + "/zipalign.exe", "-f", "4", APK, APK + ".aligned"], check=True)
    subprocess.run([BT + "/apksigner.bat", "sign",
                    "--ks", RELEASE_KEYSTORE, "--ks-pass", "pass:" + RELEASE_PASS,
                    "--ks-key-alias", "adbhelper", "--key-pass", "pass:" + RELEASE_PASS,
                    "--out", APK + ".signed", APK + ".aligned"], check=True)
    shutil.move(APK + ".signed", APK)
    os.remove(APK + ".aligned")
    # 4. 依赖闭包终验
    print("[4/5] 依赖完整性终验 ...")
    if not verify_closure(APK):
        print("[FAIL] 拒绝发布")
        sys.exit(1)
    # 5. 发布
    print("[5/5] 发布 -> %s (%.2f MB)" % (PUBLISH, os.path.getsize(APK) / 1048576))
    shutil.copy(APK, PUBLISH)
    print("DONE")


if __name__ == "__main__":
    main()
