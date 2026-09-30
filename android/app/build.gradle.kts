import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.ksp)
}

// Firebase is configured from a file kept outside the repository. Without
// it the app builds and runs with push notifications turned off.
val configDir = File(System.getProperty("user.home"), ".config/remote-agent")
val googleServices = File(configDir, "google-services.json")
// -PappIdSuffix=.v2dev builds a copy that installs next to the real app,
// e.g. to test against a local server without touching its account. The
// Firebase config names the real package only, so the copy has no push.
val appIdSuffix = (findProperty("appIdSuffix") as String?).orEmpty()
val pushEnabled = googleServices.exists() && appIdSuffix.isEmpty()
if (pushEnabled) {
    googleServices.copyTo(file("google-services.json"), overwrite = true)
    apply(plugin = "com.google.gms.google-services")
} else {
    file("google-services.json").delete()
}

// Release signing, also kept outside the repository:
// signing.properties has storeFile, storePassword, keyAlias, keyPassword.
val signingFile = File(configDir, "signing.properties")
val signing = Properties().apply { if (signingFile.exists()) signingFile.inputStream().use { load(it) } }

android {
    namespace = "io.github.lewin671.remoteagent"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.github.lewin671.remoteagent"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
        buildConfigField("boolean", "PUSH_ENABLED", pushEnabled.toString())
        resValue("string", "app_name", if (appIdSuffix.isEmpty()) "remote-agent" else "remote-agent $appIdSuffix")
        if (appIdSuffix.isNotEmpty()) applicationIdSuffix = appIdSuffix
    }

    signingConfigs {
        if (signingFile.exists()) {
            create("release") {
                storeFile = File(configDir, signing.getProperty("storeFile"))
                storePassword = signing.getProperty("storePassword")
                keyAlias = signing.getProperty("keyAlias")
                keyPassword = signing.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            // Without a release key the release build is signed with the
            // debug key, which is enough to test it with R8.
            signingConfig = if (signingFile.exists()) signingConfigs.getByName("release") else signingConfigs.getByName("debug")
        }
        // The release build (R8 and all) that may talk to a local server
        // over plain HTTP, for testing against a dev server.
        create("minified") {
            initWith(getByName("release"))
            matchingFallbacks += "release"
            signingConfig = signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures {
        compose = true
        buildConfig = true
    }
    packaging {
        resources.excludes += setOf("META-INF/versions/9/OSGI-INF/MANIFEST.MF", "META-INF/DEPENDENCIES", "META-INF/LICENSE*", "META-INF/NOTICE*")
    }
}

kotlin {
    jvmToolchain(17)
}

ksp {
    arg("room.schemaLocation", "$projectDir/schemas")
}

dependencies {
    implementation(project(":core"))
    implementation(project(":terminal"))

    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.ui.tooling.preview)
    implementation(libs.compose.material3)
    implementation(libs.compose.icons)
    debugImplementation(libs.compose.ui.tooling)
    implementation(libs.core.ktx)
    implementation(libs.activity.compose)
    implementation(libs.fragment)
    implementation(libs.lifecycle.runtime.compose)
    implementation(libs.lifecycle.viewmodel.compose)
    implementation(libs.lifecycle.process)
    implementation(libs.navigation.compose)
    implementation(libs.coroutines.android)

    implementation(libs.room.runtime)
    implementation(libs.room.ktx)
    ksp(libs.room.compiler)
    implementation(libs.datastore.preferences)
    implementation(libs.work.runtime)

    implementation(libs.camera.camera2)
    implementation(libs.camera.lifecycle)
    implementation(libs.camera.view)
    implementation(libs.zxing)

    implementation(platform(libs.firebase.bom))
    implementation(libs.firebase.messaging)

    testImplementation(libs.junit)
}
