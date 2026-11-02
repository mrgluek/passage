package info.gluek.passage

import android.content.Context
import java.security.SecureRandom

/** What the user entered. The tunnel link stays in app-private storage only. */
class Settings(context: Context) {
    private val prefs = context.getSharedPreferences("passage", Context.MODE_PRIVATE)

    var link: String
        get() = prefs.getString("link", "") ?: ""
        set(v) = prefs.edit().putString("link", v.trim()).apply()

    var routesConfig: String
        get() = prefs.getString("routes_config", "") ?: ""
        private set(v) { prefs.edit().putString("routes_config", v).apply() }

    fun saveRoutes(link: String, config: String) {
        prefs.edit().putString("link", link).putString("routes_config", config).apply()
    }

    /** auto, always or off. */
    var mode: String
        get() = prefs.getString("mode", "auto") ?: "auto"
        set(v) = prefs.edit().putString("mode", v).apply()


    /** The user wants the proxy on: a system restart of the service resumes it. */
    var running: Boolean
        get() = prefs.getBoolean("running", false)
        set(v) = prefs.edit().putBoolean("running", v).apply()

    var keepCpuAwake: Boolean
        get() = prefs.getBoolean("keep_cpu_awake", true)
        set(v) = prefs.edit().putBoolean("keep_cpu_awake", v).apply()

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

    /**
     * The proxy as a t.me/socks link: messaging apps accept it in their proxy
     * settings, and some add the proxy when it is tapped. [scheme] "tg://socks"
     * opens it in an app directly.
     */
    fun proxyLink(scheme: String = "https://t.me/socks"): String {
        val creds = if (auth) "&user=$user&pass=$pass" else ""
        return "$scheme?server=$HOST&port=$port$creds"
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
        const val HOST = "127.0.0.1"
        private const val MIN_PORT = 20000
        private const val MAX_PORT = 60000
        private val random = SecureRandom()

        private fun hex(bytes: Int): String {
            val b = ByteArray(bytes)
            random.nextBytes(b)
            return b.joinToString("") { "%02x".format(it) }
        }
    }
}
