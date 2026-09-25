// ADB 助手（安卓版）— M1 技术验证壳工程
// 仓库策略：google() 直连（dl.google.com 国内 CDN 实测可达），阿里云镜像兜底 Maven Central
pluginManagement {
    repositories {
        google()
        maven("https://maven.aliyun.com/repository/gradle-plugin")
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        maven("https://maven.aliyun.com/repository/public")
        mavenCentral()
    }
}
rootProject.name = "adb-helper-android"
include(":app")
