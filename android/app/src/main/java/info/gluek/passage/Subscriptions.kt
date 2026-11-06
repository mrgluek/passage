package info.gluek.passage

import android.content.Context
import org.json.JSONObject

/** Subscription display uses only public metadata. */
object Subscriptions {
    fun rows(config: JSONObject): List<JSONObject> {
        val rows = config.optJSONArray("subscriptions") ?: return emptyList()
        return (0 until rows.length()).map { rows.getJSONObject(it) }
    }

    fun owner(config: JSONObject, id: String): String = rows(config).firstOrNull { sub ->
        val ids = sub.optJSONArray("route_ids")
        ids != null && (0 until ids.length()).any { ids.optString(it) == id }
    }?.optString("name").orEmpty()

    fun routeIds(config: JSONObject): Set<String> {
        val routes = config.optJSONArray("routes") ?: return emptySet()
        return (0 until routes.length()).map { routes.getJSONObject(it).getString("id") }.toSet()
    }

    fun delta(before: JSONObject, after: JSONObject): Pair<Int, Int> {
        val old = routeIds(before)
        val new = routeIds(after)
        return (new - old).size to (old - new).size
    }

    fun errorResource(code: String): Int? = when (code) {
        "", "unchanged" -> null
        "http" -> R.string.sub_error_http
        "size" -> R.string.sub_error_size
        "decrypt" -> R.string.sub_error_decrypt
        "signature" -> R.string.sub_error_signature
        "rollback" -> R.string.sub_error_rollback
        "collision" -> R.string.sub_error_collision
        "required" -> R.string.sub_error_required
        else -> R.string.sub_error_network
    }

    fun isClosed(sub: JSONObject): Boolean = sub.optBoolean("closed")

    fun detail(context: Context, sub: JSONObject, now: Long = System.currentTimeMillis() / 1000): String {
        if (isClosed(sub)) return context.getString(R.string.sub_closed)
        val count = if (sub.has("routes")) sub.optInt("routes") else sub.optJSONArray("route_ids")?.length() ?: 0
        val heading = context.getString(R.string.sub_detail, sub.optLong("revision"), count)
        val error = errorResource(sub.optString("last_error"))
        if (error != null) return heading + "\n" + context.getString(error)
        val last = sub.optLong("last_ok")
        if (last <= 0) return heading
        val minutes = ((now - last).coerceAtLeast(0) / 60)
        val time = when {
            minutes < 1 -> context.getString(R.string.sub_just_now)
            minutes < 60 -> context.getString(R.string.sub_minutes, minutes)
            minutes < 1440 -> context.getString(R.string.sub_hours, minutes / 60)
            else -> context.getString(R.string.sub_days, minutes / 1440)
        }
        return "$heading\n$time"
    }

    fun request(config: String): JSONObject = JSONObject().put("existing", if (config.isEmpty()) JSONObject() else JSONObject(config))
}
