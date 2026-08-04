package org.vulos.mobile

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.provider.MediaStore
import android.util.Base64
import android.webkit.WebView
import androidx.core.content.FileProvider
import androidx.webkit.JavaScriptReplyProxy
import com.google.zxing.integration.android.IntentIntegrator
import com.google.zxing.integration.android.IntentResult
import org.json.JSONObject
import java.io.File

/**
 * Camera bridge (CAM-01). Native photo/video capture + a camera-based QR/barcode
 * scan. Delegates the actual capture to the SYSTEM camera app (ACTION_IMAGE/VIDEO
 * _CAPTURE) and the scan to the bundled ZXing scanner activity — Vulos never opens
 * the raw camera device itself, so there is no custom preview/permission surface to
 * get wrong.
 *
 * JS object: `vulosCamera`
 *   { id, action:"perms" }                 report CAMERA grant
 *   { id, action:"photo.capture", maxBytes? }   returns { dataUrl } (JPEG base64)
 *   { id, action:"video.capture" }         returns { uri, path, sizeBytes }
 *   { id, action:"qr.scan", prompt? }      returns { text, format }
 *
 * Contextual: CAMERA is requested on first capture/scan; on denial the shell keeps
 * working (it can still use the WebView <input type=file capture> fallback).
 * Because we DECLARE the CAMERA permission, the system requires it be granted for
 * ACTION_IMAGE_CAPTURE, so gating every action behind CAMERA is correct.
 */
class CameraBridge(activity: MainActivity) : BridgeBase(activity) {

    override fun handle(
        action: String,
        msg: JSONObject,
        rp: JavaScriptReplyProxy,
        id: String,
        view: WebView,
    ) {
        when (action) {
            "perms" -> reply(
                rp, id, ok = true,
                extra = JSONObject().put("camera", hasPerm(Manifest.permission.CAMERA)),
            )
            "photo.capture" -> withPerm(Manifest.permission.CAMERA, rp, id) {
                capturePhoto(msg.optInt("maxBytes", 8 * 1024 * 1024), rp, id)
            }
            "video.capture" -> withPerm(Manifest.permission.CAMERA, rp, id) {
                captureVideo(rp, id)
            }
            "qr.scan" -> withPerm(Manifest.permission.CAMERA, rp, id) {
                scanCode(msg.optString("prompt", "Scan a code"), rp, id)
            }
            else -> reply(rp, id, error = "unknown-action")
        }
    }

    private fun capturePhoto(maxBytes: Int, rp: JavaScriptReplyProxy, id: String) {
        val file = tempFile("jpg")
        val uri = fileUri(file)
        val intent = Intent(MediaStore.ACTION_IMAGE_CAPTURE)
            .putExtra(MediaStore.EXTRA_OUTPUT, uri)
            .addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
        activity.launchForResult(intent) { code, _ ->
            if (code != Activity.RESULT_OK) { file.delete(); reply(rp, id, error = "cancelled"); return@launchForResult }
            io.execute {
                try {
                    val bytes = file.readBytes()
                    if (bytes.size > maxBytes.coerceAtLeast(1)) {
                        reply(rp, id, error = "too-large")
                    } else {
                        val b64 = Base64.encodeToString(bytes, Base64.NO_WRAP)
                        reply(rp, id, ok = true, extra = JSONObject().put("dataUrl", "data:image/jpeg;base64,$b64"))
                    }
                } catch (e: Exception) {
                    reply(rp, id, error = e.message ?: "read-failed")
                } finally {
                    file.delete()
                }
            }
        }
    }

    private fun captureVideo(rp: JavaScriptReplyProxy, id: String) {
        val file = tempFile("mp4")
        val uri = fileUri(file)
        val intent = Intent(MediaStore.ACTION_VIDEO_CAPTURE)
            .putExtra(MediaStore.EXTRA_OUTPUT, uri)
            .addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
        activity.launchForResult(intent) { code, data ->
            if (code != Activity.RESULT_OK) { file.delete(); reply(rp, id, error = "cancelled"); return@launchForResult }
            // Video is returned as a content URI + size rather than base64 (base64
            // over the JS bridge is impractical for video). The shell uploads it to
            // the box via the normal file-input path using this URI.
            val out = data?.data ?: uri
            reply(
                rp, id, ok = true,
                extra = JSONObject()
                    .put("uri", out.toString())
                    .put("path", file.absolutePath)
                    .put("sizeBytes", if (file.exists()) file.length() else 0L),
            )
        }
    }

    private fun scanCode(prompt: String, rp: JavaScriptReplyProxy, id: String) {
        // zxing-android-embedded's CaptureActivity (registered by the library's merged
        // manifest) does the live camera preview + decode; we only launch its intent
        // and parse the decoded string back. Pure ZXing — no Play Services, so it
        // stays F-Droid / sovereign friendly.
        val integrator = IntentIntegrator(activity)
            .setPrompt(prompt)
            .setBeepEnabled(false)
            .setOrientationLocked(false)
        val intent = integrator.createScanIntent()
        activity.launchForResult(intent) { code, data ->
            val result: IntentResult = IntentIntegrator.parseActivityResult(code, data)
            val text = result.contents
            if (text == null) reply(rp, id, error = "cancelled")
            else reply(
                rp, id, ok = true,
                extra = JSONObject().put("text", text).put("format", result.formatName ?: ""),
            )
        }
    }

    // Capture targets live in cache/capture so the FileProvider `file_paths` entry
    // exposes exactly that subtree and nothing else.
    private fun tempFile(ext: String): File {
        val dir = File(activity.cacheDir, "capture").apply { mkdirs() }
        return File(dir, "cap_${System.currentTimeMillis()}.$ext")
    }

    private fun fileUri(file: File): Uri =
        FileProvider.getUriForFile(activity, activity.packageName + ".fileprovider", file)
}
