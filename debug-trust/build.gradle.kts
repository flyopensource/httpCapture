plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
    id("maven-publish")
}

val jitPackGroup = providers.environmentVariable("GROUP").orNull
val jitPackArtifact = providers.environmentVariable("ARTIFACT").orNull

group = if (!jitPackGroup.isNullOrBlank() && !jitPackArtifact.isNullOrBlank()) {
    "$jitPackGroup.$jitPackArtifact"
} else {
    "com.github.flyopensource.httpCapture"
}
version = providers.environmentVariable("VERSION").orElse("0.4.0-local").get()

android {
    namespace = "com.fly.httpcapture.debugtrust"
    compileSdk = 35
    defaultConfig { minSdk = 21 }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    publishing {
        singleVariant("release") {
            withSourcesJar()
        }
    }
}

afterEvaluate {
    publishing {
        publications {
            create<MavenPublication>("release") {
                from(components["release"])
                artifactId = "debug-trust"
            }
        }
    }
}
