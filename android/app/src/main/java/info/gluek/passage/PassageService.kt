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
import android.os.BatteryManager
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
    override fun attachBaseContext(newBase: Context) = super.attachBaseContext(AppLanguage.wrap(newBase))

    companion object {
        @Volatile private var instance: PassageService? = null

        fun <T> withSubscriptionRouter(previous: String, refresh: () -> T): T {
            val service = instance
            if (active && service != null) {
                return service.worker.submit<T> {
                    if (active && runningRoutesConfig != previous) service.startTunnel("subscription imported")
                    refresh()
                }.get()
            }
            return refresh()
        }

        private const val ACTION_START = "info.gluek.passage.START"
        private const val ACTION_STOP = "info.gluek.passage.STOP"
        private const val ACTION_NOTIFY = "info.gluek.passage.NOTIFY"
        private const val CHANNEL = "proxy"
        private const val NOTIFICATION_ID = 1
        private const val TICK_MS = 60_000L
        private const val RETRY_MS = 15_000L
        private const val NET_SETTLE_MS = 2_000L
        private const val BATTERY_SAMPLE_MS = 10 * 60_000L
        private const val UPDATE_SETTLE_MS = 5 * 60_000L

        /** The service is up (the proxy may still be starting or retrying). */
        @Volatile
        var active = false
            private set

        /** Why the last start failed, shown in the app until a start succeeds. */
        @Volatile
        var lastError: String? = null
            private set

        @Volatile
        var runningRoutesConfig: String? = null
            private set

        fun savedRefreshMetadata(previous: String, current: String) {
            if (runningRoutesConfig == previous) runningRoutesConfig = current
        }

        fun start(context: Context, reason: String = "user start") {
            val i = Intent(context, PassageService::class.java).setAction(ACTION_START)
                .putExtra("reason", reason)
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
    private var lastNotification: Pair<String, String>? = null
    private var lastBatterySample = 0L
    private val startedAt = System.currentTimeMillis()

    private val tick = object : Runnable {
        override fun run() {
            bg {
                try {
                    checkNetwork(nudge = false)
                    val status = status()
                    Diagnostics.routerEvents(status)
                    updateWakeLock(status)
                    updateNotification(status)
                    sampleBattery()
                    checkForUpdate()
                } finally {
                    // With a Soar route, check often: its polling needs the
                    // wake lock soon after it starts carrying connections.
                    val delay = if (lastError != null || "soar" in Settings(this@PassageService).routeKinds) RETRY_MS else TICK_MS
                    main.post {
                        if (active && !worker.isShutdown) {
                            main.removeCallbacks(this)
                            main.postDelayed(this, delay)
                        }
                    }
                }
            }
        }
    }
    private val netKick = Runnable { bg { checkNetwork(nudge = true); updateNotification() } }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        Diagnostics.event("service created")
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_NOTIFY) {
            if (active) bg { updateNotification(force = true) }
            return START_STICKY
        }
        if (intent?.action == ACTION_STOP) {
            Diagnostics.event("service stop requested")
            shutdown()
            return START_NOT_STICKY
        }
        // ACTION_START, or the system restarting a sticky service (null intent).
        if (intent == null && !Settings(this).running) {
            Diagnostics.event("system service restart ignored: proxy was stopped")
            stopSelf()
            return START_NOT_STICKY
        }
        val reason = if (intent == null) "system service restart" else intent.getStringExtra("reason") ?: "start requested"
        Diagnostics.event("service start: $reason active=$active flags=$flags")
        goForeground()
        if (!active) {
            begin(reason)
        } else {
            bg { startTunnel(reason); updateNotification() }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        instance = null
        Diagnostics.event("service destroyed")
        if (active) release()
        worker.shutdown()
        super.onDestroy()
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        Diagnostics.event("app task removed")
        super.onTaskRemoved(rootIntent)
    }

    private fun begin(reason: String) {
        active = true
        Settings(this).running = true
        lastError = null
        updateWakeLock()
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = settle()
            override fun onLost(network: Network) = settle()
            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = settle()
        }
        try {
            cm.registerDefaultNetworkCallback(cb)
            netCallback = cb
        } catch (e: Exception) {
            Diagnostics.error("network callback registration failed", e)
        }
        bg { startTunnel(reason); updateNotification() }
        main.postDelayed(tick, RETRY_MS)
    }

    /** The router's status, or null while it can't be read. */
    private fun status(): JSONObject? = try { Native.json(Native.status(500)) } catch (_: Throwable) { null }

    /**
     * The CPU stays awake while a Soar route carries connections: it polls
     * and nothing from the network would wake the phone for it. Other routes
     * get incoming data and let the phone sleep. Changes go to the log.
     */
    private fun updateWakeLock(status: JSONObject? = null) {
        val reason = if (active && soarInUse(status)) "soar in use" else null
        val held = wakeLock?.isHeld == true
        if (reason != null && !held) {
            val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
            wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "passage:proxy").apply {
                setReferenceCounted(false)
                acquire()
            }
            Diagnostics.event("wake lock on: $reason")
        } else if (reason == null && held) {
            wakeLock?.release()
            wakeLock = null
            Diagnostics.event("wake lock off")
        }
    }

    private fun soarInUse(status: JSONObject?): Boolean {
        val tunnels = status?.optJSONArray("tunnels") ?: return false
        return (0 until tunnels.length()).any {
            val t = tunnels.getJSONObject(it)
            t.optString("type") == "soar" && t.optString("state") == "up"
        }
    }

    /** One battery sample every BATTERY_SAMPLE_MS for the report (while the proxy runs). */
    private fun sampleBattery() {
        val now = System.currentTimeMillis()
        if (now - lastBatterySample < BATTERY_SAMPLE_MS) return
        lastBatterySample = now
        try {
            val bm = getSystemService(BatteryManager::class.java)
            val pm = getSystemService(PowerManager::class.java)
            fun prop(id: Int): Any = bm.getIntProperty(id).let { if (it == 0 || it == Int.MIN_VALUE) JSONObject.NULL else it }
            Diagnostics.battery(JSONObject()
                .put("level", bm.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY))
                .put("charge_uah", prop(BatteryManager.BATTERY_PROPERTY_CHARGE_COUNTER))
                .put("current_ua", prop(BatteryManager.BATTERY_PROPERTY_CURRENT_NOW))
                .put("charging", bm.isCharging)
                .put("screen_on", pm.isInteractive)
                .put("doze", pm.isDeviceIdleMode)
                .put("wake_lock", wakeLock?.isHeld == true))
        } catch (e: Throwable) {
            Diagnostics.error("battery sample failed", e)
        }
    }

    /** On its own thread: a check can take most of a minute. */
    private fun checkForUpdate() {
        if (System.currentTimeMillis() - startedAt < UPDATE_SETTLE_MS || !Updater.due(this)) return
        val app = applicationContext
        Thread({ Updater.checkNow(app) }, "passage-update-check").start()
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
        bg {
            Diagnostics.routerEvents(status())
            Native.stop()
        }
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
                    Diagnostics.error("service task failed", e)
                }
            }
        } catch (_: java.util.concurrent.RejectedExecutionException) {
            // The service is being destroyed.
        }
    }

    /** Worker thread: (re)starts the proxy with the current network's resolvers. */
    private fun startTunnel(why: String) {
        if (!active) return
        lastNotification = null
        val s = Settings(this)
        Diagnostics.event("router start: $why mode=${s.mode}")
        try {
            // Without a link the router runs direct and smart routes only.
            val savedConfig = s.routesConfig
            val cfg = if (savedConfig.isNotEmpty()) JSONObject(savedConfig) else if (s.link.isEmpty()) JSONObject() else Native.parse(s.link)
            cfg.optString("error").takeIf { it.isNotEmpty() }?.let { throw IllegalArgumentException("link: $it") }
            val net = NetInfo.snapshot(this)
            cfg.put("listen", s.listen)
            if (s.auth) {
                cfg.put("user", s.user)
                cfg.put("pass", s.pass)
            } else {
                cfg.remove("user")
                cfg.remove("pass")
            }
            cfg.put("mode", s.mode)
            if (s.myRoute.isNotEmpty()) cfg.put("my", s.myRoute) else cfg.remove("my")
            cfg.put("system_resolvers", JSONArray(net.dns))
            cfg.put("operator", net.operator)
            cfg.put("iroh_binary", java.io.File(applicationInfo.nativeLibraryDir, "libpassage_iroh.so").path)
            cfg.put("private_dir", java.io.File(filesDir, "iroh").path)
            cfg.put("state_file", java.io.File(filesDir, "router-state.json").path)
            Diagnostics.routerEvents(status())
            val out = Native.json(Native.start(cfg.toString().toByteArray(Charsets.UTF_8)))
            out.optString("error").takeIf { it.isNotEmpty() }?.let { throw IllegalStateException(it) }
            runningRoutesConfig = savedConfig
            lastNet = net.key
            lastError = null
            Diagnostics.event("router started")
        } catch (e: Throwable) {
            lastError = "$why failed: ${e.message ?: e}"
            Diagnostics.error("router start failed: $why", e)
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
            val current = Native.json(Native.status(500))
            Diagnostics.routerEvents(current)
            if (current.optBoolean("config_changed") && !acceptSubscriptionUpdate()) return
            current.optBoolean("running")
        } catch (e: Throwable) {
            Diagnostics.error("router status failed", e)
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

    /** Retrieve the private candidate before any restart discards it. */
    private fun acceptSubscriptionUpdate(): Boolean {
        val settings = Settings(this)
        val previous = settings.routesConfig
        if (runningRoutesConfig != previous) return false
        val request = Subscriptions.request(previous)
        val result = Native.json(Native.refreshSubscriptions(request.toString().toByteArray(Charsets.UTF_8)))
        if (result.has("error")) return false
        val config = result.getJSONObject("config")
        if (!settings.saveSubscriptionRefresh(previous, config)) return false
        startTunnel("subscription updated")
        return true
    }

    private fun goForeground() {
        val localized = AppLanguage.localizedContext(this)
        val connecting = HomeSetup.Plain.CONNECTING
        val n = buildNotification(localized.getString(R.string.status_title, localized.getString(connecting.title)), localized.getString(connecting.reason(false)))
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }

    private fun updateNotification(snapshot: JSONObject? = null, force: Boolean = false) {
        if (!active) return
        val localized = AppLanguage.localizedContext(this)
        val content = StatusText.notification(localized, snapshot ?: status(), Settings(this).appSeen, fallbackRunning = lastError == null, display = notificationChannels)
        if (!force && content == lastNotification) return
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIFICATION_ID, buildNotification(content.first, content.second))
        lastNotification = content
    }

    private val notificationChannels = StatusChannels.Display()

    private fun buildNotification(title: String, text: String): Notification {
        val localized = AppLanguage.localizedContext(this)
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            if (nm.getNotificationChannel(CHANNEL) == null) {
                nm.createNotificationChannel(NotificationChannel(CHANNEL, localized.getString(R.string.notification_channel), NotificationManager.IMPORTANCE_LOW))
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
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setPublicVersion((if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) Notification.Builder(this, CHANNEL) else {
                @Suppress("DEPRECATION")
                Notification.Builder(this)
            }).setSmallIcon(R.drawable.ic_notification).setContentTitle(title).setOngoing(true).setContentIntent(open).build())
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(0, localized.getString(R.string.stop), stop).build())
            .build()
    }
}
