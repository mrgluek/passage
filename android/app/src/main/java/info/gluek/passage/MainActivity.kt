package info.gluek.passage

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.widget.Button
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
    private lateinit var connectBox: LinearLayout
    private lateinit var detailsView: TextView
    private lateinit var batteryButton: Button
    private lateinit var statusView: TextView
    private var shownActive: Boolean? = null

    private val poll = object : Runnable {
        override fun run() {
            refresh()
            main.postDelayed(this, 2_000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        settings = Settings(this)
        setContentView(buildUi())
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
        worker.shutdown()
        super.onDestroy()
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.main, menu)
        return true
    }

    override fun onPrepareOptionsMenu(menu: Menu): Boolean {
        val active = PassageService.active
        menu.findItem(R.id.action_start).isVisible = !active
        menu.findItem(R.id.action_stop).isVisible = active
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        when (item.itemId) {
            R.id.action_start -> onStartClicked()
            R.id.action_stop -> {
                PassageService.stop(this)
                main.postDelayed({ refresh() }, 300)
            }
            R.id.action_report -> shareReport()
            R.id.action_check -> checkNow()
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
        }
        col.addView(stateView)
        routeView = TextView(this).apply { textSize = 15f }
        col.addView(routeView)

        noLinkBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        noLinkBox.addView(Ui.hint(this, getString(R.string.no_link)).apply {
            setPadding(0, Ui.dp(this@MainActivity, 8), 0, 0)
        })
        noLinkBox.addView(Button(this).apply {
            text = getString(R.string.open_settings)
            setOnClickListener { startActivity(Intent(this@MainActivity, SettingsActivity::class.java)) }
        })
        col.addView(noLinkBox)

        connectBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        connectBox.addView(Ui.heading(this, getString(R.string.connect_label)))
        connectBox.addView(Ui.hint(this, getString(R.string.connect_help)))
        connectBox.addView(Button(this).apply {
            text = getString(R.string.copy_proxy_link)
            setOnClickListener {
                Ui.copy(this@MainActivity, "Passage proxy", settings.proxyLink(), getString(R.string.proxy_link_copied))
            }
        })
        detailsView = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            textSize = 12f
            setTextIsSelectable(true)
        }
        connectBox.addView(detailsView)
        col.addView(connectBox)

        batteryButton = Button(this).apply {
            text = getString(R.string.battery)
            setOnClickListener { Ui.askBatteryExemption(this@MainActivity) }
        }
        col.addView(batteryButton)

        col.addView(Ui.heading(this, getString(R.string.status_label)))
        statusView = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            textSize = 11f
            setTextIsSelectable(true)
        }
        col.addView(statusView)

        return Ui.scroll(this, col)
    }

    private fun onStartClicked() {
        if (settings.link.isEmpty()) {
            Toast.makeText(this, getString(R.string.need_link_first), Toast.LENGTH_SHORT).show()
            startActivity(Intent(this, SettingsActivity::class.java))
            return
        }
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        PassageService.start(this)
        main.postDelayed({ refresh() }, 500)
    }

    private fun refresh() {
        val active = PassageService.active
        if (shownActive != active) {
            shownActive = active
            invalidateOptionsMenu()
        }
        val hasLink = settings.link.isNotEmpty()
        noLinkBox.visibility = if (hasLink) View.GONE else View.VISIBLE
        connectBox.visibility = if (hasLink) View.VISIBLE else View.GONE
        detailsView.text = if (settings.auth) {
            getString(R.string.proxy_details, Settings.HOST, settings.port, settings.user, settings.pass)
        } else {
            getString(R.string.proxy_details_open, Settings.HOST, settings.port)
        }
        batteryButton.visibility = if (Ui.batteryExempt(this)) View.GONE else View.VISIBLE
        worker.execute {
            val head = getString(if (active) R.string.proxy_running else R.string.proxy_stopped)
            val (line, body) = try {
                val s = Native.status()
                val err = PassageService.lastError
                val line = if (active && s.optBoolean("running")) StatusText.oneLine(this, s) else ""
                val full = StatusText.full(this, s)
                Pair(line, if (err != null) "⚠ $err\n\n$full" else full)
            } catch (e: Throwable) {
                Pair("", getString(R.string.native_broken, e.message ?: e.toString()))
            }
            runOnUiThread {
                stateView.text = head
                routeView.text = line
                routeView.visibility = if (line.isEmpty()) View.GONE else View.VISIBLE
                statusView.text = body
            }
        }
    }

    /** The report goes through the share sheet, so a tester can send it straight from here. */
    private fun shareReport() {
        worker.execute {
            val report = try {
                StatusText.report(Native.status(), NetInfo.snapshot(this), settings, appVersion())
            } catch (e: Throwable) {
                "error: ${e.message ?: e}"
            }
            runOnUiThread {
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

    /** Probes the network now instead of at the next scheduled check (auto mode only). */
    private fun checkNow() {
        if (!PassageService.active) {
            Toast.makeText(this, getString(R.string.proxy_stopped), Toast.LENGTH_SHORT).show()
            return
        }
        worker.execute {
            try {
                Native.networkChanged()
            } catch (_: Throwable) {
            }
        }
        Toast.makeText(this, getString(R.string.checking), Toast.LENGTH_SHORT).show()
        main.postDelayed({ refresh() }, 3_000)
    }
}
