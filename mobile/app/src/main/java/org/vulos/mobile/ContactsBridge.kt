package org.vulos.mobile

import android.Manifest
import android.net.Uri
import android.provider.ContactsContract
import android.webkit.WebView
import androidx.webkit.JavaScriptReplyProxy
import org.json.JSONArray
import org.json.JSONObject

/**
 * Contacts bridge (CONTACTS-01). READ-ONLY: it reads device + SIM contacts and
 * returns them to the shell so a backend agent can ingest them into unified
 * contacts. It never writes, deletes or modifies a contact.
 *
 * JS object: `vulosContacts`
 *   { id, action:"perms" }                 report READ_CONTACTS grant
 *   { id, action:"list", limit? }          device contacts (READ_CONTACTS)
 *   { id, action:"sim", limit? }           SIM (ICC/ADN) contacts (READ_CONTACTS)
 * reply: { id, ok:true, contacts:[ {name, phones:[], emails:[], org?} ] }
 *
 * Contextual: READ_CONTACTS is requested on first `list`/`sim`; on denial the
 * shell keeps working (manual entry / server-side contacts still function).
 */
class ContactsBridge(activity: MainActivity) : BridgeBase(activity) {

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
                extra = JSONObject().put("readContacts", hasPerm(Manifest.permission.READ_CONTACTS)),
            )
            "list" -> withPerm(Manifest.permission.READ_CONTACTS, rp, id) {
                io.execute {
                    val contacts = readDeviceContacts(msg.optInt("limit", 2000))
                    reply(rp, id, ok = true, extra = JSONObject().put("contacts", contacts))
                }
            }
            "sim" -> withPerm(Manifest.permission.READ_CONTACTS, rp, id) {
                io.execute {
                    val contacts = readSimContacts(msg.optInt("limit", 500))
                    reply(rp, id, ok = true, extra = JSONObject().put("contacts", contacts))
                }
            }
            else -> reply(rp, id, error = "unknown-action")
        }
    }

    /**
     * Aggregate device contacts into the shell's {name, phones[], emails[], org?}
     * shape. One pass per data kind keyed by CONTACT_ID keeps the queries simple and
     * avoids a giant JOIN; the map is assembled in insertion order.
     */
    private fun readDeviceContacts(limit: Int): JSONArray {
        val cap = limit.coerceIn(1, 20000)
        val byId = LinkedHashMap<String, Contact>()

        // Names — seed the map so the ordering is by display name.
        activity.contentResolver.query(
            ContactsContract.Contacts.CONTENT_URI,
            arrayOf(ContactsContract.Contacts._ID, ContactsContract.Contacts.DISPLAY_NAME_PRIMARY),
            null, null,
            ContactsContract.Contacts.DISPLAY_NAME_PRIMARY + " ASC LIMIT " + cap,
        )?.use { c ->
            val idi = c.getColumnIndex(ContactsContract.Contacts._ID)
            val ni = c.getColumnIndex(ContactsContract.Contacts.DISPLAY_NAME_PRIMARY)
            while (c.moveToNext()) {
                val cid = if (idi >= 0) c.getString(idi) else continue
                byId.getOrPut(cid) { Contact() }.name =
                    if (ni >= 0) c.getString(ni) ?: "" else ""
            }
        }
        if (byId.isEmpty()) return JSONArray()

        // Phones.
        activity.contentResolver.query(
            ContactsContract.CommonDataKinds.Phone.CONTENT_URI,
            arrayOf(
                ContactsContract.CommonDataKinds.Phone.CONTACT_ID,
                ContactsContract.CommonDataKinds.Phone.NUMBER,
            ),
            null, null, null,
        )?.use { c ->
            val idi = c.getColumnIndex(ContactsContract.CommonDataKinds.Phone.CONTACT_ID)
            val vi = c.getColumnIndex(ContactsContract.CommonDataKinds.Phone.NUMBER)
            while (c.moveToNext()) {
                val cid = if (idi >= 0) c.getString(idi) else continue
                val v = if (vi >= 0) c.getString(vi) else null
                if (!v.isNullOrBlank()) byId[cid]?.phones?.add(v.trim())
            }
        }

        // Emails.
        activity.contentResolver.query(
            ContactsContract.CommonDataKinds.Email.CONTENT_URI,
            arrayOf(
                ContactsContract.CommonDataKinds.Email.CONTACT_ID,
                ContactsContract.CommonDataKinds.Email.ADDRESS,
            ),
            null, null, null,
        )?.use { c ->
            val idi = c.getColumnIndex(ContactsContract.CommonDataKinds.Email.CONTACT_ID)
            val vi = c.getColumnIndex(ContactsContract.CommonDataKinds.Email.ADDRESS)
            while (c.moveToNext()) {
                val cid = if (idi >= 0) c.getString(idi) else continue
                val v = if (vi >= 0) c.getString(vi) else null
                if (!v.isNullOrBlank()) byId[cid]?.emails?.add(v.trim())
            }
        }

        // Organisation (optional).
        activity.contentResolver.query(
            ContactsContract.Data.CONTENT_URI,
            arrayOf(
                ContactsContract.Data.CONTACT_ID,
                ContactsContract.CommonDataKinds.Organization.COMPANY,
            ),
            ContactsContract.Data.MIMETYPE + " = ?",
            arrayOf(ContactsContract.CommonDataKinds.Organization.CONTENT_ITEM_TYPE),
            null,
        )?.use { c ->
            val idi = c.getColumnIndex(ContactsContract.Data.CONTACT_ID)
            val vi = c.getColumnIndex(ContactsContract.CommonDataKinds.Organization.COMPANY)
            while (c.moveToNext()) {
                val cid = if (idi >= 0) c.getString(idi) else continue
                val v = if (vi >= 0) c.getString(vi) else null
                if (!v.isNullOrBlank() && byId[cid]?.org == null) byId[cid]?.org = v.trim()
            }
        }

        return toJson(byId.values)
    }

    /**
     * SIM (ICC/ADN) contacts. The `content://icc/adn` provider is undocumented and
     * absent on some OEM/eSIM devices — best-effort, returns empty rather than
     * throwing when unavailable.
     */
    private fun readSimContacts(limit: Int): JSONArray {
        val out = LinkedHashMap<String, Contact>()
        val cap = limit.coerceIn(1, 5000)
        try {
            activity.contentResolver.query(
                Uri.parse("content://icc/adn"),
                arrayOf("name", "number"), null, null, null,
            )?.use { c ->
                val ni = c.getColumnIndex("name")
                val vi = c.getColumnIndex("number")
                var n = 0
                while (c.moveToNext() && n < cap) {
                    val name = if (ni >= 0) c.getString(ni) ?: "" else ""
                    val number = if (vi >= 0) c.getString(vi) else null
                    val key = "$name|$number"
                    val contact = out.getOrPut(key) { Contact().also { it.name = name } }
                    if (!number.isNullOrBlank()) contact.phones.add(number.trim())
                    n++
                }
            }
        } catch (_: Exception) {
            // Provider missing / restricted on this device — SIM read is best-effort.
        }
        return toJson(out.values)
    }

    private class Contact {
        var name: String = ""
        val phones = LinkedHashSet<String>()
        val emails = LinkedHashSet<String>()
        var org: String? = null
    }

    private fun toJson(contacts: Collection<Contact>): JSONArray {
        val arr = JSONArray()
        for (c in contacts) {
            if (c.name.isBlank() && c.phones.isEmpty() && c.emails.isEmpty()) continue
            val o = JSONObject()
                .put("name", c.name)
                .put("phones", JSONArray(c.phones.toList()))
                .put("emails", JSONArray(c.emails.toList()))
            c.org?.let { o.put("org", it) }
            arr.put(o)
        }
        return arr
    }
}
