package org.vulos.mobile

import android.webkit.WebView
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.webkit.JavaScriptReplyProxy
import org.json.JSONObject

/**
 * Biometric bridge (BIO-01). Fingerprint / face (or device credential fallback) to
 * unlock the app or a box session. No manifest permission is required for
 * androidx.biometric; the user is prompted by the system biometric sheet, and
 * everything degrades gracefully — if no biometric is enrolled the shell can fall
 * back to its passphrase/PIN unlock.
 *
 * JS object: `vulosBiometric`
 *   { id, action:"available" }                 { status, canAuthenticate }
 *   { id, action:"authenticate", title?, subtitle?, allowDeviceCredential? }
 *                                              { ok } or { error }
 *
 * This bridge only reports success/failure of a local user-presence check; it does
 * not itself hold or release any key. Binding a box-session key to the result is
 * the shell's job (keeps the crypto in one place).
 */
class BiometricBridge(activity: MainActivity) : BridgeBase(activity) {

    override fun handle(
        action: String,
        msg: JSONObject,
        rp: JavaScriptReplyProxy,
        id: String,
        view: WebView,
    ) {
        when (action) {
            "available" -> reportAvailability(rp, id)
            "authenticate" -> authenticate(
                msg.optString("title", "Unlock Vulos"),
                msg.optString("subtitle"),
                msg.optBoolean("allowDeviceCredential", false),
                rp, id,
            )
            else -> reply(rp, id, error = "unknown-action")
        }
    }

    private fun authenticators(allowDeviceCredential: Boolean): Int =
        if (allowDeviceCredential)
            BiometricManager.Authenticators.BIOMETRIC_STRONG or
                BiometricManager.Authenticators.DEVICE_CREDENTIAL
        else
            BiometricManager.Authenticators.BIOMETRIC_STRONG or
                BiometricManager.Authenticators.BIOMETRIC_WEAK

    private fun reportAvailability(rp: JavaScriptReplyProxy, id: String) {
        val result = BiometricManager.from(activity)
            .canAuthenticate(authenticators(false))
        val status = when (result) {
            BiometricManager.BIOMETRIC_SUCCESS -> "available"
            BiometricManager.BIOMETRIC_ERROR_NO_HARDWARE -> "no-hardware"
            BiometricManager.BIOMETRIC_ERROR_HW_UNAVAILABLE -> "hw-unavailable"
            BiometricManager.BIOMETRIC_ERROR_NONE_ENROLLED -> "none-enrolled"
            else -> "unavailable"
        }
        reply(
            rp, id, ok = true,
            extra = JSONObject()
                .put("status", status)
                .put("canAuthenticate", result == BiometricManager.BIOMETRIC_SUCCESS),
        )
    }

    private fun authenticate(
        title: String,
        subtitle: String,
        allowDeviceCredential: Boolean,
        rp: JavaScriptReplyProxy,
        id: String,
    ) {
        activity.runOnUiThread {
            try {
                val executor = ContextCompat.getMainExecutor(activity)
                val prompt = BiometricPrompt(
                    activity, executor,
                    object : BiometricPrompt.AuthenticationCallback() {
                        override fun onAuthenticationSucceeded(r: BiometricPrompt.AuthenticationResult) {
                            reply(rp, id, ok = true, extra = JSONObject().put("authenticated", true))
                        }

                        override fun onAuthenticationError(code: Int, msg: CharSequence) {
                            // User cancel / lockout / no-biometric — a normal outcome,
                            // not a crash. The shell falls back to passphrase unlock.
                            reply(
                                rp, id, error = "auth-error",
                                extra = JSONObject().put("code", code).put("message", msg.toString()),
                            )
                        }

                        override fun onAuthenticationFailed() {
                            // A single non-matching attempt; the sheet stays open, so we
                            // do NOT reply here (only terminal outcomes reply).
                        }
                    },
                )
                val builder = BiometricPrompt.PromptInfo.Builder()
                    .setTitle(title)
                    .setAllowedAuthenticators(authenticators(allowDeviceCredential))
                if (subtitle.isNotBlank()) builder.setSubtitle(subtitle)
                // A negative button is required UNLESS DEVICE_CREDENTIAL is allowed
                // (the two are mutually exclusive in the PromptInfo contract).
                if (!allowDeviceCredential) builder.setNegativeButtonText("Cancel")
                prompt.authenticate(builder.build())
            } catch (e: Exception) {
                reply(rp, id, error = e.message ?: "prompt-failed")
            }
        }
    }
}
