plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "org.vulos.mobile"
    compileSdk = 35

    defaultConfig {
        applicationId = "org.vulos.mobile"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"

        // The paired instance's default host. The app keeps in-app any https origin
        // that is a Vulos origin (see MainActivity.isVulosOrigin); everything else
        // opens in the real browser. Overridable at enrolment via SharedPreferences.
        buildConfigField("String", "INSTANCE_HOST", "\"os.vulos.org\"")
    }

    buildFeatures {
        buildConfig = true
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
        debug {
            applicationIdSuffix = ".debug"
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.3")
    // androidx.webkit is the important one: WebViewAssetLoader (local shell) and
    // ServiceWorkerControllerCompat (so the service worker's fetches resolve to
    // bundled assets — without it, offline silently fails in the APK).
    implementation("androidx.webkit:webkit:1.12.0")
}

// ── syncShell ────────────────────────────────────────────────────────────────
// Bundle the web shell (the existing Vite build output at vulos/dist/) into the
// app's assets, so nothing required to paint the first screen crosses the network
// (roadmap/OFFLINE.md's rule). Wired as a preBuild dependency so a stale copy is
// impossible. No-op when dist/ is absent — the committed placeholder ships then.
//
// The shell MUST NOT drift between tiers: this copies the SAME build the PWA
// serves rather than a duplicate maintained by hand.
val syncShell by tasks.registering(Copy::class) {
    val dist = rootProject.projectDir.parentFile.resolve("dist")
    onlyIf { dist.exists() }
    from(dist) { exclude("**/.*") }
    into(layout.projectDirectory.dir("src/main/assets/shell"))
    // Overwrite the placeholder / previous copy every build.
    duplicatesStrategy = DuplicatesStrategy.INCLUDE
}
tasks.named("preBuild") { dependsOn(syncShell) }
