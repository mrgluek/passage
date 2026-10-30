package info.gluek.passage

import android.content.Context
import java.security.SecureRandom

/** What the user entered. The tunnel link stays in app-private storage only. */
class Settings(context: Context) {
    private val prefs = context.getSharedPreferences("passage", Context.MODE_PRIVATE)

    var link: String
        get() = prefs.getString("link", "") ?: ""
        set(v) = prefs.edit().putString("link", v.trim()).apply()

    /** auto, always or off. */
    var mode: String
        get() = prefs.getString("mode", "auto") ?: "auto"
        set(v) = prefs.edit().putString("mode", v).apply()

    /** Hosts probed to decide the route. */
    var probeHosts: String
        get() = prefs.getString("probe", "") ?: ""
        set(v) = prefs.edit().putString("probe", v.trim()).apply()

    /**
     * Start every resolver path at soar's short 101-char query names and let
     * soar grow them to 253, instead of starting at 253.
     */
    var shortNames: Boolean
        get() = prefs.getBoolean("short_names", false)
        set(v) = prefs.edit().putBoolean("short_names", v).apply()

    /** The user wants the proxy on: a system restart of the service resumes it. */
    var running: Boolean
        get() = prefs.getBoolean("running", false)
        set(v) = prefs.edit().putBoolean("running", v).apply()

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

    /** The proxy as a socks5:// link that other apps accept in their proxy settings. */
    fun proxyLink(): String =
        if (auth) "socks5://$user:$pass@$HOST:$port" else "socks5://$HOST:$port"

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

    fun probeTargets(): List<String> = splitHosts(probeHosts)

    companion object {
        const val HOST = "127.0.0.1"
        private const val MIN_PORT = 20000
        private const val MAX_PORT = 60000
        private val random = SecureRandom()

        private fun hex(bytes: Int): String {
            val b = ByteArray(bytes)
            random.nextBytes(b)
            return b.joinToString("") { "%02x".format(it) }
        }

        fun splitHosts(s: String): List<String> =
            s.split(',', ' ', '\n', ';').map { it.trim() }.filter { it.isNotEmpty() }
    }
}
