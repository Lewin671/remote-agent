// Termux's terminal-emulator and terminal-view (Apache License 2.0, see
// LICENSE), vendored from termux-app v0.118.3
// (5b657c6adf4304e5198951ce815fe0205dcac29c). Only TerminalSession is
// changed to use a remote byte stream instead of a local PTY (no JNI).
// TerminalView also preserves the scroll position during remote output.
plugins {
    alias(libs.plugins.android.library)
}

android {
    namespace = "com.termux.view"
    compileSdk = 36
    defaultConfig {
        minSdk = 26
        consumerProguardFiles("consumer-rules.pro")
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    testOptions { unitTests.isReturnDefaultValues = true }
    lint {
        abortOnError = false
    }
}

dependencies {
    implementation(libs.annotation)
    testImplementation(libs.junit)
}
