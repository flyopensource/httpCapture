# Debug Trust SDK

这个 AAR 只通过 Android Network Security Configuration 让 Debug 构建信任用户安装的 CA，不包含运行时代码，不使用 `TrustAll`。

业务 App 接入：

```kotlin
dependencies {
    debugImplementation(files("libs/httpcapture-debug-trust.aar"))
}
```

或在本仓库中：

```kotlin
debugImplementation(project(":debug-trust"))
```

不要添加 `releaseImplementation`。Release APK 中不应出现 `com.fly.httpcapture.DEBUG_TRUST` 元数据，也不应合并 `httpcapture_network_security_config.xml`。

如果业务 App 已有 `android:networkSecurityConfig`，Manifest 合并会冲突。此时保留业务配置，并仅在业务的 Debug 资源中合入：

```xml
<debug-overrides>
    <trust-anchors>
        <certificates src="user" />
    </trust-anchors>
</debug-overrides>
```

SDK 只影响遵循 Android 系统信任策略的网络栈；证书锁定、自带 CA 库或特殊 Cronet 配置需要业务 App 自行处理。
