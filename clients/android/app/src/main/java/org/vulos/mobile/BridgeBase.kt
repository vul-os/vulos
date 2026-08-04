package org.vulos.mobile

import android.content.pm.PackageManager
import android.net.Uri
import android.webkit.WebView
import androidx.core.content.ContextCompat
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Shared plumbing for the origin-gated native bridges (Contacts, Camera, Notify,
 * Files, Biometric, Launcher). Mirrors the security model of [TelephonyBridge]:
 *
 *  - Each bridge is registered via WebViewCompat.addWebMessageListener with the
 *    SAME narrow allowed-origin rules (see MainActivity.bridgeOriginRules), so
 *    remote / untrusted web content can never invoke it. This is deliberately the
 *    origin-restricted WebMessageListener and NOT a blanket addJavascriptInterface
 *    (which cannot restrict by origin and leaks to every frame).
 *  - Every message additionally requires the MAIN frame — a trusted page's
 *    cross-origin iframe cannot act.
 *  - Sensitive actions go through [withPerm], which requests the Android runtime
 *    permission CONTEXTUALLY on first use and fails closed ("permission-denied")
 *    if the user declines, so the PWA fallback still works.
 *
 * Protocol is identical to the telephony bridge:
 *   JS→native  : { id, action, ...args }  via `vulosX.postMessage(JSON.stringify(...))`
 *   native→JS  : { id, ok, ... } or { id, error }
 *   push events: { event, ... }           on a subscribed reply channel
 */
abstract class BridgeBase(protected val activity: MainActivity) :
    WebViewCompat.WebMessageListener {

    /** Per-bridge single background thread for ContentResolver / IO work. */
    protected val io = Executors.newSingleThreadExecutor()

    final override fun onPostMessage(
        view: WebView,
        message: WebMessageCompat,
        sourceOrigin: Uri,
        isMainFrame: Boolean,
        replyProxy: JavaScriptReplyProxy,
    ) {
        // getData() THROWS if the page ever posts a non-string (ArrayBuffer)
        // payload — guard the access itself, not just the parse, so a trusted-origin
        // bug/compromise can't crash the app on the UI thread.
        val data = try { message.data } catch (_: Exception) { null } ?: return
        val msg = try { JSONObject(data) } catch (_: Exception) { return }
        val id = msg.optString("id")

        // Origin is already restricted by the listener rules; require the MAIN frame
        // too so a trusted page's iframe can't reach native capabilities.
        if (!isMainFrame) { reply(replyProxy, id, error = "not-main-frame"); return }

        try {
            handle(msg.optString("action"), msg, replyProxy, id, view)
        } catch (e: Exception) {
            reply(replyProxy, id, error = e.message ?: "bridge-error")
        }
    }

    /** Handle one action. Implementations reply via [reply]. */
    protected abstract fun handle(
        action: String,
        msg: JSONObject,
        rp: JavaScriptReplyProxy,
        id: String,
        view: WebView,
    )

    /** Release the background executor (called from MainActivity.onDestroy). */
    open fun shutdown() {
        io.shutdownNow()
    }

    protected fun hasPerm(permission: String): Boolean =
        ContextCompat.checkSelfPermission(activity, permission) == PackageManager.PERMISSION_GRANTED

    /**
     * Contextual permission gate: run [body] if already granted, otherwise request
     * `permission` at runtime NOW and run [body] only on grant; on denial reply
     * "permission-denied" so the shell can fall back to its PWA path.
     */
    protected inline fun withPerm(
        permission: String,
        rp: JavaScriptReplyProxy,
        id: String,
        crossinline body: () -> Unit,
    ) {
        if (hasPerm(permission)) { body(); return }
        activity.ensurePermission(permission) { granted ->
            if (granted) body() else reply(rp, id, error = "permission-denied")
        }
    }

    protected fun reply(
        rp: JavaScriptReplyProxy,
        id: String,
        ok: Boolean? = null,
        error: String? = null,
        extra: JSONObject? = null,
    ) {
        val o = extra ?: JSONObject()
        o.put("id", id)
        if (ok != null) o.put("ok", ok)
        if (error != null) o.put("error", error)
        activity.runOnUiThread { try { rp.postMessage(o.toString()) } catch (_: Exception) {} }
    }
}
