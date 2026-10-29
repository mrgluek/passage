package info.gluek.passage

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** Turns the router's status JSON (tunnel.Status) into text for people. */
object StatusText {
    fun oneLine(c: Context, s: JSONObject): String {
        if (!s.optBoolean("running")) return c.getString(R.string.not_running)
        val session = c.getString(if (s.optBoolean("session")) R.string.session_up else R.string.session_down)
        return c.getString(R.string.one_line, routeName(c, s.optString("route")), stateName(c, s.optString("state")), session)
    }

    fun full(c: Context, s: JSONObject): String {
        if (!s.optBoolean("running")) return c.getString(R.string.proxy_not_running)
        val sb = StringBuilder()
        sb.append(c.getString(R.string.status_route, routeName(c, s.optString("route")).uppercase(),
            clock(s.optString("route_since")), s.optString("mode"))).append('\n')
        sb.append(c.getString(R.string.status_network, stateName(c, s.optString("state")))).append('\n')
        val probe = s.optString("last_probe")
        if (!probe.startsWith("0001-")) {
            val r = s.optJSONObject("last_result") ?: JSONObject()
            // Canaries are only checked when no probe host answered.
            val canary = when {
                !r.optBoolean("canary_checked") -> ""
                r.optBoolean("canary_ok") -> c.getString(R.string.status_canary_ok)
                else -> c.getString(R.string.status_canary_silent)
            }
            sb.append(c.getString(R.string.status_last_check, clock(probe), r.optInt("relay_ok"), r.optInt("relays"), canary))
                .append('\n')
        }
        sb.append(c.getString(R.string.status_probe_hosts,
            list(s.optJSONArray("relays")).ifEmpty { c.getString(R.string.none_yet) })).append('\n')
        val session = c.getString(if (s.optBoolean("session")) R.string.session_up else R.string.session_down)
        sb.append(c.getString(R.string.status_session, session, s.optInt("resolvers"),
            s.optString("operator").ifEmpty { "?" })).append('\n')
        sb.append(c.getString(R.string.status_network_resolvers,
            list(s.optJSONArray("network_resolvers")).ifEmpty { c.getString(R.string.none) })).append('\n')
        if (s.optBoolean("dns_intercepted")) {
            val via = s.optString("dns_egress")
            sb.append(if (via.isNotEmpty()) c.getString(R.string.status_intercepted_via, via)
                else c.getString(R.string.status_intercepted)).append('\n')
        }
        val active = s.optJSONObject("active") ?: JSONObject()
        sb.append(c.getString(R.string.status_open_conns, active.optInt("direct"), active.optInt("tunnel"))).append('\n')
        val stats = s.optJSONObject("stats") ?: JSONObject()
        for ((route, label) in listOf("direct" to R.string.status_direct, "tunnel" to R.string.status_tunnel)) {
            val r = stats.optJSONObject(route) ?: continue
            sb.append(c.getString(R.string.status_route_stats, c.getString(label), r.optLong("conns"), r.optLong("fails"),
                bytes(r.optLong("up")), bytes(r.optLong("down")))).append('\n')
        }

        val paths = s.optJSONArray("paths") ?: JSONArray()
        if (paths.length() > 0) {
            // Narrow enough for a phone in portrait: the port is implied by the
            // transport (dot = 853, else 53), the resolver column is as wide as
            // the longest address, loss is a percentage. "len" is the path's
            // current query-name cap.
            sb.append('\n').append(c.getString(R.string.status_paths)).append('\n')
            val rows = (0 until paths.length()).map { paths.getJSONObject(it) }.sortedWith(
                compareByDescending<JSONObject> { working(it) }.thenByDescending { it.optInt("successes") },
            )
            val names = rows.map { shortResolver(it.optString("resolver")) }
            val w = (names.maxOfOrNull { it.length } ?: 0).coerceAtLeast(8)
            val fmt = "%-3s %-4s %-${w}s %4s %4s %5s %3s\n"
            sb.append(String.format(Locale.ROOT, fmt, "via", "st", "resolver", "rtt", "loss", "ok", "len"))
            rows.forEachIndexed { i, p ->
                val mark = when {
                    p.optBoolean("suspended") -> "susp"
                    p.optBoolean("verified") -> "ok"
                    else -> "?"
                }
                val rtt = p.optLong("rtt") / 1_000_000
                sb.append(String.format(Locale.ROOT, fmt,
                    p.optString("transport"), mark, names[i],
                    if (rtt > 0) rtt.toString() else "-",
                    "${(p.optDouble("loss", 0.0) * 100).toInt()}%",
                    p.optInt("successes").toString(),
                    p.optInt("name_len").takeIf { it > 0 }?.toString() ?: "-"))
            }
        }

        val events = s.optJSONArray("events") ?: JSONArray()
        if (events.length() > 0) {
            sb.append('\n').append(c.getString(R.string.status_events)).append('\n')
            for (i in 0 until events.length()) {
                val e = events.getJSONObject(i)
                sb.append("  ${clock(e.optString("t"))} ${e.optString("msg")}\n")
            }
        }
        return sb.toString()
    }

    /**
     * The status for a bug report, without the tunnel zone (private, like
     * everything the link carries).
     */
    fun report(s: JSONObject, net: NetInfo.Snapshot, settings: Settings): String {
        val r = JSONObject(s.toString())
        r.remove("zone")
        r.put("app", JSONObject()
            .put("listen", settings.listen)
            .put("auth", settings.auth)
            .put("start_on_boot", settings.startOnBoot)
            .put("mode_setting", settings.mode)
            .put("probe_hosts", JSONArray(settings.probeTargets()))
            .put("last_error", PassageService.lastError ?: JSONObject.NULL))
        r.put("device_network", JSONObject()
            .put("transport", net.transport)
            .put("dns", JSONArray(net.dns))
            .put("operator", net.operator)
            .put("private_dns", net.privateDns))
        return r.toString(2)
    }

    private fun stateName(c: Context, state: String): String = when (state) {
        "direct" -> c.getString(R.string.state_direct)
        "blocked" -> c.getString(R.string.state_blocked)
        "relays-down" -> c.getString(R.string.state_relays_down)
        "offline" -> c.getString(R.string.state_offline)
        "unknown" -> c.getString(R.string.state_unknown)
        else -> state
    }

    private fun routeName(c: Context, route: String): String = when (route) {
        "direct" -> c.getString(R.string.route_direct)
        "tunnel" -> c.getString(R.string.route_tunnel)
        else -> route
    }

    private fun working(p: JSONObject) =
        p.optBoolean("verified") && !p.optBoolean("suspended")

    /** "77.88.8.8:853" -> "77.88.8.8", "[2a0c::1]:53" -> "2a0c::1": the port shows in the transport column. */
    private fun shortResolver(r: String): String {
        val m = Regex("^\\[?(.*?)\\]?:(53|853)$").find(r) ?: return r
        return m.groupValues[1]
    }

    private fun list(a: JSONArray?): String =
        if (a == null) "" else (0 until a.length()).joinToString(", ") { a.optString(it) }

    private fun bytes(n: Long): String = when {
        n < 1024 -> "$n B"
        n < 1024 * 1024 -> String.format(Locale.ROOT, "%.1f KB", n / 1024.0)
        else -> String.format(Locale.ROOT, "%.1f MB", n / (1024.0 * 1024.0))
    }

    /** RFC 3339 from Go (fractional seconds, Z or ±hh:mm) as local HH:mm:ss. */
    fun clock(rfc3339: String): String {
        if (rfc3339.isEmpty()) return "?"
        return try {
            val trimmed = rfc3339.replace(Regex("\\.\\d+"), "")
            val parsed: Date = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.ROOT).parse(trimmed) ?: return rfc3339
            SimpleDateFormat("HH:mm:ss", Locale.ROOT).format(parsed)
        } catch (_: Exception) {
            rfc3339
        }
    }
}
