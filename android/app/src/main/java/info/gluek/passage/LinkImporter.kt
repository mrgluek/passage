package info.gluek.passage

import android.app.Activity
import android.app.AlertDialog
import android.widget.Toast
import java.util.concurrent.Executor

object LinkImporter {
    fun importLink(activity: Activity, settings: Settings, worker: Executor, text: String, onImported: (Boolean) -> Unit) = with(activity) {
        val incoming = text.trim()
        val previous = settings.routesConfig
        val previousLink = settings.link
        worker.execute {
            try {
                val existing = if (previous.isNotEmpty()) org.json.JSONObject(previous)
                    else if (previousLink.isNotEmpty()) Native.parse(previousLink) else org.json.JSONObject()
                fun prepare(replace: Boolean, subscribe: Boolean = false): org.json.JSONObject {
                    val request = org.json.JSONObject().put("existing", existing).put("link", incoming).put("replace", replace).put("subscribe", subscribe)
                    return Native.json(Native.importLink(request.toString().toByteArray(Charsets.UTF_8)))
                }
                val preview = prepare(false)
                if (preview.has("error")) throw IllegalArgumentException(preview.optString("error"))
                runOnUiThread {
                    if (isFinishing || isDestroyed) return@runOnUiThread
                    val subscribing = preview.optString("mode") == "subscribe"
                    val trust = if (preview.optString("signature") == "trusted_author") getString(R.string.import_trusted)
                        else getString(R.string.import_untrusted)
                    val collision = if (preview.optBoolean("collision")) "\n" + getString(R.string.import_collision) else ""
                    val operation = if (incoming.isEmpty()) getString(R.string.import_clear)
                        else if (preview.optString("mode") == "replace") getString(R.string.import_replace)
                        else getString(R.string.import_merge)
                    AlertDialog.Builder(this).setTitle(if (subscribing) R.string.subscribe else R.string.import_title)
                        .setMessage(if (subscribing) subscriptionPreview(preview) else trust + "\n\n" + operation + collision + "\n" + (preview.optJSONArray("route_ids")?.join(", ") ?: "") + "\n\n" + getString(R.string.import_personal))
                        .setNegativeButton(android.R.string.cancel, null)
                        .setPositiveButton(if (subscribing) R.string.subscribe else R.string.save) { _, _ ->
                            worker.execute {
                                try {
                                    if (settings.routesConfig != previous || settings.link != previousLink) throw IllegalStateException("Settings changed; retry import")
                                    val result = prepare(if (subscribing) preview.optBoolean("collision") else true, subscribing)
                                    if (result.has("error")) throw IllegalArgumentException(result.optString("error"))
                                    synchronized(Settings.configLock) {
                                        if (settings.routesConfig != previous || settings.link != previousLink) throw IllegalStateException("Settings changed; retry import")
                                        settings.saveRoutes(if (subscribing) previousLink else incoming, result.getJSONObject("config").toString())
                                    }
                                    runOnUiThread {
                                        onImported(subscribing)
                                    }
                                } catch (e: Throwable) { runOnUiThread { Toast.makeText(this, getString(R.string.link_error_toast, e.message), Toast.LENGTH_LONG).show() } }
                            }
                        }.showStyled(primary = true)
                }
            } catch (e: Throwable) { runOnUiThread { Toast.makeText(this, getString(R.string.link_error_toast, e.message), Toast.LENGTH_LONG).show() } }
        }
    }

    private fun Activity.subscriptionPreview(preview: org.json.JSONObject): String {
        val sub = preview.getJSONObject("subscription")
        return listOf(
            *Subscriptions.inviteSignerResources(preview.optString("signature")).map { getString(it) }.toTypedArray(),
            "",
            Subscriptions.displayName(sub.optString("name")),
            resources.getQuantityString(R.plurals.sub_addresses, sub.optInt("sources"), sub.optInt("sources")),
            getString(if (sub.optBoolean("has_seed")) R.string.sub_seed else R.string.sub_no_seed),
        ).joinToString("\n") + if (preview.optBoolean("collision")) "\n\n" + getString(R.string.sub_collision) else ""
    }

}
