// 根构建脚本：仅声明插件版本（M1 用 Kotlin + 传统 View，不引入 Compose，依赖最小化）
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
}
