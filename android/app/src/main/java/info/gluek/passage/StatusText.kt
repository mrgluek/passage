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
        return listOfNotNull(routes(c, s), network(c, s), tunnelState(c, s)).joinToString(" · ")
    }

    /** Notification title and text: the routes, then the network and the tunnel. */
    fun notification(c: Context, s: JSONObject): Pair<String, String> {
        if (!s.optBoolean("running")) return c.getString(R.string.app_name) to c.getString(R.string.not_running)
        return routes(c, s) to listOfNotNull(network(c, s), tunnelState(c, s)).joinToString(" · ")
    }

    /**
     * The network as the last check saw it. Only auto checks the network,
     * so with a fixed route there is nothing to show.
     */
    private fun network(c: Context, s: JSONObject): String? {
        if (s.optString("mode") != "auto") return null
        return c.getString(when (s.optString("state")) {
            "direct" -> R.string.net_open
            "blocked" -> R.string.net_restricted
            "relays-down" -> R.string.net_relays_down
            "offline" -> R.string.net_offline
            else -> R.string.net_unchecked
        })
    }

    /**
     * The routes in one line: "Route: direct" until relays have been seen,
     * then "Mail: direct" (or "Mail: direct (2), tunnel (1)" when relays
     * differ), plus "Telegram: tunnel" once Telegram has been used.
     */
    private fun routes(c: Context, s: JSONObject): String {
        val groups = s.optJSONObject("groups") ?: JSONObject()
        // Counted by name, so relays on iroh and on soar show apart.
        val counts = linkedMapOf<String, Int>()
        val only = onlyType(s)
        for ((_, g) in relays(groups)) {
            val name = routeName(c, g.optString("route"), g.optString("via").ifEmpty { only })
            counts[name] = (counts[name] ?: 0) + 1
        }
        val telegram = groups.optJSONObject("telegram")?.takeIf { (it.optJSONArray("targets")?.length() ?: 0) > 0 }
        if (counts.isEmpty() && telegram == null) {
            return c.getString(R.string.notification_route, routeName(c, s.optString("route"), s.optString("via").ifEmpty { only }))
        }
        val parts = mutableListOf<String>()
        if (counts.isNotEmpty()) {
            val mail = if (counts.size == 1) counts.keys.first()
            else counts.entries.joinToString(", ") { "${it.key} (${it.value})" }
            parts += c.getString(R.string.group_mail, mail)
        }
        if (telegram != null) {
            parts += c.getString(R.string.group_telegram,
                routeName(c, telegram.optString("route"), telegram.optString("via").ifEmpty { only }))
        }
        return parts.joinToString(" · ")
    }

    /**
     * The route type a tunnel connection must use: the chosen one, or the
     * only type set up. Shown before a connection has gone through.
     */
    private fun onlyType(s: JSONObject): String {
        val mode = s.optString("mode")
        if (mode in Settings.ROUTE_KINDS) return mode
        val list = s.optJSONArray("tunnels") ?: return ""
        val types = (0 until list.length()).map { list.getJSONObject(it).optString("type") }.distinct()
        return types.singleOrNull() ?: ""
    }

    /** The relay groups (host name, status), sorted by name. */
    private fun relays(groups: JSONObject): List<Pair<String, JSONObject>> =
        groups.keys().asSequence()
            .mapNotNull { name -> groups.optJSONObject(name)?.takeIf { it.optString("kind") == "relay" }?.let { name to it } }
            .sortedBy { it.first }
            .toList()

    /**
     * Battery drain from consecutive samples while not charging, split by
     * whether the wake lock was held at the start of each interval.
     */
    fun batterySummary(samples: JSONArray): JSONObject {
        val hours = doubleArrayOf(0.0, 0.0)
        val drop = doubleArrayOf(0.0, 0.0)
        val format = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", Locale.US).apply { timeZone = java.util.TimeZone.getTimeZone("UTC") }
        fun time(s: JSONObject) = try { format.parse(s.optString("t").take(19))?.time } catch (_: Exception) { null }
        for (i in 1 until samples.length()) {
            val a = samples.getJSONObject(i - 1)
            val b = samples.getJSONObject(i)
            if (a.optBoolean("charging") || b.optBoolean("charging")) continue
            val ta = time(a) ?: continue
            val tb = time(b) ?: continue
            val h = (tb - ta) / 3_600_000.0
            // Samples need the CPU awake, so without the lock they come late;
            // longer gaps likely span time with the proxy off.
            if (h <= 0 || h > 3.0 || b.optInt("level") > a.optInt("level")) continue
            val k = if (a.optBoolean("wake_lock")) 1 else 0
            hours[k] += h
            drop[k] += (a.optInt("level") - b.optInt("level")).toDouble()
        }
        fun part(k: Int) = JSONObject().put("hours", "%.1f".format(Locale.US, hours[k])).put("percent", drop[k].toInt())
            .put("percent_per_hour", if (hours[k] > 0) "%.2f".format(Locale.US, drop[k] / hours[k]) else JSONObject.NULL)
        return JSONObject().put("with_wake_lock", part(1)).put("without_wake_lock", part(0))
    }

    /** The network test in one line for the log: "12 ok, 3 failed". */
    fun diagnosisSummary(d: JSONObject): String {
        d.optString("error").takeIf { it.isNotEmpty() }?.let { return it }
        var ok = 0
        var failed = 0
        val list = d.optJSONArray("rows") ?: JSONArray()
        for (i in 0 until list.length()) {
            val res = list.getJSONObject(i).optJSONArray("results") ?: continue
            for (j in 0 until res.length()) {
                val x = res.getJSONObject(j)
                when {
                    x.optBoolean("skip") -> {}
                    x.optBoolean("ok") -> ok++
                    else -> failed++
                }
            }
        }
        return "$ok ok, $failed failed"
    }

    /** The network test (tunnel.Diagnosis) as a table: one row per target. */
    fun diagnosis(c: Context, d: JSONObject): String {
        d.optString("error").takeIf { it.isNotEmpty() }?.let { return it }
        val ways = d.optJSONArray("ways") ?: JSONArray()
        val head = listOf(c.getString(R.string.test_target)) + (0 until ways.length()).map { ways.optString(it) }
        val rows = mutableListOf(head)
        val list = d.optJSONArray("rows") ?: JSONArray()
        for (i in 0 until list.length()) {
            val r = list.getJSONObject(i)
            val res = r.optJSONArray("results") ?: JSONArray()
            rows += listOf(r.optString("target").take(32)) + (0 until res.length()).map { j ->
                val x = res.getJSONObject(j)
                when {
                    x.optBoolean("skip") -> "-"
                    x.optBoolean("ok") -> "ok ${x.optLong("ms")}ms"
                    else -> x.optString("error", "fail")
                }
            }
        }
        val widths = head.indices.map { col -> rows.maxOf { it.getOrElse(col) { "" }.length } }
        val sb = StringBuilder(c.getString(R.string.test_time, clock(d.optString("time")))).append("\n\n")
        for (row in rows) {
            sb.append(row.mapIndexed { col, cell -> cell.padEnd(widths[col]) }.joinToString("  ").trimEnd()).append('\n')
        }
        routeChecks(d)?.let { sb.append('\n').append(it) }
        return sb.toString()
    }

    /** Each route's stages (tunnel.RouteCheck), one line per route. */
    private fun routeChecks(d: JSONObject): String? {
        val list = d.optJSONArray("checks") ?: return null
        return (0 until list.length()).joinToString("") { i ->
            val r = list.getJSONObject(i)
            val stages = r.optJSONArray("stages") ?: JSONArray()
            val cells = (0 until stages.length()).map { j ->
                val s = stages.getJSONObject(j)
                val result = if (s.optBoolean("ok")) "ok ${s.optLong("ms")}ms" else s.optString("error", "fail")
                val bytes = s.optLong("bytes").takeIf { it > 0 }?.let { " ${it}B" } ?: ""
                val target = s.optString("target").takeIf { it.isNotEmpty() }?.let { " ($it)" } ?: ""
                val received = s.optLong("received").takeIf { it > 0 }?.let { " [session ${it}B]" } ?: ""
                "${s.optString("stage")} $result$bytes$target$received"
            }
            "${r.optString("route")}: ${cells.joinToString(", ")}\n"
        }
    }

    /** Each tunnel route's state ("iroh: up, soar: idle"), or "tunnel: no link". */
    private fun tunnelState(c: Context, s: JSONObject): String {
        val rows = tunnels(c, s)
        if (rows.isEmpty()) return c.getString(R.string.tunnel_label, c.getString(R.string.tunnel_no_link))
        return rows.joinToString(", ") { c.getString(R.string.tunnel_route, it.first, it.second) }
    }

    /**
     * The tunnel routes as (name, state). The name is the route type, or the
     * route ID when two routes share a type.
     */
    private fun tunnels(c: Context, s: JSONObject): List<Pair<String, String>> {
        val list = s.optJSONArray("tunnels") ?: return emptyList()
        val rows = (0 until list.length()).map { list.getJSONObject(it) }
        val types = rows.groupingBy { it.optString("type") }.eachCount()
        return rows.map { r ->
            val type = r.optString("type")
            val name = if ((types[type] ?: 0) > 1) r.optString("id") else Ui.routeLabel(c, type)
            name to routeStateName(c, r.optString("state"))
        }
    }

    private fun routeStateName(c: Context, state: String): String = when (state) {
        "up" -> c.getString(R.string.route_state_up)
        "failed" -> c.getString(R.string.route_state_failed)
        "idle" -> c.getString(R.string.route_state_idle)
        else -> state
    }

    fun full(c: Context, s: JSONObject): String {
        if (!s.optBoolean("running")) return c.getString(R.string.proxy_not_running)
        val sb = StringBuilder()
        sb.append(c.getString(R.string.status_route, routeName(c, s.optString("route"), s.optString("via")).uppercase(),
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
        val relays = relays(s.optJSONObject("groups") ?: JSONObject())
        if (relays.isEmpty()) {
            sb.append(c.getString(R.string.status_probe_hosts, c.getString(R.string.none_yet))).append('\n')
        } else {
            sb.append(c.getString(R.string.status_relays)).append('\n')
            for ((name, g) in relays) {
                sb.append(c.getString(R.string.status_relay, name, routeName(c, g.optString("route"), g.optString("via")),
                    stateName(c, g.optString("state")))).append('\n')
            }
        }
        if ((s.optString("zone").isEmpty() && s.optInt("route_count") == 0)) {
            sb.append(c.getString(R.string.status_no_tunnel)).append('\n')
        } else {
            sb.append(c.getString(R.string.status_session, tunnelState(c, s), s.optInt("resolvers"),
                s.optString("operator").ifEmpty { "?" })).append('\n')
            sb.append(c.getString(if (s.optBoolean("tunnel_ipv6")) R.string.server_ipv6_on else R.string.server_ipv6_off)).append('\n')
        }
        sb.append(c.getString(R.string.status_network_resolvers,
            list(s.optJSONArray("network_resolvers")).ifEmpty { c.getString(R.string.none) })).append('\n')
        if (s.optBoolean("dns_intercepted")) {
            val via = s.optString("dns_egress")
            sb.append(if (via.isNotEmpty()) c.getString(R.string.status_intercepted_via, via)
                else c.getString(R.string.status_intercepted)).append('\n')
        }
        val active = s.optJSONObject("active") ?: JSONObject()
        sb.append(c.getString(R.string.status_open_conns, active.optInt("direct") + active.optInt("smart"), active.optInt("tunnel"))).append('\n')
        val stats = s.optJSONObject("stats") ?: JSONObject()
        for ((route, label) in listOf(
            "direct" to R.string.status_direct, "smart" to R.string.status_smart, "tunnel" to R.string.status_tunnel,
        )) {
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
    fun report(c: Context, s: JSONObject, net: NetInfo.Snapshot, settings: Settings, version: String, networkTest: String? = null): String {
        Diagnostics.collectExits(c)
        val r = JSONObject(s.toString())
        r.remove("zone")
        r.put("app", JSONObject()
            .put("version", version)
            .put("listen", settings.listen)
            .put("auth", settings.auth)
            .put("mode_setting", settings.mode)
            .put("last_error", PassageService.lastError?.let(Diagnostics::redact) ?: JSONObject.NULL))
        r.put("device", Diagnostics.device())
        val diagnostics = Diagnostics.snapshot()
        r.put("diagnostics", diagnostics)
        diagnostics.optJSONArray("battery")?.let { r.put("battery_summary", batterySummary(it)) }
        r.put("device_network", JSONObject()
            .put("transport", net.transport)
            .put("dns", JSONArray(net.dns))
            .put("operator", net.operator)
            .put("private_dns", net.privateDns))
        if (networkTest != null) r.put("network_test", JSONArray(networkTest.trimEnd().lines()))
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

    /** The route for people; a tunnel shows the route type that carries it (iroh, soar), when known. */
    private fun routeName(c: Context, route: String, via: String = ""): String = when (route) {
        "direct" -> c.getString(R.string.route_direct)
        "tunnel" -> if (via.isEmpty()) c.getString(R.string.route_tunnel) else Ui.routeLabel(c, via)
        "smart" -> c.getString(R.string.route_smart)
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
