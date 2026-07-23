# Building Vulos Mobile (Tier 2 — APK)

> **You probably do not need this yet.** Tier 1 (PWA) is the shipping path and requires zero code here.
> See [DECISIONS.md § MOB-02](DECISIONS.md#mob-02--two-delivery-tiers-one-shell-pwa-first). This document
> exists so that when Tier 2 is justified, it is a day of work rather than a week of research.

**Language: Kotlin** ([MOB-03](DECISIONS.md#mob-03--kotlin-not-java)).

---

## Easiest path

1. **Android Studio** → New Project → **Empty Views Activity** (not Compose — the UI is the web shell)
2. Language **Kotlin**, build DSL **Kotlin (`.kts`)**
3. `minSdk = 26`, `targetSdk` = current
4. Package `org.vulos.mobile`, project location `vulos/mobile/app/`
5. Replace the generated files with the boilerplate below

Then `./gradlew assembleDebug`. That is the whole loop.

---

## Dependencies

`app/build.gradle.kts` — check for newer versions, these are indicative:

```kotlin
dependencies {
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.3")
    implementation("androidx.webkit:webkit:1.12.0")   // WebViewAssetLoader + ServiceWorker compat
}
```

`androidx.webkit` is the important one — it provides `WebViewAssetLoader` and
`ServiceWorkerControllerCompat`, without which local assets and offline both fail.

---

## `AndroidManifest.xml`

```xml
<manifest xmlns:android="http://schemas.android.com/apk/res/android">

    <uses-permission android:name="android.permission.INTERNET" />

    <application
        android:label="Vulos"
        android:icon="@mipmap/ic_launcher"
        android:usesCleartextTraffic="false"
        android:theme="@style/Theme.Vulos">

        <activity
            android:name=".MainActivity"
            android:exported="true"
            android:configChanges="orientation|screenSize|keyboardHidden|smallestScreenSize|screenLayout"
            android:windowSoftInputMode="adjustResize">

            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
            </intent-filter>

            <!-- DEFERRED (MOB-05): uncommenting this makes Vulos the home screen.
                 Do NOT enable until the home surface paints from local cache with
                 no network call, the WebView is pre-warmed in Application.onCreate(),
                 and windowBackground is a home-screen-shaped drawable.
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.HOME" />
                <category android:name="android.intent.category.DEFAULT" />
            </intent-filter>
            -->
        </activity>
    </application>
</manifest>
```

`configChanges` prevents the WebView being destroyed and reloaded on rotation.
`adjustResize` keeps the soft keyboard from covering inputs — pair it with `visualViewport`
handling on the web side, not `resize`.

---

## `MainActivity.kt`

```kotlin
package org.vulos.mobile

import android.annotation.SuppressLint
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.webkit.*
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity
import androidx.webkit.*

class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView

    /** Locally-bundled shell. Nothing required to paint the first screen crosses the network. */
    private val localShell = "https://appassets.androidplatform.net/assets/shell/index.html"

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val assetLoader = WebViewAssetLoader.Builder()
            .setDomain("appassets.androidplatform.net")
            .addPathHandler("/assets/", WebViewAssetLoader.AssetsPathHandler(this))
            .build()

        // Service worker fetches DO NOT go through WebViewClient. Without this, offline
        // silently fails for every request the SW makes. This is the step people miss.
        if (WebViewFeature.isFeatureSupported(WebViewFeature.SERVICE_WORKER_BASIC_USAGE)) {
            ServiceWorkerControllerCompat.getInstance().setServiceWorkerClient(
                object : ServiceWorkerClientCompat() {
                    override fun shouldInterceptRequest(request: WebResourceRequest) =
                        assetLoader.shouldInterceptRequest(request.url)
                }
            )
        }

        webView = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true          // localStorage AND IndexedDB
            settings.databaseEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            settings.setSupportMultipleWindows(true)

            webViewClient = VulosWebViewClient(assetLoader)
            webChromeClient = WebChromeClient()        // extend for file chooser + permissions
        }
        setContentView(webView)

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (webView.canGoBack()) webView.goBack() else finish()
            }
        })

        webView.loadUrl(localShell)
    }

    private inner class VulosWebViewClient(
        private val loader: WebViewAssetLoader
    ) : WebViewClientCompat() {

        override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest) =
            loader.shouldInterceptRequest(request.url)

        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            val url = request.url
            return when (url.scheme) {
                // WebView cannot handle these schemes itself — hand off to the system apps.
                // This is the ONLY telephony the APK does in v1 (MOB-07).
                "tel", "sms", "smsto", "mailto", "geo" -> {
                    startActivity(Intent(Intent.ACTION_VIEW, url)); true
                }
                // Keep our own origins in-app; everything else goes to the real browser.
                "https" -> if (isVulosOrigin(url)) false else {
                    startActivity(Intent(Intent.ACTION_VIEW, url)); true
                }
                else -> true   // fail closed on unknown schemes
            }
        }
    }

    /** TODO: read the paired instance origin from SharedPreferences, set during enrolment. */
    private fun isVulosOrigin(url: Uri): Boolean {
        val host = url.host ?: return false
        return host == "appassets.androidplatform.net" ||
               host.endsWith(".os.vulos.org") ||
               host == BuildConfig.INSTANCE_HOST
    }
}
```

---

## Wiring the shell in

The web shell is built by the existing Vite pipeline. The APK ships that build output as Android assets:

```
app/src/main/assets/shell/     ← copy of the built shell (index.html, JS, CSS, SW)
```

Add a Gradle task that copies `dist/` in rather than committing a duplicate — the shell must never drift
between tiers. Wire it as a `preBuild` dependency so a stale copy is impossible.

**Everything else — app content, data, sync — loads from the paired instance over the network.**
Chrome is local, content is remote.

---

## CI

A workflow scoped to `mobile/**` running `./gradlew assembleRelease`, with the signing key in secrets.

⚠️ Keep the OS image build (`build.sh`, `Makefile`) on an **explicit include list** so `mobile/` never
enters the roothash-signed surface. Verify this before the first APK lands, not after.

---

## Gotchas, in the order you will hit them

| Symptom | Cause |
|---|---|
| Offline works in Chrome, fails in the APK | Service worker fetches bypass `WebViewClient` — you need `ServiceWorkerClientCompat` |
| IndexedDB silently empty | `domStorageEnabled` not set |
| `tel:`/`sms:` links do nothing | Not handled in `shouldOverrideUrlLoading` — WebView has no default handler |
| Keyboard covers the compose box | Web side listening to `resize` instead of `visualViewport` |
| WebView reloads on rotation | Missing `configChanges` |
| Predictive back looks broken | No `OnBackPressedCallback` bridged to web history |
| **No notifications at all** | Expected — WebView has no Push API. See [MOB-06](DECISIONS.md#mob-06--push-the-apks-real-cost) |

That last row is not a bug to fix in this file. It is the reason Tier 2 is deferred.
