import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val releaseSigningProperties = Properties()
val releaseSigningPropertiesFile = rootProject.file("key.properties")
if (releaseSigningPropertiesFile.isFile) {
    releaseSigningPropertiesFile.inputStream().use(releaseSigningProperties::load)
}

fun releaseSigningValue(propertyName: String, environmentName: String): String? =
    releaseSigningProperties.getProperty(propertyName)?.takeIf(String::isNotBlank)
        ?: providers.environmentVariable(environmentName).orNull?.takeIf(String::isNotBlank)

val releaseSigningEnvironment = mapOf(
    "storeFile" to releaseSigningValue("storeFile", "HTTPCAPTURE_SIGNING_STORE_FILE"),
    "storePassword" to releaseSigningValue("storePassword", "HTTPCAPTURE_SIGNING_STORE_PASSWORD"),
    "keyAlias" to releaseSigningValue("keyAlias", "HTTPCAPTURE_SIGNING_KEY_ALIAS"),
    "keyPassword" to releaseSigningValue("keyPassword", "HTTPCAPTURE_SIGNING_KEY_PASSWORD"),
)
val releaseSigningConfigured = releaseSigningEnvironment.values.all { !it.isNullOrBlank() }
if (releaseSigningEnvironment.values.any { !it.isNullOrBlank() } && !releaseSigningConfigured) {
    error("Release signing requires all HTTPCAPTURE_SIGNING_* environment variables")
}

android {
    namespace = "com.fly.httpcapture"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.fly.httpcapture"
        minSdk = 26
        targetSdk = 35
        versionCode = 3
        versionName = "0.3.0"
    }

    signingConfigs {
        if (releaseSigningConfigured) {
            create("release") {
                storeFile = rootProject.file(requireNotNull(releaseSigningEnvironment["storeFile"]))
                storePassword = requireNotNull(releaseSigningEnvironment["storePassword"])
                keyAlias = requireNotNull(releaseSigningEnvironment["keyAlias"])
                keyPassword = requireNotNull(releaseSigningEnvironment["keyPassword"])
            }
        }
    }
    buildTypes {
        release {
            isMinifyEnabled = false
            if (releaseSigningConfigured) {
                signingConfig = signingConfigs.getByName("release")
            }
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
    buildFeatures { compose = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    splits {
        abi {
            isEnable = true
            reset()
            include("armeabi-v7a", "arm64-v8a", "x86_64")
            isUniversalApk = true
        }
    }
    packaging { resources.excludes += "/META-INF/{AL2.0,LGPL2.1}" }
}

androidComponents {
    onVariants(selector().all()) { variant ->
        variant.outputs.forEach { output ->
            val abi = output.filters
                .firstOrNull {
                    it.filterType == com.android.build.api.variant.FilterConfiguration.FilterType.ABI
                }
                ?.identifier
            output.enabled.set(
                if (variant.buildType == "release") {
                    abi == "armeabi-v7a" || abi == "arm64-v8a"
                } else {
                    abi == null
                },
            )
        }
    }
}

dependencies {
    implementation(platform("androidx.compose:compose-bom:2025.04.01"))
    implementation("androidx.activity:activity-compose:1.10.1")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.0")
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
    implementation("com.google.zxing:core:3.5.3")
    testImplementation("junit:junit:4.13.2")
    debugImplementation("androidx.compose.ui:ui-tooling")
}
