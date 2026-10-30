plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Release signing comes from the environment (CI secrets). Without it,
// release APKs stay unsigned: a debug-signed "release" could not be updated
// by real releases, which use another key.
val releaseKeystore: String? = System.getenv("PASSAGE_KEYSTORE")

android {
    namespace = "info.gluek.passage"
    compileSdk = 36

    defaultConfig {
        applicationId = "info.gluek.passage"
        minSdk = 24
        targetSdk = 36
        versionCode = 2
        versionName = "0.2.0-beta2"
    }

    // One APK per ABI plus a universal one.
    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a", "armeabi-v7a")
            isUniversalApk = true
        }
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
            signingConfig = signingConfigs.findByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    // libpassage.so, built from core/cmd/libpassage.
    sourceSets.getByName("main").jniLibs.srcDir("src/main/nativeLibs")
}

// versionCode * 10 + ABI digit, so a phone always gets the APK for its ABI
// as the newer one: universal 0, armeabi-v7a 1, arm64-v8a 2.
val abiDigit = mapOf("armeabi-v7a" to 1, "arm64-v8a" to 2)
androidComponents {
    onVariants { variant ->
        variant.outputs.forEach { output ->
            val abi = output.filters.find {
                it.filterType == com.android.build.api.variant.FilterConfiguration.FilterType.ABI
            }?.identifier
            output.versionCode.set((output.versionCode.orNull ?: 1) * 10 + (abiDigit[abi] ?: 0))
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}
