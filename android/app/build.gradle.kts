import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.ksp)
}

// Firebase is configured from a file kept outside the repository. Without
// it the app builds and runs with push notifications turned off.
val product = Properties().apply { rootProject.file("../version.properties").inputStream().use { load(it) } }
val devBuild = providers.gradleProperty("devBuild").orNull == "true"
val officialRelease = providers.gradleProperty("officialRelease").orNull == "true"
val buildCommit = providers.gradleProperty("buildCommit").getOrElse("unknown")
val configDir = File(System.getProperty("user.home"), ".config/remote-agent")
val googleServices = File(configDir, "google-services.json")
// -PappIdSuffix=.v2dev builds a copy that installs next to the real app,
// e.g. to test against a local server without touching its account. The
// Firebase config names the real package only, so the copy has no push.
val appIdSuffix = providers.gradleProperty("appIdSuffix").getOrElse(if (devBuild) ".dev" else "")
val pushEnabled = googleServices.exists() && !devBuild && appIdSuffix.isEmpty()
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

if (officialRelease || providers.gradleProperty("requireSigning").orNull == "true") {
    require(signingFile.exists()) { "CI APK builds require fixed signing.properties" }
}
if (officialRelease) {
    require(pushEnabled) { "Official releases require Firebase client configuration" }
}

android {
    sourceSets.getByName("test").java.srcDir(rootProject.file("../testdata/projection/kotlin"))
    namespace = "io.github.lewin671.remoteagent"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.github.lewin671.remoteagent"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        minSdk = 26
        targetSdk = 35
        versionCode = product.getProperty("androidVersionCode").toInt()
        versionName = product.getProperty("version") + if (devBuild) "-dev.${buildCommit.take(7)}" else ""
        resValue("string", "build_commit", buildCommit)
        resValue("bool", "push_enabled", pushEnabled.toString())
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
        debug {
            // A fresh runner's debug key would prevent updating the previous
            // development APK. CI copies use the fixed key, with a separate ID.
            if (devBuild && signingFile.exists()) signingConfig = signingConfigs.getByName("release")
        }
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

    testOptions.unitTests.isIncludeAndroidResources = true

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures {
        compose = true
        // BuildConfig holds the version, so with it every release compiles
        // different classes. Without it a release that changes nothing
        // under android/ takes KSP's and the compiler's output from Gradle's
        // build cache. R8 still runs: its inputs include the manifest. The
        // commit and the push switch are resources instead.
        buildConfig = false
    }
    packaging {
        resources.excludes += setOf("META-INF/versions/9/OSGI-INF/MANIFEST.MF", "META-INF/DEPENDENCIES", "META-INF/LICENSE*", "META-INF/NOTICE*")
    }
}

kotlin {
    jvmToolchain(17)
}

// The Markdown and terminal history cases the clients read by live outside this build.
tasks.withType<Test>().configureEach {
    inputs.dir(file("schemas")).withPathSensitivity(PathSensitivity.RELATIVE)
    systemProperty("cacheSchemas", file("schemas").absolutePath)
    inputs.file(rootProject.file("../testdata/projection/vectors.json")).withPathSensitivity(PathSensitivity.NONE)
    systemProperty("projectionVectors", rootProject.file("../testdata/projection/vectors.json").absolutePath)
    inputs.file(rootProject.file("../testdata/schedules/clients.json")).withPathSensitivity(PathSensitivity.NONE)
    systemProperty("scheduleClients", rootProject.file("../testdata/schedules/clients.json").absolutePath)
    inputs.file(rootProject.file("../testdata/markdown/cases.json")).withPathSensitivity(PathSensitivity.NONE)
    inputs.file(rootProject.file("../testdata/terminal/history-sgr.json")).withPathSensitivity(PathSensitivity.NONE)
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
    testImplementation(libs.robolectric)
    testImplementation(platform(libs.compose.bom))
    testImplementation(libs.compose.ui.test)
    testImplementation(libs.coroutines.test)
    testImplementation(libs.serialization.json)
    androidTestImplementation(platform(libs.compose.bom))
    androidTestImplementation(libs.compose.ui.test)
    androidTestImplementation(libs.android.test.runner)
    debugImplementation(libs.compose.ui.test.manifest)
}
