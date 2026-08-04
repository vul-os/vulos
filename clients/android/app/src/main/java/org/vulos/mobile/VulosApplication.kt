package org.vulos.mobile

import android.app.Application
import android.webkit.WebView

/**
 * Pre-warms the WebView provider so the launcher's first paint is not gated on
 * cold WebView init (LAUNCH-01 / the MOB-05 safeguard for the home surface).
 *
 * Instantiating a throwaway WebView loads the (expensive) WebView provider and
 * warms its classes; we destroy it immediately since it is never displayed. Any
 * failure here is non-fatal — the app just starts slightly colder.
 */
class VulosApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        // Notification channels (box connection + alerts) must exist before any
        // notification is posted; creating them here is idempotent and cheap.
        VulosNotifications.ensureChannels(this)
        try {
            WebView(this).destroy()
        } catch (_: Throwable) {
            // WebView unavailable (e.g. updating) — ignore; MainActivity will retry.
        }
    }
}
