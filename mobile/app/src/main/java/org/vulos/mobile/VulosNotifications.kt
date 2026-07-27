package org.vulos.mobile

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build

/**
 * Notification channel definitions (NOTIFY-01), created once at app start.
 *
 *  - [CHANNEL_BOX] — the ongoing, low-importance foreground-service notification
 *    that keeps the box connection alive. Silent; it is a presence indicator.
 *  - [CHANNEL_ALERTS] — default-importance user-facing alerts: a missed SMS/call
 *    the shell surfaces, or a box event. The user can mute this channel in system
 *    settings without killing the connection channel.
 *
 * Web Push does NOT work inside a WebView (see MOB-06), so these native channels
 * are how the APK tier delivers reliable notifications the PWA gets from Web Push.
 */
object VulosNotifications {
    const val CHANNEL_BOX = "box_connection"
    const val CHANNEL_ALERTS = "alerts"
    const val FGS_NOTIFICATION_ID = 1001

    fun ensureChannels(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = context.getSystemService(NotificationManager::class.java) ?: return

        val box = NotificationChannel(
            CHANNEL_BOX, "Box connection", NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Keeps your Vulos box reachable in the background."
            setShowBadge(false)
        }
        val alerts = NotificationChannel(
            CHANNEL_ALERTS, "Alerts", NotificationManager.IMPORTANCE_DEFAULT,
        ).apply {
            description = "Missed messages, calls and box events."
        }
        nm.createNotificationChannel(box)
        nm.createNotificationChannel(alerts)
    }
}
