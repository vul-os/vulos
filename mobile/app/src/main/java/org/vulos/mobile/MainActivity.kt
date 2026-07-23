package org.vulos.mobile

import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.webkit.GeolocationPermissions
import android.webkit.PermissionRequest
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.webkit.ServiceWorkerClientCompat
import androidx.webkit.ServiceWorkerControllerCompat
import androidx.webkit.WebViewAssetLoader
import androidx.webkit.WebViewClientCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature

/**
 * MainActivity — the Tier-2 (APK) frame around the Vulos web shell.
 *
 * See mobile/DECISIONS.md + mobile/BUILD.md. The shell is loaded LOCALLY from
 * bundled assets (nothing required to paint the first screen crosses the
 * network); app content, data and sync load from the paired instance over the
 * network. Chrome is local, content is remote.
 *
 * Deliberately NOT Compose — the UI is the web shell. Kotlin is a thin frame.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView

    /** Locally-bundled shell entry point, served by the asset loader below. */
    private val localShell = "https://$ASSET_HOST/assets/shell/index.html"

    // ── File chooser (for <input type=file> in the shell: attachments, avatars) ──
    private var filePathCallback: ValueCallback<Array<Uri>>? = null
    private val fileChooser =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            val cb = filePathCallback ?: return@registerForActivityResult
            filePathCallback = null
            cb.onReceiveValue(
                WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data)
            )
        }

    // ── Telephony (TEL-01): origin-gated SMS + calling bridge. ───────────────────
    private val telephony = TelephonyBridge(this)

    // Runtime permission plumbing for the bridge. A single ActivityResultLauncher
    // can only have ONE request in flight, so overlapping requests are SERIALIZED
    // through a queue — each keeps its own callback, so no reply is ever dropped
    // (concurrent JS-side dispatch of sms.send + call.dial won't clobber each other).
    private val permQueue = ArrayDeque<Pair<String, (Boolean) -> Unit>>()
    private var permCurrent: Pair<String, (Boolean) -> Unit>? = null
    private val requestPerm =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            val cur = permCurrent
            permCurrent = null
            cur?.second?.invoke(granted)
            pumpPermQueue()
        }

    /** Grant-or-request `permission`, then deliver the result. Called by the bridge. */
    fun ensurePermission(permission: String, onResult: (Boolean) -> Unit) {
        if (ContextCompat.checkSelfPermission(this, permission) == PackageManager.PERMISSION_GRANTED) {
            onResult(true); return
        }
        permQueue.addLast(permission to onResult)
        pumpPermQueue()
    }

    private fun pumpPermQueue() {
        if (permCurrent != null) return
        val next = permQueue.removeFirstOrNull() ?: return
        permCurrent = next
        requestPerm.launch(next.first)
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val assetLoader = WebViewAssetLoader.Builder()
            .setDomain(ASSET_HOST)
            .addPathHandler("/assets/", WebViewAssetLoader.AssetsPathHandler(this))
            .build()

        // Service-worker fetches DO NOT go through WebViewClient. Without this, the
        // shell's service worker can't resolve bundled assets and offline silently
        // fails for everything it caches. This is the step that is easy to miss.
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
            // Leave multiple-windows OFF: a target=_blank / window.open then
            // navigates in-place and is routed by shouldOverrideUrlLoading (in-app
            // for Vulos origins, the real browser otherwise). Turning it on would
            // require onCreateWindow handling or _blank links silently do nothing.
            // The shell owns its own text sizing; don't let the system font scale
            // reflow it unexpectedly.
            settings.textZoom = 100

            webViewClient = VulosWebViewClient(assetLoader)
            webChromeClient = VulosChromeClient()
        }
        setContentView(webView)

        // Expose the telephony bridge ONLY to trusted Vulos origins (framework-level
        // origin restriction — remote/untrusted content can't reach it). The shell
        // talks to it via the injected `vulosTelephony` object (postMessage).
        if (WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) {
            WebViewCompat.addWebMessageListener(
                webView, "vulosTelephony", telephonyOriginRules(), telephony,
            )
        }

        // Predictive back: walk the SPA history, then leave.
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (webView.canGoBack()) webView.goBack() else finish()
            }
        })

        if (savedInstanceState == null) webView.loadUrl(localShell)
    }

    // Route inbound SMS to the shell only while foregrounded (the bridge does the
    // origin gating; the subscribe channel must exist for it to land anywhere).
    override fun onResume() {
        super.onResume()
        TelephonyEvents.onSms = { from, body, ts -> telephony.pushSms(from, body, ts) }
    }

    override fun onPause() {
        super.onPause()
        TelephonyEvents.onSms = null
    }

    override fun onDestroy() {
        // Tear down the WebView and the bridge's background executor so an activity
        // recreation (e.g. a locale change not covered by configChanges) doesn't leak
        // the old instance's thread + WebView.
        telephony.shutdown()
        TelephonyEvents.onSms = null
        webView.destroy()
        super.onDestroy()
    }

    /**
     * Origins allowed to reach the telephony bridge: the local shell, `os.vulos.org`
     * and its subdomains, the build-time instance host and the paired instance.
     * The `*.` wildcard matches subdomains only (never a suffix like
     * `evil-os.vulos.org`).
     */
    private fun telephonyOriginRules(): Set<String> {
        // NARROW on purpose (security): telephony (send SMS / place calls) is granted
        // ONLY to the local shell and THIS device's own instance (+ its app
        // subdomains) — never a blanket `*.os.vulos.org`, which would hand the
        // capability to every tenant/product under that domain, so one XSS anywhere
        // in the namespace becomes an SMS-send primitive. The paired instance is the
        // authoritative scope; the build-time INSTANCE_HOST is the default until
        // enrolment sets the paired host.
        val rules = linkedSetOf("https://$ASSET_HOST")
        BuildConfig.INSTANCE_HOST.takeIf { it.isNotBlank() }?.let {
            rules.add("https://$it"); rules.add("https://*.$it")
        }
        pairedInstanceHost()?.let { rules.add("https://$it"); rules.add("https://*.$it") }
        return rules
    }

    // Preserve WebView state (scroll, form, history) across process death / config
    // changes the manifest doesn't already absorb.
    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        webView.saveState(outState)
    }

    override fun onRestoreInstanceState(savedInstanceState: Bundle) {
        super.onRestoreInstanceState(savedInstanceState)
        webView.restoreState(savedInstanceState)
    }

    // ── WebViewClient — routing + local asset interception ──────────────────────
    private inner class VulosWebViewClient(
        private val loader: WebViewAssetLoader,
    ) : WebViewClientCompat() {

        override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest) =
            loader.shouldInterceptRequest(request.url)

        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            val url = request.url
            return when (url.scheme?.lowercase()) {
                // WebView cannot handle these schemes itself — hand off to the system
                // apps (dialer / messaging / mail / maps). This is the ONLY telephony
                // the APK does in v1 (MOB-07): it opens a prefilled draft/dialer; it
                // never reads or sends anything itself.
                "tel", "sms", "smsto", "mms", "mailto", "geo" -> {
                    openExternally(Intent(Intent.ACTION_VIEW, url)); true
                }
                // Keep our own origins in-app; send everything else to the real
                // browser so an off-instance link can't run inside our WebView.
                // (Asset requests use https://appassets.androidplatform.net and are
                // handled by the loader, so they satisfy isVulosOrigin here.)
                "https" -> if (isVulosOrigin(url)) false else {
                    openExternally(Intent(Intent.ACTION_VIEW, url)); true
                }
                else -> true // fail closed on everything else (http/cleartext, intent:, javascript:, file:, …)
            }
        }
    }

    private fun openExternally(intent: Intent) {
        try {
            startActivity(intent)
        } catch (_: ActivityNotFoundException) {
            // No app to handle it (e.g. no dialer on a tablet) — swallow rather than crash.
        }
    }

    // ── ChromeClient — file chooser + capability prompts ────────────────────────
    private inner class VulosChromeClient : WebChromeClient() {
        override fun onShowFileChooser(
            webView: WebView,
            callback: ValueCallback<Array<Uri>>,
            params: FileChooserParams,
        ): Boolean {
            filePathCallback?.onReceiveValue(null)
            filePathCallback = callback
            return try {
                fileChooser.launch(params.createIntent()); true
            } catch (_: ActivityNotFoundException) {
                filePathCallback = null; false
            }
        }

        // Camera/mic (Meet, Talk) and geolocation (Maps) need Android runtime
        // permissions + manifest declarations we deliberately do NOT ship in the v1
        // frame. Deny for now rather than crash; wiring them is a scoped follow-up
        // (declare CAMERA/RECORD_AUDIO/ACCESS_FINE_LOCATION, request at runtime,
        // then grant here). NOTE (MOB-06): Web Push does not work in a WebView at
        // all — notifications are the reason Tier 2 is deferred, not a bug here.
        override fun onPermissionRequest(request: PermissionRequest) {
            request.deny()
        }

        override fun onGeolocationPermissionsShowPrompt(
            origin: String?,
            callback: GeolocationPermissions.Callback?,
        ) {
            callback?.invoke(origin, false, false)
        }
    }

    /**
     * True for origins we keep inside the WebView: the local asset host, any
     * `*.os.vulos.org` app origin, the build-time default instance host, and the
     * instance this device was paired with at enrolment (stored in prefs).
     */
    private fun isVulosOrigin(url: Uri): Boolean {
        val host = url.host?.lowercase() ?: return false
        if (host == ASSET_HOST) return true
        if (host == "os.vulos.org" || host.endsWith(".os.vulos.org")) return true
        if (host == BuildConfig.INSTANCE_HOST) return true
        val paired = pairedInstanceHost()
        return paired != null && (host == paired || host.endsWith(".$paired"))
    }

    /** The instance host this device enrolled with, if any (set during pairing). */
    private fun pairedInstanceHost(): String? =
        getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .getString(KEY_INSTANCE_HOST, null)
            ?.lowercase()

    companion object {
        // WebViewAssetLoader's reserved virtual host for bundled assets — never a
        // real network host; requests to it are served from src/main/assets.
        private const val ASSET_HOST = "appassets.androidplatform.net"
        private const val PREFS = "vulos"
        private const val KEY_INSTANCE_HOST = "instance_host"
    }
}
