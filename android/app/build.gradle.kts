plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Release signing comes from the environment (CI secrets). Without it,
// release builds are signed with the debug key.
val releaseKeystore: String? = System.getenv("PASSAGE_KEYSTORE")

android {
    namespace = "info.gluek.passage"
    compileSdk = 36

    defaultConfig {
        applicationId = "info.gluek.passage"
        minSdk = 24
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
        ndk { abiFilters += listOf("arm64-v8a") }
    }

    signingConfigs {
        if (releaseKeystore != null) {
            create("release") {
                storeFile = file(releaseKeystore)
                storeType = "pkcs12"
                storePassword = System.getenv("PASSAGE_KEYSTORE_PASS")
                keyAlias = System.getenv("PASSAGE_KEY_ALIAS")
                keyPassword = System.getenv("PASSAGE_KEYSTORE_PASS")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release") ?: signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    // libpassage.so, built from core/cmd/libpassage.
    sourceSets.getByName("main").jniLibs.srcDir("src/main/nativeLibs")
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}
