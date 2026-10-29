package info.gluek.passage

import android.content.Context

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

    fun probeTargets(): List<String> = splitHosts(probeHosts)

    companion object {
        fun splitHosts(s: String): List<String> =
            s.split(',', ' ', '\n', ';').map { it.trim() }.filter { it.isNotEmpty() }
    }
}
