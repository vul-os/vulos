package org.vulos.mobile

/**
 * Thin hand-off from the SmsReceiver (a manifest BroadcastReceiver, separate from
 * the Activity) to whatever is currently listening — normally MainActivity while
 * resumed. Kept deliberately tiny: no state, just a volatile listener slot.
 *
 * MainActivity sets [onSms] in onResume and clears it in onPause, so an inbound
 * SMS only reaches the WebView while the app is actually in the foreground with a
 * trusted page loaded (the bridge does the origin gating).
 */
object TelephonyEvents {
    @Volatile
    var onSms: ((from: String, body: String, timestampMillis: Long) -> Unit)? = null
}
