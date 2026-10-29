package info.gluek.passage

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.view.View
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast

/** Plain-View building blocks shared by both screens. */
object Ui {
    fun dp(c: Context, v: Int): Int = (v * c.resources.displayMetrics.density).toInt()

    fun column(c: Context): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.VERTICAL
        val pad = dp(c, 16)
        setPadding(pad, pad, pad, pad)
    }

    fun heading(c: Context, text: String): TextView = TextView(c).apply {
        this.text = text
        setPadding(0, dp(c, 20), 0, dp(c, 4))
        setTypeface(typeface, Typeface.BOLD)
    }

    fun hint(c: Context, text: String): TextView = TextView(c).apply {
        this.text = text
        textSize = 13f
    }

    /**
     * A scroll view that keeps its content off the system bars. From Android
     * 15 the window is edge to edge and the title bar overlays the content;
     * the top inset then covers status and title bar. Before that the
     * content already starts below the title bar.
     */
    fun scroll(activity: Activity, content: View): ScrollView = ScrollView(activity).apply {
        addView(content)
        setOnApplyWindowInsetsListener { v, insets ->
            val edgeToEdge = Build.VERSION.SDK_INT >= 35
            @Suppress("DEPRECATION")
            v.setPadding(
                insets.systemWindowInsetLeft,
                if (activity.actionBar == null || edgeToEdge) insets.systemWindowInsetTop else 0,
                insets.systemWindowInsetRight,
                insets.systemWindowInsetBottom,
            )
            insets
        }
    }

    fun copy(c: Context, label: String, text: String, toast: String) {
        val cm = c.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText(label, text))
        Toast.makeText(c, toast, Toast.LENGTH_SHORT).show()
    }

    fun batteryExempt(c: Context): Boolean =
        (c.getSystemService(Context.POWER_SERVICE) as PowerManager).isIgnoringBatteryOptimizations(c.packageName)

    fun askBatteryExemption(activity: Activity) {
        try {
            activity.startActivity(
                Intent(android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
                    .setData(Uri.parse("package:${activity.packageName}")),
            )
        } catch (_: Exception) {
            activity.startActivity(Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
        }
    }
}
