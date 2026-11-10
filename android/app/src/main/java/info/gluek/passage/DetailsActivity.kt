package info.gluek.passage

import android.graphics.Typeface
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.MenuItem
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors

class DetailsActivity : ThemedActivity() {
    private val worker = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private lateinit var settings: Settings
    private lateinit var routes: LinearLayout
    private lateinit var connections: TextView
    private lateinit var status: TextView
    private var routeKey = ""
    private val poll = object : Runnable {
        override fun run() { refresh(); main.postDelayed(this, 2_000) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        settings = Settings(this)
        setTitle(R.string.details_title)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        val col = Ui.column(this)
        col.addView(Ui.heading(this, getString(R.string.route_label)))
        routes = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(routes)
        col.addView(Ui.heading(this, getString(R.string.details_connections)))
        connections = TextView(this).apply { textSize = 15f }
        col.addView(connections)
        col.addView(Ui.outlinedButton(this, getString(R.string.details_check)) { checkNow() }.apply {
            layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = Ui.dp(this@DetailsActivity, 16) }
        })
        col.addView(Ui.heading(this, getString(R.string.details_status)))
        status = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            textSize = 13f
            setTextIsSelectable(true)
            setHorizontallyScrolling(false)
            setLineSpacing(Ui.dp(this@DetailsActivity, 2).toFloat(), 1f)
        }
        col.addView(status)
        col.addView(Ui.textButton(this, getString(R.string.details_copy)) {
            Ui.copy(this, getString(R.string.details_status), status.text.toString(), getString(R.string.status_copied))
        })
        setContentView(Ui.scroll(this, col))
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        if (item.itemId == android.R.id.home) { finish(); return true }
        return super.onOptionsItemSelected(item)
    }

    override fun onResume() { super.onResume(); main.post(poll) }
    override fun onPause() { main.removeCallbacks(poll); super.onPause() }
    override fun onDestroy() { main.removeCallbacksAndMessages(null); worker.shutdown(); super.onDestroy() }

    private fun checkNow() {
        if (!PassageService.active) {
            Toast.makeText(this, R.string.proxy_stopped, Toast.LENGTH_SHORT).show()
            return
        }
        Toast.makeText(this, R.string.checking, Toast.LENGTH_SHORT).show()
        worker.execute {
            try { Native.recheck() } catch (_: Throwable) { }
            runOnUiThread { if (!isFinishing && !isDestroyed) refresh() }
        }
    }

    private fun refresh() {
        if (isFinishing || isDestroyed) return
        val key = "${settings.installedRoutes} ${settings.myRoute.isNotEmpty()} ${settings.mode}"
        if (key != routeKey) {
            routeKey = key
            routes.removeAllViews()
            routes.addView(Ui.routeSelector(this, settings, worker::execute, equalWeights = true) { refresh() })
        }
        worker.execute {
            val (line, body) = try {
                val snapshot = Native.status()
                val full = StatusText.full(this, snapshot) + Diagnostics.text(this)
                StatusText.oneLine(this, snapshot) to (PassageService.lastError?.let { "⚠ $it\n\n" }.orEmpty() + full)
            } catch (e: Throwable) {
                "" to (getString(R.string.native_broken, e.message ?: e.toString()) + Diagnostics.text(this))
            }
            runOnUiThread {
                if (!isFinishing && !isDestroyed) { connections.text = line; status.text = body }
            }
        }
    }
}
