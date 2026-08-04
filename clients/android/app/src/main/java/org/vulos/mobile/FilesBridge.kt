package org.vulos.mobile

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Environment
import android.provider.OpenableColumns
import android.util.Base64
import android.webkit.WebView
import androidx.core.content.FileProvider
import androidx.core.content.IntentCompat
import androidx.webkit.JavaScriptReplyProxy
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * Files bridge (FILES-01). Storage Access Framework open/save plus share-target
 * (receive) and share-out. No storage runtime permission is needed: SAF and
 * ACTION_SEND are user-mediated (the user picks the document / share), which is the
 * whole point of SAF — it grants access to exactly what the user chose and nothing
 * else.
 *
 * JS object: `vulosFiles`
 *   { id, action:"subscribe" }                           receive inbound shares
 *   { id, action:"open", mimeTypes?:[], maxBytes? }      SAF open → { name, mime, sizeBytes, dataUrl? }
 *   { id, action:"save", name, mime?, dataBase64 }       SAF create (Downloads) → { uri }
 *   { id, action:"share", text?, url?, dataBase64?, name?, mime? }  share out
 * inbound share push (subscribe channel):
 *   { event:"shareIn", text?, items:[ {uri, name, mime, sizeBytes, dataUrl?} ] }
 */
class FilesBridge(activity: MainActivity) : BridgeBase(activity) {

    @Volatile
    private var eventChannel: JavaScriptReplyProxy? = null

    // A share can cold-start the app before the shell has subscribed; buffer one so
    // it is not lost, and flush it on subscribe.
    @Volatile
    private var pendingShare: JSONObject? = null

    override fun handle(
        action: String,
        msg: JSONObject,
        rp: JavaScriptReplyProxy,
        id: String,
        view: WebView,
    ) {
        when (action) {
            "subscribe" -> {
                eventChannel = rp
                reply(rp, id, ok = true)
                pendingShare?.let { pushShareJson(it); pendingShare = null }
            }
            "open" -> openDocument(mimeArray(msg), msg.optInt("maxBytes", 20 * 1024 * 1024), rp, id)
            "save" -> saveDocument(
                msg.optString("name", "vulos-file"),
                msg.optString("mime", "application/octet-stream"),
                msg.optString("dataBase64"),
                rp, id,
            )
            "share" -> shareOut(msg, rp, id)
            else -> reply(rp, id, error = "unknown-action")
        }
    }

    override fun shutdown() {
        eventChannel = null
        super.shutdown()
    }

    // ── inbound share target (called by MainActivity) ────────────────────────────
    /**
     * Handle an ACTION_SEND / ACTION_SEND_MULTIPLE intent routed from MainActivity.
     * Copies streamed items into cache (the sender's read grant is transient) and
     * forwards them to the subscribed shell, buffering if it has not subscribed yet.
     */
    fun onShareIntent(intent: Intent) {
        io.execute {
            try {
                val text = intent.getStringExtra(Intent.EXTRA_TEXT)
                val uris = ArrayList<Uri>()
                when (intent.action) {
                    Intent.ACTION_SEND ->
                        IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java)
                            ?.let { uris.add(it) }
                    Intent.ACTION_SEND_MULTIPLE ->
                        IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java)
                            ?.let { uris.addAll(it) }
                }
                val items = JSONArray()
                for (u in uris) items.put(copyInbound(u))
                val payload = JSONObject().put("event", "shareIn")
                if (!text.isNullOrBlank()) payload.put("text", text)
                payload.put("items", items)

                val ch = eventChannel
                if (ch != null) pushShareJson(payload) else pendingShare = payload
            } catch (_: Exception) {
                // A malformed share must never crash the app.
            }
        }
    }

    private fun copyInbound(uri: Uri): JSONObject {
        val (name, size, mime) = queryMeta(uri)
        val dir = File(activity.cacheDir, "shared").apply { mkdirs() }
        val dest = File(dir, "in_${System.currentTimeMillis()}_${name.take(60)}")
        activity.contentResolver.openInputStream(uri)?.use { input ->
            dest.outputStream().use { input.copyTo(it) }
        }
        val outUri = FileProvider.getUriForFile(activity, activity.packageName + ".fileprovider", dest)
        return JSONObject()
            .put("uri", outUri.toString())
            .put("name", name)
            .put("mime", mime ?: activity.contentResolver.getType(uri) ?: "application/octet-stream")
            .put("sizeBytes", if (dest.exists()) dest.length() else size)
    }

    private fun pushShareJson(json: JSONObject) {
        val ch = eventChannel ?: return
        activity.runOnUiThread { try { ch.postMessage(json.toString()) } catch (_: Exception) {} }
    }

    // ── SAF open ─────────────────────────────────────────────────────────────────
    private fun openDocument(mimeTypes: Array<String>, maxBytes: Int, rp: JavaScriptReplyProxy, id: String) {
        // ACTION_OPEN_DOCUMENT returns a URI already granted read access for this
        // process, so no extra flag is needed.
        val intent = Intent(Intent.ACTION_OPEN_DOCUMENT)
            .addCategory(Intent.CATEGORY_OPENABLE)
            .setType(if (mimeTypes.size == 1) mimeTypes[0] else "*/*")
        if (mimeTypes.size > 1) intent.putExtra(Intent.EXTRA_MIME_TYPES, mimeTypes)
        activity.launchForResult(intent) { code, data ->
            val uri = data?.data
            if (code != Activity.RESULT_OK || uri == null) { reply(rp, id, error = "cancelled"); return@launchForResult }
            io.execute {
                try {
                    val (name, size, mime) = queryMeta(uri)
                    val out = JSONObject().put("name", name).put("sizeBytes", size)
                        .put("mime", mime ?: activity.contentResolver.getType(uri) ?: "application/octet-stream")
                    if (size in 1..maxBytes.toLong()) {
                        val bytes = activity.contentResolver.openInputStream(uri)?.use { it.readBytes() }
                        if (bytes != null) out.put("dataUrl", "data:${out.optString("mime")};base64," +
                            Base64.encodeToString(bytes, Base64.NO_WRAP))
                    }
                    reply(rp, id, ok = true, extra = out)
                } catch (e: Exception) {
                    reply(rp, id, error = e.message ?: "read-failed")
                }
            }
        }
    }

    // ── SAF save (biased to Downloads) ───────────────────────────────────────────
    private fun saveDocument(name: String, mime: String, dataBase64: String, rp: JavaScriptReplyProxy, id: String) {
        if (dataBase64.isBlank()) { reply(rp, id, error = "bad-args"); return }
        val intent = Intent(Intent.ACTION_CREATE_DOCUMENT)
            .addCategory(Intent.CATEGORY_OPENABLE)
            .setType(mime)
            .putExtra(Intent.EXTRA_TITLE, name)
        downloadsUri()?.let { intent.putExtra("android.provider.extra.INITIAL_URI", it) }
        activity.launchForResult(intent) { code, data ->
            val uri = data?.data
            if (code != Activity.RESULT_OK || uri == null) { reply(rp, id, error = "cancelled"); return@launchForResult }
            io.execute {
                try {
                    val bytes = Base64.decode(dataBase64, Base64.DEFAULT)
                    activity.contentResolver.openOutputStream(uri)?.use { it.write(bytes) }
                    reply(rp, id, ok = true, extra = JSONObject().put("uri", uri.toString()))
                } catch (e: Exception) {
                    reply(rp, id, error = e.message ?: "write-failed")
                }
            }
        }
    }

    // ── share out ────────────────────────────────────────────────────────────────
    private fun shareOut(msg: JSONObject, rp: JavaScriptReplyProxy, id: String) {
        try {
            val text = msg.optString("text").ifBlank { msg.optString("url") }
            val dataBase64 = msg.optString("dataBase64")
            val intent = Intent(Intent.ACTION_SEND)
            if (dataBase64.isNotBlank()) {
                val dir = File(activity.cacheDir, "share_out").apply { mkdirs() }
                val f = File(dir, msg.optString("name", "vulos-share"))
                f.writeBytes(Base64.decode(dataBase64, Base64.DEFAULT))
                val uri = FileProvider.getUriForFile(activity, activity.packageName + ".fileprovider", f)
                intent.setType(msg.optString("mime", "application/octet-stream"))
                    .putExtra(Intent.EXTRA_STREAM, uri)
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                if (text.isNotBlank()) intent.putExtra(Intent.EXTRA_TEXT, text)
            } else {
                if (text.isBlank()) { reply(rp, id, error = "bad-args"); return }
                intent.setType("text/plain").putExtra(Intent.EXTRA_TEXT, text)
            }
            activity.startActivity(
                Intent.createChooser(intent, "Share").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
            reply(rp, id, ok = true)
        } catch (e: Exception) {
            reply(rp, id, error = e.message ?: "share-failed")
        }
    }

    // ── helpers ──────────────────────────────────────────────────────────────────
    private fun mimeArray(msg: JSONObject): Array<String> {
        val arr = msg.optJSONArray("mimeTypes") ?: return arrayOf("*/*")
        val out = ArrayList<String>()
        for (i in 0 until arr.length()) arr.optString(i).takeIf { it.isNotBlank() }?.let { out.add(it) }
        return if (out.isEmpty()) arrayOf("*/*") else out.toTypedArray()
    }

    private fun queryMeta(uri: Uri): Triple<String, Long, String?> {
        var name = "file"
        var size = -1L
        activity.contentResolver.query(uri, null, null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                val ni = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                val si = c.getColumnIndex(OpenableColumns.SIZE)
                if (ni >= 0) c.getString(ni)?.let { name = it }
                if (si >= 0 && !c.isNull(si)) size = c.getLong(si)
            }
        }
        return Triple(name, size, activity.contentResolver.getType(uri))
    }

    private fun downloadsUri(): Uri? =
        try {
            Uri.fromFile(Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS))
        } catch (_: Exception) { null }
}
