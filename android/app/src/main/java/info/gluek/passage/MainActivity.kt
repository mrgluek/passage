package info.gluek.passage

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.widget.Button
import android.widget.ImageButton
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors

/**
 * The home screen: whether the proxy runs, how to point an app at it, and
 * the live status. Start/stop, the report and settings live in the title bar.
 */
class MainActivity : Activity() {
    private val worker = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private lateinit var settings: Settings

    private lateinit var stateView: TextView
    private lateinit var routeView: TextView
    private lateinit var noLinkBox: LinearLayout
    private lateinit var routeBox: LinearLayout
    private lateinit var connectBox: LinearLayout
    private lateinit var detailsBox: LinearLayout
    private var shownRoutes: String? = null
    private var shownDetails: String? = null
    private lateinit var batteryButton: Button
    private lateinit var statusView: TextView
    private lateinit var startStopButton: ImageButton
    private lateinit var updateNotice: Button
    private var shownActive: Boolean? = null
    /** A network test for a bug report; kept across screen rotation. */
    private class NetworkTest {
        @Volatile var result: String? = null
    }
    private var networkTest: NetworkTest? = null
    private var testDialog: AlertDialog? = null
    private var checkingUntil = 0L

    private val poll = object : Runnable {
        override fun run() {
            refresh()
            main.postDelayed(this, 2_000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        networkTest = lastNonConfigurationInstance as? NetworkTest
        Diagnostics.event("main screen created: recreation=${savedInstanceState != null}")
        settings = Settings(this)
        setContentView(buildUi())
        title = "${getString(R.string.app_name)} ${appVersion()}"
        // The proxy was on and is not now: an app update or the system
        // stopped it. Opening the app turns it back on.
        if (settings.running && !PassageService.active) {
            PassageService.start(this, "restore on app open")
        }
    }

    override fun onResume() {
        super.onResume()
        main.post(poll)
    }

    override fun onPause() {
        main.removeCallbacks(poll)
        super.onPause()
    }

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        testDialog?.setOnDismissListener(null)
        testDialog?.dismiss()
        testDialog = null
        Diagnostics.event("main screen destroyed: configuration=$isChangingConfigurations finishing=$isFinishing")
        worker.shutdown()
        super.onDestroy()
    }

    @Suppress("OVERRIDE_DEPRECATION")
    override fun onRetainNonConfigurationInstance(): Any? = networkTest

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.main, menu)
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        when (item.itemId) {
            R.id.action_settings -> startActivity(Intent(this, SettingsActivity::class.java))
            else -> return super.onOptionsItemSelected(item)
        }
        return true
    }

    private fun buildUi(): View {
        val col = Ui.column(this)

        stateView = TextView(this).apply {
            textSize = 18f
            setTypeface(typeface, Typeface.BOLD)
            setOnClickListener { checkNow() }
        }
        startStopButton = Ui.iconButton(this, R.drawable.ic_action_start, getString(R.string.action_start)) { onStartStop() }
        col.addView(LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(stateView, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            addView(startStopButton)
        })
        routeView = TextView(this).apply { textSize = 15f }
        col.addView(routeView)

        updateNotice = Button(this).apply {
            visibility = View.GONE
            setOnClickListener {
                startActivity(Intent(this@MainActivity, SettingsActivity::class.java).setAction(Updater.ACTION_SHOW))
            }
        }
        col.addView(updateNotice)

        noLinkBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        noLinkBox.addView(Ui.hint(this, getString(R.string.no_link)).apply {
            setPadding(0, Ui.dp(this@MainActivity, 8), 0, 0)
        })
        noLinkBox.addView(Button(this).apply {
            text = getString(R.string.open_settings)
            setOnClickListener { startActivity(Intent(this@MainActivity, SettingsActivity::class.java)) }
        })
        col.addView(noLinkBox)

        routeBox = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, Ui.dp(this@MainActivity, 12), 0, 0)
        }
        col.addView(routeBox)

        connectBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        connectBox.addView(Ui.heading(this, getString(R.string.connect_label)))
        connectBox.addView(Ui.hint(this, getString(R.string.connect_help)))
        val proxyRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, Ui.dp(this@MainActivity, 8), 0, 0)
        }
        detailsBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        proxyRow.addView(detailsBox, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        val icons = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(Ui.dp(this@MainActivity, 4), 0, 0, 0)
        }
        icons.addView(Ui.iconButton(this, R.drawable.ic_copy, getString(R.string.copy_proxy_link)) {
            Ui.copy(this, "Passage proxy", settings.socksLink(), getString(R.string.proxy_link_copied))
        })
        icons.addView(Ui.iconButton(this, R.drawable.ic_send, getString(R.string.open_proxy_link)) { openProxyLink() })
        proxyRow.addView(icons)
        connectBox.addView(proxyRow)
        col.addView(connectBox)

        batteryButton = Button(this).apply {
            text = getString(R.string.battery)
            setOnClickListener { Ui.askBatteryExemption(this@MainActivity) }
        }
        col.addView(batteryButton)

        val statusRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, Ui.dp(this@MainActivity, 12), 0, 0)
        }
        statusRow.addView(Ui.heading(this, getString(R.string.status_label)).apply { setPadding(0, 0, 0, 0) },
            LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        statusRow.addView(Ui.iconButton(this, R.drawable.ic_bug, getString(R.string.send_report)) { sendReport() })
        col.addView(statusRow)
        statusView = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            textSize = 11f
            setTextIsSelectable(true)
        }
        col.addView(statusView)

        return Ui.scroll(this, col)
    }

    /** The route choice, rebuilt when the link or the choice changes. */
    private fun showRoutes() {
        val key = "${settings.installedRoutes} ${settings.myRoute.isNotEmpty()} ${settings.mode}"
        if (key == shownRoutes) return
        shownRoutes = key
        routeBox.removeAllViews()
        routeBox.addView(Ui.routeSelector(this, settings, worker::execute) { refresh() })
    }

    /** Host, port and credentials, each copied on tap. */
    private fun showDetails() {
        val rows = mutableListOf(getString(R.string.proxy_host) to Settings.HOST, getString(R.string.proxy_port) to settings.port.toString())
        if (settings.auth) {
            rows += getString(R.string.proxy_user) to settings.user
            rows += getString(R.string.proxy_pass) to settings.pass
        }
        val key = rows.toString()
        if (key == shownDetails) return
        shownDetails = key
        detailsBox.removeAllViews()
        detailsBox.addView(Ui.valueBlock(this, rows, if (settings.auth) null else getString(R.string.proxy_no_password)))
    }

    /** Hands the proxy to an app that handles tg://socks links; it asks before adding it. */
    private fun openProxyLink() {
        try {
            startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(settings.tgLink())))
        } catch (_: ActivityNotFoundException) {
            Toast.makeText(this, getString(R.string.no_app_for_link), Toast.LENGTH_LONG).show()
        }
    }

    private fun onStartStop() {
        if (PassageService.active) {
            PassageService.stop(this)
            main.postDelayed({ refresh() }, 300)
        } else {
            onStartClicked()
        }
    }

    private fun onStartClicked() {
        PassageService.start(this)
        main.postDelayed({ refresh() }, 500)
        askNotifications()
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) PassageService.refreshNotification(this)
    }

    /**
     * Android 13+ asks before an app may show notifications. The proxy runs
     * without it, but its notification (route, Stop button) stays hidden, so
     * say why first; once, since Android stops asking after two refusals.
     */
    private fun askNotifications() {
        if (Build.VERSION.SDK_INT < 33 ||
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED ||
            settings.askedNotifications
        ) {
            return
        }
        settings.askedNotifications = true
        AlertDialog.Builder(this)
            .setTitle(R.string.notifications_title)
            .setMessage(R.string.notifications_why)
            .setPositiveButton(R.string.notifications_continue) { _, _ ->
                requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
            }
            .setNegativeButton(R.string.notifications_not_now, null)
            .show()
    }

    private fun refresh() {
        if (isFinishing || isDestroyed) return
        showNetworkTest()
        val newer = Updater.pending(this)
        updateNotice.visibility = if (newer == null) View.GONE else View.VISIBLE
        if (newer != null) updateNotice.text = getString(R.string.update_available, newer)
        val active = PassageService.active
        if (shownActive != active) {
            shownActive = active
            startStopButton.setImageResource(if (active) R.drawable.ic_action_stop else R.drawable.ic_action_start)
            val label = getString(if (active) R.string.action_stop else R.string.action_start)
            startStopButton.contentDescription = label
            if (Build.VERSION.SDK_INT >= 26) startStopButton.tooltipText = label
        }
        val hasLink = settings.link.isNotEmpty()
        noLinkBox.visibility = if (hasLink) View.GONE else View.VISIBLE
        showRoutes()
        showDetails()
        batteryButton.visibility = if (Ui.batteryExempt(this)) View.GONE else View.VISIBLE
        worker.execute {
            val head = getString(when {
                active && System.currentTimeMillis() < checkingUntil -> R.string.checking
                active -> R.string.proxy_running
                else -> R.string.proxy_stopped
            })
            val (line, body) = try {
                val s = Native.status()
                val err = PassageService.lastError
                val line = if (active && s.optBoolean("running")) StatusText.oneLine(this, s) else ""
                val full = StatusText.full(this, s) + Diagnostics.text(this)
                Pair(line, if (err != null) "⚠ $err\n\n$full" else full)
            } catch (e: Throwable) {
                Pair("", getString(R.string.native_broken, e.message ?: e.toString()) + Diagnostics.text(this))
            }
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                stateView.text = head
                routeView.text = line
                routeView.visibility = if (line.isEmpty()) View.GONE else View.VISIBLE
                statusView.text = body
            }
        }
    }

    /** The report goes through the share sheet, so a tester can send it straight from here. */
    private fun shareReport(networkTest: String? = null) {
        worker.execute {
            val status = try { Native.status() } catch (e: Throwable) {
                org.json.JSONObject().put("running", false).put("error", e.toString())
            }
            val report = try {
                StatusText.report(this, status, NetInfo.snapshot(this), settings, appVersion(), networkTest)
            } catch (e: Throwable) {
                Diagnostics.error("report failed", e)
                org.json.JSONObject().put("error", e.javaClass.name)
                    .put("device", Diagnostics.device()).put("diagnostics", Diagnostics.snapshot()).toString(2)
            }
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                val send = Intent(Intent.ACTION_SEND)
                    .setType("text/plain")
                    .putExtra(Intent.EXTRA_SUBJECT, "Passage report")
                    .putExtra(Intent.EXTRA_TEXT, report)
                startActivity(Intent.createChooser(send, getString(R.string.share_report)))
            }
        }
    }

    private fun appVersion(): String = try {
        packageManager.getPackageInfo(packageName, 0).versionName ?: "?"
    } catch (_: Exception) {
        "?"
    }

    /**
     * The network test: every relay, canary and two Telegram data centers,
     * each checked directly, with each strategy and through the tunnel. The
     * result can be shared, e.g. with whoever gave the link.
     */
    /**
     * The bug button: with the proxy on, test the network first (the result
     * goes into the log and the report), then share the whole report.
     */
    private fun sendReport() {
        if (!PassageService.active) {
            shareReport()
            return
        }
        if (networkTest != null) return
        val test = NetworkTest()
        networkTest = test
        val context = applicationContext
        showNetworkTest()
        worker.execute {
            test.result = try {
                val d = Native.json(Native.diagnose())
                Diagnostics.event("network test: ${StatusText.diagnosisSummary(d)}")
                StatusText.diagnosis(context, d)
            } catch (e: Throwable) {
                Diagnostics.error("network test failed", e)
                e.message ?: e.toString()
            }
        }
    }

    /** While a test runs, a progress dialog; when it ends, the report. */
    private fun showNetworkTest() {
        val test = networkTest ?: return
        val text = test.result
        if (text == null) {
            if (testDialog == null) {
                testDialog = AlertDialog.Builder(this)
                    .setTitle(R.string.test_network)
                    .setMessage(R.string.testing_network)
                    .setCancelable(false)
                    .show()
            }
            return
        }
        testDialog?.dismiss()
        testDialog = null
        networkTest = null
        shareReport(text)
    }

    private fun checkNow() {
        if (!PassageService.active) {
            Toast.makeText(this, getString(R.string.proxy_stopped), Toast.LENGTH_SHORT).show()
            return
        }
        worker.execute {
            try {
                Native.recheck()
            } catch (_: Throwable) {
            }
        }
        checkingUntil = System.currentTimeMillis() + 3_000
        refresh()
        main.postDelayed({ refresh() }, 3_100)
    }
}
