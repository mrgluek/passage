package info.gluek.passage

import org.json.JSONArray
import org.json.JSONObject
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.security.SecureRandom
import java.text.SimpleDateFormat
import java.util.Locale
import java.util.TimeZone

object WhatsApp {
    private val random = SecureRandom()
    const val MIN_PORT = 20000
    const val MAX_PORT = 32767
    private const val MAX_TRIES = 500

    fun choosePort(excluded: Set<Int>, start: Int = random.nextInt(MAX_PORT - MIN_PORT + 1), isBindable: (Int) -> Boolean = ::isBindable): Int {
        val count = MAX_PORT - MIN_PORT + 1
        // Bounded: this can run on the UI thread, and a free port turns up within a few tries.
        for (offset in 0 until minOf(count, MAX_TRIES)) {
            val port = MIN_PORT + Math.floorMod(start + offset, count)
            if (port !in excluded && isBindable(port)) return port
        }
        error("No local port available")
    }

    internal fun isBindable(port: Int): Boolean = try {
        ServerSocket().use { it.bind(InetSocketAddress(InetAddress.getByName(Settings.HOST), port)) }
        true
    } catch (_: Exception) { false }

    fun configure(config: JSONObject, enabled: Boolean, chatPort: Int, mediaPort: Int): JSONObject = config.apply {
        remove("forwards")
        if (enabled) put("forwards", JSONArray().apply {
            put(JSONObject().put("name", "whatsapp-chat").put("listen", "${Settings.HOST}:$chatPort").put("target", "g.whatsapp.net:5222"))
            put(JSONObject().put("name", "whatsapp-media").put("listen", "${Settings.HOST}:$mediaPort").put("target", "whatsapp.net:443"))
        })
    }

    data class Labels(val chat: String, val media: String, val waiting: String, val last: String, val down: String)
    data class Status(val lines: List<String>, val warnings: List<String>, val connected: Boolean)

    fun status(forwards: JSONArray?, labels: Labels, zone: TimeZone = TimeZone.getDefault()): Status {
        val rows = (0 until (forwards?.length() ?: 0)).mapNotNull { forwards?.optJSONObject(it) }
        val warnings = mutableListOf<String>()
        var connected = false
        val lines = listOf("whatsapp-chat" to labels.chat, "whatsapp-media" to labels.media).map { (name, label) ->
            val row = rows.firstOrNull { it.optString("name") == name }
            val stamp = try {
                SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.ROOT)
                    .parse(row?.optString("last_conn").orEmpty().replace(Regex("\\.\\d+"), ""))
                    ?.takeIf { it.time > 0 }
            } catch (_: Exception) { null }
            if (stamp != null) connected = true
            if (row != null && !row.optBoolean("up")) {
                val error = row.optString("error").lineSequence().firstOrNull().orEmpty().take(120)
                warnings += "$label: ${labels.down}" + if (error.isEmpty()) "" else " ($error)"
            }
            "$label: " + if (stamp == null) labels.waiting else String.format(labels.last,
                SimpleDateFormat("HH:mm", Locale.ROOT).apply { timeZone = zone }.format(stamp))
        }
        return Status(lines, warnings, connected)
    }

    fun showHint(enabled: Boolean, running: Boolean, connected: Boolean, elapsed: Long): Boolean =
        enabled && running && !connected && elapsed >= 30_000
}
