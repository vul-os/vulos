package org.vulos.mobile

import android.app.Activity
import android.app.role.RoleManager
import android.content.Intent
import android.os.Build
import android.provider.Settings
import android.webkit.WebView
import androidx.webkit.JavaScriptReplyProxy
import org.json.JSONObject

/**
 * Launcher-role bridge (LAUNCH-01). Vulos declares the HOME intent-filter (so it is
 * SELECTABLE as a home app), but it is NEVER the default by fabrication — the user
 * opts in through this bridge, which only ever opens the SYSTEM chooser / home
 * settings. It is fully reversible: `openHomeSettings` takes the user straight to
 * where they can pick a different launcher back.
 *
 * JS object: `vulosLauncher`
 *   { id, action:"status" }            { isDefault, canRequest }
 *   { id, action:"setDefault" }        open the system "set default home" flow
 *   { id, action:"openHomeSettings" }  open Home settings (to change / revert)
 */
class LauncherBridge(activity: MainActivity) : BridgeBase(activity) {

    override fun handle(
        action: String,
        msg: JSONObject,
        rp: JavaScriptReplyProxy,
        id: String,
        view: WebView,
    ) {
        when (action) {
            "status" -> reply(
                rp, id, ok = true,
                extra = JSONObject().put("isDefault", isDefaultHome()).put("canRequest", true),
            )
            "setDefault" -> requestDefault(rp, id)
            "openHomeSettings" -> openHomeSettings(rp, id)
            else -> reply(rp, id, error = "unknown-action")
        }
    }

    private fun isDefaultHome(): Boolean {
        // Prefer RoleManager on API 29+; fall back to resolving the current HOME
        // component and comparing the package.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val rm = activity.getSystemService(RoleManager::class.java)
            if (rm != null && rm.isRoleAvailable(RoleManager.ROLE_HOME)) {
                return rm.isRoleHeld(RoleManager.ROLE_HOME)
            }
        }
        val resolve = activity.packageManager.resolveActivity(
            Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME),
            android.content.pm.PackageManager.MATCH_DEFAULT_ONLY,
        )
        return resolve?.activityInfo?.packageName == activity.packageName
    }

    private fun requestDefault(rp: JavaScriptReplyProxy, id: String) {
        if (isDefaultHome()) { reply(rp, id, ok = true, extra = JSONObject().put("isDefault", true)); return }

        // API 29+: the proper role-request dialog. It only ever ASKS — the user
        // confirms, and can decline.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val rm = activity.getSystemService(RoleManager::class.java)
            if (rm != null && rm.isRoleAvailable(RoleManager.ROLE_HOME)) {
                val intent = rm.createRequestRoleIntent(RoleManager.ROLE_HOME)
                activity.launchForResult(intent) { code, _ ->
                    reply(
                        rp, id, ok = true,
                        extra = JSONObject().put("granted", code == Activity.RESULT_OK).put("isDefault", isDefaultHome()),
                    )
                }
                return
            }
        }
        // Older devices: send the user to Home settings to choose Vulos.
        openHomeSettings(rp, id)
    }

    private fun openHomeSettings(rp: JavaScriptReplyProxy, id: String) {
        try {
            activity.startActivity(
                Intent(Settings.ACTION_HOME_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
            reply(rp, id, ok = true)
        } catch (_: Exception) {
            // Some OEMs lack the dedicated screen — fall back to app details.
            try {
                activity.startActivity(
                    Intent(
                        Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                        android.net.Uri.parse("package:" + activity.packageName),
                    ).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                )
                reply(rp, id, ok = true)
            } catch (e: Exception) {
                reply(rp, id, error = e.message ?: "no-settings-screen")
            }
        }
    }
}
