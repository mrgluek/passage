package info.gluek.passage

import android.content.Context
import org.json.JSONObject
import java.security.SecureRandom

/** What the user entered. The tunnel link stays in app-private storage only. */
class Settings(context: Context) {
    private val prefs = context.getSharedPreferences("passage", Context.MODE_PRIVATE)

    var theme: String
        get() = prefs.getString("theme", "system").takeIf { it in THEMES } ?: "system"
        set(v) = prefs.edit().putString("theme", v.takeIf { it in THEMES } ?: "system").apply()

    var link: String
        get() = prefs.getString("link", "") ?: ""
        set(v) = prefs.edit().putString("link", v.trim()).apply()

    var routesConfig: String
        get() = prefs.getString("routes_config", "") ?: ""
        private set(v) { prefs.edit().putString("routes_config", v).apply() }

    /** Save routes and reset a removed selection to Auto. */
    fun saveRoutes(link: String, config: String) = synchronized(configLock) {
        prefs.edit().putString("link", link).putString("routes_config", config).apply()
        prefs.edit().putString("mode", mode).apply()
    }

    data class Route(val id: String, val type: String, val priority: Int, val subscription: String = "") {
        val mode: String get() = if (type == "my") "my" else "route:$id"
    }

    data class SavedRoute(
        val id: String,
        val type: String,
        val originalPriority: Int,
        var priority: Int,
        var on: Boolean,
        val subscription: String = "",
    )

    val subscriptions: List<JSONObject>
        get() = try { Subscriptions.rows(JSONObject(routesConfig)) } catch (_: Exception) { emptyList() }

    /** Compare and save together so an older refresh cannot overwrite a route edit. */
    fun saveCandidate(previous: String, config: JSONObject): Boolean = synchronized(configLock) {
        if (routesConfig != previous) return@synchronized false
        if (!prefs.edit().putString("routes_config", config.toString()).commit()) return@synchronized false
        mode = mode
        true
    }

    fun saveSubscriptionRefresh(previous: String, config: JSONObject): Boolean = synchronized(configLock) {
        val notice = Subscriptions.refreshNotice(Subscriptions.request(previous).getJSONObject("existing"), config, System.currentTimeMillis())
        if (!saveCandidate(previous, config)) return@synchronized false
        if (notice != null) {
            routesNotice = notice.encode()
            Diagnostics.event("routes updated: +${notice.added}, -${notice.removed}")
        }
        true
    }

    fun saveSubscriptionRemoval(previous: String, config: JSONObject): Boolean = synchronized(configLock) {
        if (!saveCandidate(previous, config)) return@synchronized false
        routesNotice = ""
        true
    }

    var routesNotice: String
        get() = prefs.getString("routes_notice", "") ?: ""
        set(v) = prefs.edit().putString("routes_notice", v).apply()

    /** Old type selections migrate to the first matching installed route. */
    var mode: String
        get() {
            val saved = prefs.getString("mode", "auto") ?: "auto"
            if (saved == "auto" || saved == "off") return saved
            val available = availableRoutes
            val resolved = available.firstOrNull { it.mode == saved }?.mode
                ?: (if (saved == "always") available.firstOrNull() else available.firstOrNull { it.type == saved })?.mode
                ?: "auto"
            if (resolved != saved) prefs.edit().putString("mode", resolved).apply()
            return resolved
        }
        set(v) = prefs.edit().putString("mode", v).apply()

    /** The user's SOCKS5 proxy, stored separately from link routes. */
    var myRoute: String
        get() = prefs.getString("my_route", "") ?: ""
        set(v) = prefs.edit().putString("my_route", v.trim()).apply()

    private val availableRoutes: List<Route>
        get() = (installedRoutes + if (myRoute.isEmpty()) emptyList() else listOf(Route("", "my", MY_PRIORITY)))
            .sortedWith(compareBy({ it.priority }, { if (it.type == "my") 0 else 1 }, { it.id }))

    val routeKinds: List<String> get() = availableRoutes.map { it.type }.distinct()

    /** Only the ID, type and priority are used in the route menu; off routes are omitted. */
    val installedRoutes: List<Route>
        get() {
            val legacy = listOf(Route("legacy-dns", "soar", 100))
            val config = routesConfig
            if (config.isEmpty()) return if (link.isEmpty()) emptyList() else legacy
            return try {
                val json = JSONObject(config)
                val routes = json.optJSONArray("routes")
                    ?: return if (json.optString("zone").isNotEmpty()) legacy else emptyList()
                val edits = json.optJSONObject("route_edits")
                (0 until routes.length()).map { routes.getJSONObject(it) }
                    .filter { r ->
                        val id = r.getString("id")
                        val edit = edits?.optJSONObject(id)
                        !(edit != null && edit.optBoolean("off"))
                    }
                    .map { r ->
                        val id = r.getString("id")
                        val edit = edits?.optJSONObject(id)
                        val p = if (edit != null && edit.has("priority")) edit.optInt("priority") else r.optInt("priority")
                        Route(id, r.getString("type"), p, Subscriptions.owner(json, id))
                    }
                    .filter { it.type in ROUTE_KINDS && it.type != "my" }
                    .sortedWith(compareBy({ it.priority }, { it.id }))
            } catch (_: Exception) {
                emptyList()
            }
        }

    /** Every installed route with its local edits, with off routes last. */
    val savedRoutes: List<SavedRoute>
        get() {
            val config = routesConfig
            if (config.isEmpty()) return emptyList()
            return try {
                val json = JSONObject(config)
                val routes = json.optJSONArray("routes") ?: return emptyList()
                val edits = json.optJSONObject("route_edits")
                (0 until routes.length()).map { routes.getJSONObject(it) }
                    .map { r ->
                        val id = r.getString("id")
                        val type = r.getString("type")
                        val origPriority = r.optInt("priority")
                        val edit = edits?.optJSONObject(id)
                        val p = if (edit != null && edit.has("priority")) edit.optInt("priority") else origPriority
                        val on = !(edit != null && edit.optBoolean("off"))
                        SavedRoute(id, type, origPriority, p, on, Subscriptions.owner(json, id))
                    }
                    .sortedWith(compareBy<SavedRoute> { !it.on }.thenBy { it.priority }.thenBy { it.id })
            } catch (_: Exception) {
                emptyList()
            }
        }

    /** Saves local route edits (priority, on/off) and resets an inactive route choice to Auto. */
    fun saveRouteEdits(edits: List<SavedRoute>) = synchronized(configLock) {
        val config = routesConfig
        if (config.isEmpty()) return@synchronized
        try {
            val json = JSONObject(config)
            val routes = json.optJSONArray("routes") ?: return@synchronized
            val linkRoutes = (0 until routes.length()).map { routes.getJSONObject(it) }
                .associate { it.getString("id") to it.optInt("priority") }
            val editsObj = JSONObject()
            for (r in edits) {
                val origPriority = linkRoutes[r.id] ?: continue
                val diffPriority = r.priority != origPriority
                val isOff = !r.on
                if (!diffPriority && !isOff) continue
                val edit = JSONObject()
                if (diffPriority) edit.put("priority", r.priority.coerceIn(0, 65535))
                if (isOff) edit.put("off", true)
                editsObj.put(r.id, edit)
            }
            if (editsObj.length() > 0) {
                json.put("route_edits", editsObj)
            } else {
                json.remove("route_edits")
            }
            routesConfig = json.toString()
            mode = mode
        } catch (_: Exception) {
        }
    }


    /** The user wants the proxy on: a system restart of the service resumes it. */
    var running: Boolean
        get() = prefs.getBoolean("running", false)
        set(v) = prefs.edit().putBoolean("running", v).apply()


    /** Check for a new version once a day while the proxy runs. */
    var updateChecks: Boolean
        get() = prefs.getBoolean("update_checks", true)
        set(v) = prefs.edit().putBoolean("update_checks", v).apply()

    var nextUpdateCheck: Long
        get() = prefs.getLong("next_update_check", 0)
        set(v) = prefs.edit().putLong("next_update_check", v).apply()

    var availableVersion: String
        get() = prefs.getString("available_version", "") ?: ""
        set(v) = prefs.edit().putString("available_version", v).apply()

    /** The newest version the user was told about, to tell them once. */
    var notifiedVersion: String
        get() = prefs.getString("notified_version", "") ?: ""
        set(v) = prefs.edit().putString("notified_version", v).apply()

    /** The notification explainer was shown (Android 13+ asks at most twice). */
    var askedNotifications: Boolean
        get() = prefs.getBoolean("asked_notifications", false)
        set(v) = prefs.edit().putBoolean("asked_notifications", v).apply()

    /**
     * Require the SOCKS5 username and password. Off ("open mode") is for
     * clients that can't send them: then any app on the phone can use the port.
     */
    var auth: Boolean
        get() = prefs.getBoolean("auth", true)
        set(v) = prefs.edit().putBoolean("auth", v).apply()

    /** Loopback port, picked at random on first use and kept: apps store it. */
    val port: Int
        get() {
            ensureAccess()
            return prefs.getInt("port", 0)
        }

    val user: String
        get() {
            ensureAccess()
            return prefs.getString("user", "") ?: ""
        }

    val pass: String
        get() {
            ensureAccess()
            return prefs.getString("pass", "") ?: ""
        }

    val listen: String get() = "$HOST:$port"

    /** The proxy as a socks5:// URL, which apps take in their proxy settings. */
    fun socksLink(): String {
        val creds = if (auth) "$user:$pass@" else ""
        return "socks5://$creds$HOST:$port"
    }

    /** The proxy as a tg://socks link: opening it hands the proxy to an app directly. */
    fun tgLink(): String {
        val creds = if (auth) "&user=$user&pass=$pass" else ""
        return "tg://socks?server=$HOST&port=$port$creds"
    }

    /** New port, username and password; apps set up with the old ones must be updated. */
    fun newAccess() {
        prefs.edit()
            .putInt("port", MIN_PORT + random.nextInt(MAX_PORT - MIN_PORT))
            .putString("user", hex(4))
            .putString("pass", hex(16))
            .apply()
    }

    private fun ensureAccess() {
        if (prefs.getInt("port", 0) == 0) newAccess()
    }

    companion object {
        val configLock = Any()
        val THEMES = listOf("system", "light", "dark")
        const val HOST = "127.0.0.1"
        /** Route types that can be chosen, as in the Go core's RouteKinds. */
        val ROUTE_KINDS = listOf("my", "http", "wss", "turn", "iroh", "soar")
        /** The Go core's myPriority. */
        private const val MY_PRIORITY = 10

        /**
         * [s] in the stored form [user:pass@]host:port, or null if it is not a
         * SOCKS5 address. Also takes socks://, socks5:// and socks5h:// URLs and
         * t.me/socks or tg://socks links, like the Go core's parseMy.
         */
        fun normalizeMyRoute(s: String): String? {
            try {
                val text = s.trim()
                val uri = java.net.URI(if ("://" in text) text else "socks5://$text")
                val scheme = uri.scheme?.lowercase()
                val host: String?
                val port: Int
                var user = ""
                var pass = ""
                when {
                    scheme == "socks" || scheme == "socks5" || scheme == "socks5h" -> {
                        if (!uri.rawPath.isNullOrEmpty() && uri.rawPath != "/" || uri.rawQuery != null) return null
                        host = uri.host
                        port = uri.port
                        uri.rawUserInfo?.let {
                            user = decode(it.substringBefore(':').replace("+", "%2B"))
                            pass = decode(it.substringAfter(':', "").replace("+", "%2B"))
                        }
                    }
                    scheme == "tg" && uri.host.equals("socks", true) ||
                        (scheme == "https" || scheme == "http") && (uri.host.equals("t.me", true) || uri.host.equals("telegram.me", true)) &&
                        uri.rawPath?.trimEnd('/') == "/socks" -> {
                        val q = (uri.rawQuery ?: "").split('&').filter { '=' in it }
                            .associate { decode(it.substringBefore('=')) to decode(it.substringAfter('=')) }
                        host = q["server"]
                        port = q["port"]?.toIntOrNull() ?: -1
                        user = q["user"] ?: ""
                        pass = q["pass"] ?: ""
                    }
                    else -> return null
                }
                if (host.isNullOrEmpty() || port !in 1..65535 || user.isEmpty() && pass.isNotEmpty()) return null
                val hostPort = (if (':' in host && !host.startsWith("[")) "[$host]" else host) + ":$port"
                return if (user.isEmpty()) hostPort else "${encode(user)}:${encode(pass)}@$hostPort"
            } catch (_: Exception) {
                return null
            }
        }

        /** Query decoding: + is a space, as in Go's url.ParseQuery. */
        private fun decode(s: String) = java.net.URLDecoder.decode(s, "UTF-8")
        private fun encode(s: String) = java.net.URLEncoder.encode(s, "UTF-8").replace("+", "%20")
        // Below Linux's ephemeral range (32768-60999): a client retrying a
        // stopped proxy could otherwise get the port as its own source port,
        // connect to itself and block the restart for a minute.
        private const val MIN_PORT = 20000
        private const val MAX_PORT = 32768 // exclusive
        private val random = SecureRandom()

        private fun hex(bytes: Int): String {
            val b = ByteArray(bytes)
            random.nextBytes(b)
            return b.joinToString("") { "%02x".format(it) }
        }
    }
}
