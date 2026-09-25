// M1 技术验证壳工程：唯一目的 = 验证「打包 libadb.so → exec → 连电视 → 装包 → 抓日志」四步链路
// 通过后 M2 起在真实工程里重写 UI（Compose），本工程作为 ADB 路线的活体证据保留
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.jiaxluo.adbhelper"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.jiaxluo.adbhelper"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "1.0.0"
    }

    packaging {
        jniLibs {
            // ★ 总开关：必须让 .so 解压为真实文件，nativeLibraryDir 里才有可执行的 libadb.so
            // （AGP 8 默认 extractNativeLibs=false，.so 只留在 APK 内，exec 会直接 ENOENT）
            useLegacyPackaging = true
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    buildFeatures {
        compose = true
    }
    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.14"
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.05.00")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.activity:activity-compose:1.9.0")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.2")
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    testImplementation("junit:junit:4.13.2")
}
// 无第三方业务依赖：Compose(M3) + AndroidX 基础包
