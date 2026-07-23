package org.vulos.mobile

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.provider.Telephony

/**
 * Inbound SMS. Registered in the manifest for SMS_RECEIVED and gated by the
 * signature-level BROADCAST_SMS permission, so only the system can deliver here.
 *
 * This is a NON-default-handler receiver: it observes and reads inbound SMS
 * (RECEIVE_SMS) without taking the full default-SMS-app (SMS_DELIVER) contract.
 * It concatenates multipart parts and forwards a single message to whatever is
 * listening (TelephonyEvents) — it never renders UI or persists anything itself.
 */
class SmsReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Telephony.Sms.Intents.SMS_RECEIVED_ACTION) return
        val listener = TelephonyEvents.onSms ?: return // no one foregrounded to receive it

        val parts = Telephony.Sms.Intents.getMessagesFromIntent(intent) ?: return
        if (parts.isEmpty()) return

        val from = parts[0].originatingAddress ?: ""
        val body = buildString { parts.forEach { append(it.messageBody ?: "") } }
        val ts = parts[0].timestampMillis

        listener(from, body, ts)
    }
}
