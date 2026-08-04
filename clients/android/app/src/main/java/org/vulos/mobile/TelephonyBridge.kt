package org.vulos.mobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.CallLog
import android.provider.Telephony
import android.telephony.SmsManager
import android.webkit.WebView
import androidx.core.content.ContextCompat
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Origin-gated native telephony bridge (TEL-01). Reached ONLY from trusted Vulos
 * origins: it is registered via WebViewCompat.addWebMessageListener with
 * allowed-origin rules (see MainActivity.bridgeOriginRules), so remote /
 * untrusted web content cannot invoke it. Each action additionally requires the
 * MAIN frame (a trusted page's cross-origin iframe cannot act) AND a runtime
 * permission grant. A telephony bridge exposed to arbitrary web content would be
 * a serious hole; this is the whole reason it uses the origin-restricted listener
 * rather than a blanket addJavascriptInterface.
 *
 * Protocol — JS→native (JSON via `vulosTelephony.postMessage(...)`):
 *   { id, action:"subscribe" }                 register for inbound-SMS pushes
 *   { id, action:"perms" }                     report granted permissions
 *   { id, action:"sms.send", to, text }        send SMS            (SEND_SMS)
 *   { id, action:"sms.list", limit? }          read recent SMS     (READ_SMS)
 *   { id, action:"call.dial", number }         place a call        (CALL_PHONE)
 *   { id, action:"calllog.list", limit? }      read call history   (READ_CALL_LOG)
 * native→JS (reply proxy): { id, ok, ... } or { id, error }
 * inbound SMS push (subscribe channel): { event:"sms", from, body, timestamp }
 *
 * Calling is deliberately place-call + call-log only; the SYSTEM dialer owns the
 * in-call UI, so emergency calls (112/10111) stay safe — we never take ROLE_DIALER.
 */
class TelephonyBridge(private val activity: MainActivity) : WebViewCompat.WebMessageListener {

    private val io = Executors.newSingleThreadExecutor()

    @Volatile
    private var eventChannel: JavaScriptReplyProxy? = null

    override fun onPostMessage(
        view: WebView,
        message: WebMessageCompat,
        sourceOrigin: Uri,
        isMainFrame: Boolean,
        replyProxy: JavaScriptReplyProxy,
    ) {
        // WebMessageCompat.getData() THROWS if the page ever posts a non-string
        // (ArrayBuffer) payload — guard the access itself, not just the JSON parse,
        // or a trusted-origin bug/compromise could crash the app on the UI thread.
        val data = try { message.data } catch (_: Exception) { null } ?: return
        val msg = try { JSONObject(data) } catch (_: Exception) { return }
        val id = msg.optString("id")

        // Origin is already restricted by the listener's allowed-origin rules;
        // require the MAIN frame too so a trusted page's iframe can't act.
        if (!isMainFrame) { reply(replyProxy, id, error = "not-main-frame"); return }

        when (msg.optString("action")) {
            // RECEIVE_SMS gates DELIVERY of the SMS_RECEIVED broadcast, so subscribing
            // for inbound SMS must actually hold it — otherwise the receiver is dead.
            "subscribe" -> withPerm(Manifest.permission.RECEIVE_SMS, replyProxy, id) {
                eventChannel = replyProxy; reply(replyProxy, id, ok = true)
            }
            "perms" -> reply(replyProxy, id, ok = true, extra = permStatus())
            "sms.send" -> withPerm(Manifest.permission.SEND_SMS, replyProxy, id) {
                sendSms(msg.optString("to"), msg.optString("text"), replyProxy, id)
            }
            "sms.list" -> withPerm(Manifest.permission.READ_SMS, replyProxy, id) {
                io.execute {
                    val messages = readSms(msg.optInt("limit", 200))
                    reply(replyProxy, id, ok = true, extra = JSONObject().put("messages", messages))
                }
            }
            "call.dial" -> withPerm(Manifest.permission.CALL_PHONE, replyProxy, id) {
                dial(msg.optString("number"), replyProxy, id)
            }
            "calllog.list" -> withPerm(Manifest.permission.READ_CALL_LOG, replyProxy, id) {
                io.execute {
                    val calls = readCallLog(msg.optInt("limit", 200))
                    reply(replyProxy, id, ok = true, extra = JSONObject().put("calls", calls))
                }
            }
            else -> reply(replyProxy, id, error = "unknown-action")
        }
    }

    /** Release the background executor (call from MainActivity.onDestroy). */
    fun shutdown() {
        io.shutdownNow()
        eventChannel = null
    }

    /** Push an inbound SMS to the subscribed shell (called by MainActivity). */
    fun pushSms(from: String, body: String, timestampMillis: Long) {
        val ch = eventChannel ?: return
        val json = JSONObject()
            .put("event", "sms").put("from", from).put("body", body).put("timestamp", timestampMillis)
        activity.runOnUiThread { try { ch.postMessage(json.toString()) } catch (_: Exception) {} }
    }

    // ── actions ─────────────────────────────────────────────────────────────
    private fun sendSms(to: String, text: String, rp: JavaScriptReplyProxy, id: String) {
        if (to.isBlank() || text.isEmpty()) { reply(rp, id, error = "bad-args"); return }
        try {
            val sms = smsManager()
            val parts = sms.divideMessage(text)
            if (parts.size > 1) sms.sendMultipartTextMessage(to, null, parts, null, null)
            else sms.sendTextMessage(to, null, text, null, null)
            reply(rp, id, ok = true)
        } catch (e: Exception) {
            reply(rp, id, error = e.message ?: "send-failed")
        }
    }

    private fun dial(number: String, rp: JavaScriptReplyProxy, id: String) {
        if (number.isBlank()) { reply(rp, id, error = "bad-args"); return }
        try {
            val intent = Intent(Intent.ACTION_CALL, Uri.parse("tel:" + Uri.encode(number)))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            activity.startActivity(intent)
            reply(rp, id, ok = true)
        } catch (e: SecurityException) {
            // ACTION_CALL to an emergency number from a non-default app is blocked by
            // the system — that is intentional; the system dialer handles those.
            reply(rp, id, error = "emergency-or-blocked")
        } catch (e: Exception) {
            reply(rp, id, error = e.message ?: "dial-failed")
        }
    }

    private fun readSms(limit: Int): JSONArray {
        val out = JSONArray()
        val cols = arrayOf(Telephony.Sms.ADDRESS, Telephony.Sms.BODY, Telephony.Sms.DATE, Telephony.Sms.TYPE)
        activity.contentResolver.query(
            Telephony.Sms.CONTENT_URI, cols, null, null,
            Telephony.Sms.DATE + " DESC LIMIT " + limit.coerceIn(1, 1000),
        )?.use { c ->
            val ai = c.getColumnIndex(Telephony.Sms.ADDRESS)
            val bi = c.getColumnIndex(Telephony.Sms.BODY)
            val di = c.getColumnIndex(Telephony.Sms.DATE)
            val ti = c.getColumnIndex(Telephony.Sms.TYPE)
            while (c.moveToNext()) {
                out.put(
                    JSONObject()
                        .put("address", if (ai >= 0) c.getString(ai) ?: "" else "")
                        .put("body", if (bi >= 0) c.getString(bi) ?: "" else "")
                        .put("date", if (di >= 0) c.getLong(di) else 0L)
                        .put("incoming", ti >= 0 && c.getInt(ti) == Telephony.Sms.MESSAGE_TYPE_INBOX)
                )
            }
        }
        return out
    }

    private fun readCallLog(limit: Int): JSONArray {
        val out = JSONArray()
        val cols = arrayOf(CallLog.Calls.NUMBER, CallLog.Calls.TYPE, CallLog.Calls.DATE, CallLog.Calls.DURATION)
        activity.contentResolver.query(
            CallLog.Calls.CONTENT_URI, cols, null, null,
            CallLog.Calls.DATE + " DESC LIMIT " + limit.coerceIn(1, 1000),
        )?.use { c ->
            val ni = c.getColumnIndex(CallLog.Calls.NUMBER)
            val ti = c.getColumnIndex(CallLog.Calls.TYPE)
            val di = c.getColumnIndex(CallLog.Calls.DATE)
            val ui = c.getColumnIndex(CallLog.Calls.DURATION)
            while (c.moveToNext()) {
                out.put(
                    JSONObject()
                        .put("number", if (ni >= 0) c.getString(ni) ?: "" else "")
                        .put("date", if (di >= 0) c.getLong(di) else 0L)
                        .put("durationSec", if (ui >= 0) c.getLong(ui) else 0L)
                        .put(
                            "direction",
                            when (if (ti >= 0) c.getInt(ti) else 0) {
                                CallLog.Calls.INCOMING_TYPE -> "incoming"
                                CallLog.Calls.OUTGOING_TYPE -> "outgoing"
                                CallLog.Calls.MISSED_TYPE -> "missed"
                                else -> "other"
                            }
                        )
                )
            }
        }
        return out
    }

    // ── helpers ─────────────────────────────────────────────────────────────
    @Suppress("DEPRECATION")
    private fun smsManager(): SmsManager =
        activity.getSystemService(SmsManager::class.java) ?: SmsManager.getDefault()

    private inline fun withPerm(
        permission: String,
        rp: JavaScriptReplyProxy,
        id: String,
        crossinline body: () -> Unit,
    ) {
        if (ContextCompat.checkSelfPermission(activity, permission) == PackageManager.PERMISSION_GRANTED) {
            body(); return
        }
        activity.ensurePermission(permission) { granted ->
            if (granted) body() else reply(rp, id, error = "permission-denied")
        }
    }

    private fun permStatus(): JSONObject {
        fun g(p: String) = ContextCompat.checkSelfPermission(activity, p) == PackageManager.PERMISSION_GRANTED
        return JSONObject()
            .put("sendSms", g(Manifest.permission.SEND_SMS))
            .put("readSms", g(Manifest.permission.READ_SMS))
            .put("receiveSms", g(Manifest.permission.RECEIVE_SMS))
            .put("call", g(Manifest.permission.CALL_PHONE))
            .put("callLog", g(Manifest.permission.READ_CALL_LOG))
    }

    private fun reply(
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
