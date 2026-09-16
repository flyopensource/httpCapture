# Debug Trust SDK

这个 SDK 只通过 Android Network Security Configuration 让 Debug 构建信任用户安装的 CA，不包含运行时代码，不使用 `TrustAll`。业务项目通过 Maven 坐标引用，不需要手工构建、复制或提交 AAR 文件。

在业务项目的 `settings.gradle.kts` 添加 JitPack 仓库：

```kotlin
dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
        maven {
            url = uri("https://jitpack.io")
            content { includeGroup("com.github.flyopensource.httpCapture") }
        }
    }
}
```

只在需要抓包的 Debug 和 Alpha 构建中添加依赖：

```kotlin
dependencies {
    debugImplementation("com.github.flyopensource.httpCapture:debug-trust:v0.4.0")
    "alphaImplementation"("com.github.flyopensource.httpCapture:debug-trust:v0.4.0")
}
```

同一源码仓库中的样例或本地开发仍可直接使用模块依赖：

```kotlin
dependencies {
    debugImplementation(project(":debug-trust"))
}
```

不要添加 `releaseImplementation`。Release APK 中不应出现 `com.fly.httpcapture.DEBUG_TRUST` 元数据，也不应合并 SDK 的 `network_security_config.xml`。

如果业务 App 已有 `android:networkSecurityConfig` 且使用其他资源名，Manifest 合并会冲突。此时保留业务配置，并仅在业务的 Debug 资源中合入：

```xml
<debug-overrides>
    <trust-anchors>
        <certificates src="user" />
    </trust-anchors>
</debug-overrides>
```

SDK 只影响遵循 Android 系统信任策略的网络栈；证书锁定、自带 CA 库或特殊 Cronet 配置需要业务 App 自行处理。
