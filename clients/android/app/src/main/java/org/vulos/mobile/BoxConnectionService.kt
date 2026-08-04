package org.vulos.mobile

import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat

/**
 * Foreground service (NOTIFY-01) that keeps the box connection alive so
 * notifications (missed SMS/call, box events) arrive reliably even under Doze.
 * Started/stopped ONLY by the user via the notify bridge — never auto-started, so
 * there is no silent always-on service.
 *
 * It runs as foregroundServiceType=dataSync with a persistent low-importance
 * notification. Android 14+ caps dataSync foreground time (~6h/day) and requires
 * the FOREGROUND_SERVICE_DATA_SYNC permission + the type on startForeground — see
 * the manifest. The actual keep-alive transport is the shell's own connection; this
 * service exists to hold the process foregrounded and own the notification.
 */
class BoxConnectionService : Service() {

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        running = true
        val open = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val notification = NotificationCompat.Builder(this, VulosNotifications.CHANNEL_BOX)
            .setContentTitle("Vulos")
            .setContentText("Box connection active")
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setOngoing(true)
            .setContentIntent(open)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                VulosNotifications.FGS_NOTIFICATION_ID, notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
            )
        } else {
            startForeground(VulosNotifications.FGS_NOTIFICATION_ID, notification)
        }
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        super.onDestroy()
    }

    companion object {
        @Volatile
        var running: Boolean = false
            private set

        fun start(context: Context) {
            val intent = Intent(context, BoxConnectionService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, BoxConnectionService::class.java))
        }
    }
}
