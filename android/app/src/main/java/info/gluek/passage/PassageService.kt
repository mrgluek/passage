package info.gluek.passage

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Keeps the SOCKS5 proxy on loopback alive in the background: a
 * foreground service with a partial wake lock that restarts the tunnel when
 * the network's resolvers or the SIM operator change, and retries a start
 * that failed (port busy, bad network).
 */
class PassageService : Service() {
    companion object {
        private const val ACTION_START = "info.gluek.passage.START"
        private const val ACTION_STOP = "info.gluek.passage.STOP"
        private const val ACTION_NOTIFY = "info.gluek.passage.NOTIFY"
        private const val CHANNEL = "proxy"
        private const val NOTIFICATION_ID = 1
        private const val TICK_MS = 15_000L
        private const val NET_SETTLE_MS = 2_000L
        /** soar's own starting name length (Config.SafeQName default). */
        private const val SHORT_QNAME = 101

        /** The service is up (the proxy may still be starting or retrying). */
        @Volatile
        var active = false
            private set

        /** Why the last start failed, shown in the app until a start succeeds. */
        @Volatile
        var lastError: String? = null
            private set

        fun start(context: Context) {
            val i = Intent(context, PassageService::class.java).setAction(ACTION_START)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) context.startForegroundService(i) else context.startService(i)
        }

        /** Redraws the notification now, e.g. once notifications are allowed. */
        fun refreshNotification(context: Context) {
            if (active) context.startService(Intent(context, PassageService::class.java).setAction(ACTION_NOTIFY))
        }

        fun stop(context: Context) {
            context.startService(Intent(context, PassageService::class.java).setAction(ACTION_STOP))
        }
    }

    private val worker = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private var wakeLock: PowerManager.WakeLock? = null
    private var netCallback: ConnectivityManager.NetworkCallback? = null

    // Touched on the worker thread only.
    private var lastNet: Pair<List<String>, String>? = null

    private val tick = object : Runnable {
        override fun run() {
            bg { checkNetwork(nudge = false); updateNotification() }
            main.postDelayed(this, TICK_MS)
        }
    }
    private val netKick = Runnable { bg { checkNetwork(nudge = true); updateNotification() } }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_NOTIFY) {
            if (active) bg { updateNotification() }
            return START_STICKY
        }
        if (intent?.action == ACTION_STOP) {
            shutdown()
            return START_NOT_STICKY
        }
        // ACTION_START, or the system restarting a sticky service (null intent).
        if (intent == null && !Settings(this).running) {
            stopSelf()
            return START_NOT_STICKY
        }
        goForeground()
        if (!active) {
            begin()
        } else {
            // Start while running = apply the edited link / probe hosts.
            bg { startTunnel("restart"); updateNotification() }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        if (active) release()
        worker.shutdown()
        super.onDestroy()
    }

    private fun begin() {
        active = true
        Settings(this).running = true
        lastError = null
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "passage:proxy").apply {
            setReferenceCounted(false)
            acquire()
        }
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = settle()
            override fun onLost(network: Network) = settle()
            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = settle()
        }
        try {
            cm.registerDefaultNetworkCallback(cb)
            netCallback = cb
        } catch (_: Exception) {
        }
        bg { startTunnel("start"); updateNotification() }
        main.postDelayed(tick, TICK_MS)
    }

    /** Network events come in bursts (Wi-Fi off = lost + available + link changes). */
    private fun settle() {
        main.removeCallbacks(netKick)
        main.postDelayed(netKick, NET_SETTLE_MS)
    }

    private fun shutdown() {
        Settings(this).running = false
        if (active) release()
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun release() {
        active = false
        main.removeCallbacksAndMessages(null)
        netCallback?.let {
            try {
                (getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager).unregisterNetworkCallback(it)
            } catch (_: Exception) {
            }
        }
        netCallback = null
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = null
        bg { Native.stop() }
    }

    /**
     * Runs on the worker thread. An exception escaping an executor task would
     * crash the app (a missing native library throws on every Native call).
     */
    private fun bg(task: () -> Unit) {
        try {
            worker.execute {
                try {
                    task()
                } catch (e: Throwable) {
                    lastError = e.message ?: e.toString()
                }
            }
        } catch (_: java.util.concurrent.RejectedExecutionException) {
            // The service is being destroyed.
        }
    }

    /** Worker thread: (re)starts the proxy with the current network's resolvers. */
    private fun startTunnel(why: String) {
        if (!active) return
        val s = Settings(this)
        try {
            // Without a link the router runs direct and smart routes only.
            val cfg = if (s.link.isEmpty()) JSONObject() else Native.parse(s.link)
            cfg.optString("error").takeIf { it.isNotEmpty() }?.let { throw IllegalArgumentException("link: $it") }
            val net = NetInfo.snapshot(this)
            cfg.put("listen", s.listen)
            if (s.auth) {
                cfg.put("user", s.user)
                cfg.put("pass", s.pass)
            }
            cfg.put("mode", s.mode)
            cfg.put("probe_targets", JSONArray(s.probeTargets()))
            if (s.shortNames) cfg.put("safe_qname", SHORT_QNAME)
            cfg.put("system_resolvers", JSONArray(net.dns))
            cfg.put("operator", net.operator)
            cfg.put("state_file", java.io.File(filesDir, "router-state.json").path)
            val out = Native.json(Native.start(cfg.toString().toByteArray(Charsets.UTF_8)))
            out.optString("error").takeIf { it.isNotEmpty() }?.let { throw IllegalStateException(it) }
            lastNet = net.key
            lastError = null
        } catch (e: Throwable) {
            lastError = "$why failed: ${e.message ?: e}"
        }
    }

    /**
     * Worker thread: a dead proxy is started again; new network resolvers or
     * a new operator restart it; otherwise a network event only asks the
     * router to re-probe at once.
     */
    private fun checkNetwork(nudge: Boolean) {
        if (!active) return
        val running = try {
            Native.status().optBoolean("running")
        } catch (_: Throwable) {
            false
        }
        if (!running) {
            startTunnel("retry")
            return
        }
        if (NetInfo.snapshot(this).key != lastNet) {
            startTunnel("restart on network change")
        } else if (nudge) {
            Native.networkChanged()
        }
    }

    private fun goForeground() {
        val n = buildNotification(getString(R.string.app_name), getString(R.string.notification_starting))
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }

    private fun updateNotification() {
        if (!active) return
        val (title, text) = lastError?.let { getString(R.string.app_name) to it } ?: try {
            StatusText.notification(this, Native.status())
        } catch (e: Throwable) {
            getString(R.string.app_name) to (e.message ?: e.toString())
        }
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIFICATION_ID, buildNotification(title, text))
    }

    private fun buildNotification(title: String, text: String): Notification {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            if (nm.getNotificationChannel(CHANNEL) == null) {
                nm.createNotificationChannel(NotificationChannel(CHANNEL, getString(R.string.notification_channel), NotificationManager.IMPORTANCE_LOW))
            }
            Notification.Builder(this, CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), flags)
        val stop = PendingIntent.getService(
            this, 1, Intent(this, PassageService::class.java).setAction(ACTION_STOP), flags,
        )
        @Suppress("DEPRECATION")
        return b.setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(0, getString(R.string.stop), stop).build())
            .build()
    }
}
