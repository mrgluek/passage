package info.gluek.passage

import android.content.Context
import org.json.JSONObject

/** Subscription display uses only public metadata. */
object Subscriptions {
    fun displayName(name: String): String = buildString {
        var offset = 0
        var count = 0
        while (offset < name.length && count < 64) {
            val codePoint = name.codePointAt(offset)
            offset += Character.charCount(codePoint)
            when (Character.getType(codePoint)) {
                Character.CONTROL.toInt(), Character.FORMAT.toInt(),
                Character.LINE_SEPARATOR.toInt(), Character.PARAGRAPH_SEPARATOR.toInt() -> continue
            }
            appendCodePoint(codePoint)
            count++
        }
    }.trim()

    fun inviteSignerResources(signature: String): List<Int> =
        if (signature == "trusted_author") listOf(R.string.sub_trusted, R.string.sub_takeover)
        else listOf(R.string.sub_unknown)

    fun rows(config: JSONObject): List<JSONObject> {
        val rows = config.optJSONArray("subscriptions") ?: return emptyList()
        return (0 until rows.length()).map { rows.getJSONObject(it) }
    }

    fun owner(config: JSONObject, id: String): String = rows(config).firstOrNull { sub ->
        val ids = sub.optJSONArray("route_ids")
        ids != null && (0 until ids.length()).any { ids.optString(it) == id }
    }?.optString("name")?.let(::displayName).orEmpty()

    fun routeIds(config: JSONObject): Set<String> {
        val routes = config.optJSONArray("routes") ?: return emptySet()
        return (0 until routes.length()).map { routes.getJSONObject(it).getString("id") }.toSet()
    }

    fun delta(before: JSONObject, after: JSONObject): Pair<Int, Int> {
        val old = routeIds(before)
        val new = routeIds(after)
        return (new - old).size to (old - new).size
    }

    data class Notice(val added: Int, val removed: Int, val time: Long? = null) {
        fun encode(): String = "$added,$removed" + (time?.let { ",$it" } ?: "")
        fun clock(): String? = time?.let {
            java.text.SimpleDateFormat("HH:mm", java.util.Locale.ROOT).format(java.util.Date(it))
        }
    }

    fun decodeNotice(value: String): Notice? {
        val parts = value.split(',')
        if (parts.size !in 2..3) return null
        val added = parts[0].toIntOrNull()?.takeIf { it >= 0 } ?: return null
        val removed = parts[1].toIntOrNull()?.takeIf { it >= 0 } ?: return null
        val time = if (parts.size == 3) parts[2].toLongOrNull()?.takeIf { it > 0 } ?: return null else null
        return Notice(added, removed, time)
    }

    fun refreshNotice(before: JSONObject, after: JSONObject, now: Long): Notice? {
        val (added, removed) = delta(before, after)
        return if (added == 0 && removed == 0) null else Notice(added, removed, now)
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
        val heading = context.resources.getQuantityString(R.plurals.sub_detail, count, sub.optLong("revision"), count)
        val error = errorResource(sub.optString("last_error"))
        if (error != null) return heading + "\n" + context.getString(error)
        val last = sub.optLong("last_ok")
        if (last <= 0) return heading
        val minutes = ((now - last).coerceAtLeast(0) / 60)
        val time = when {
            minutes < 1 -> context.getString(R.string.sub_just_now)
            minutes < 60 -> context.getString(R.string.sub_minutes, minutes)
            minutes < 1440 -> context.getString(R.string.sub_hours, minutes / 60)
            else -> context.resources.getQuantityString(R.plurals.sub_days, (minutes / 1440).coerceAtMost(Int.MAX_VALUE.toLong()).toInt(), minutes / 1440)
        }
        return "$heading\n$time"
    }

    fun request(config: String): JSONObject = JSONObject().put("existing", if (config.isEmpty()) JSONObject() else JSONObject(config))
}
