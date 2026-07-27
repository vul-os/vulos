package org.vulos.mobile

import android.Manifest
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.os.Build
import android.webkit.WebView
import androidx.core.app.NotificationCompat
import androidx.webkit.JavaScriptReplyProxy
import org.json.JSONObject

/**
 * Notifications + foreground-service bridge (NOTIFY-01).
 *
 * JS object: `vulosNotify`
 *   { id, action:"perms" }                              report POST_NOTIFICATIONS
 *   { id, action:"service.enable" }                     start the box foreground service
 *   { id, action:"service.disable" }                    stop it
 *   { id, action:"service.status" }                     { running }
 *   { id, action:"alert", title, text, tag?, ongoing? } post a user alert
 *
 * Contextual: POST_NOTIFICATIONS (Android 13+) is requested on first
 * `service.enable` / `alert`; on denial the shell keeps working — the box is still
 * reachable while foregrounded, only the background notification is lost. Pre-13
 * the permission is auto-granted, so the flow is unchanged there.
 */
class NotifyBridge(activity: MainActivity) : BridgeBase(activity) {

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
                extra = JSONObject().put("postNotifications", canNotify()),
            )
            "service.enable" -> withNotifyPerm(rp, id) {
                BoxConnectionService.start(activity)
                reply(rp, id, ok = true)
            }
            "service.disable" -> {
                BoxConnectionService.stop(activity)
                reply(rp, id, ok = true)
            }
            "service.status" -> reply(
                rp, id, ok = true,
                extra = JSONObject().put("running", BoxConnectionService.running),
            )
            "alert" -> withNotifyPerm(rp, id) {
                postAlert(
                    msg.optString("title", "Vulos"),
                    msg.optString("text"),
                    msg.optString("tag").ifBlank { null },
                    msg.optBoolean("ongoing", false),
                    rp, id,
                )
            }
            else -> reply(rp, id, error = "unknown-action")
        }
    }

    /**
     * POST_NOTIFICATIONS only exists as a runtime permission on Android 13+; below
     * that it is implicitly granted, so we short-circuit rather than request a
     * permission the framework does not recognise.
     */
    private inline fun withNotifyPerm(rp: JavaScriptReplyProxy, id: String, crossinline body: () -> Unit) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) { body(); return }
        withPerm(Manifest.permission.POST_NOTIFICATIONS, rp, id) { body() }
    }

    private fun canNotify(): Boolean =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU)
            hasPerm(Manifest.permission.POST_NOTIFICATIONS)
        else
            true

    private fun postAlert(
        title: String,
        text: String,
        tag: String?,
        ongoing: Boolean,
        rp: JavaScriptReplyProxy,
        id: String,
    ) {
        val nm = activity.getSystemService(NotificationManager::class.java)
        if (nm == null) { reply(rp, id, error = "no-notification-manager"); return }
        val open = PendingIntent.getActivity(
            activity, 0,
            Intent(activity, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val n = NotificationCompat.Builder(activity, VulosNotifications.CHANNEL_ALERTS)
            .setContentTitle(title)
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setAutoCancel(!ongoing)
            .setOngoing(ongoing)
            .setContentIntent(open)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
        // Stable id from the tag so a re-posted alert (same tag) replaces rather than
        // stacks; distinct tags stack.
        val notifId = (tag ?: (title + text)).hashCode()
        nm.notify(tag, notifId, n)
        reply(rp, id, ok = true, extra = JSONObject().put("notificationId", notifId))
    }
}
