package info.gluek.passage

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.text.TextUtils
import android.view.Gravity
import android.view.View
import android.widget.ImageButton
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

    /** The theme's accent color. */
    fun accent(c: Context): Int {
        val a = c.obtainStyledAttributes(intArrayOf(android.R.attr.colorAccent))
        try { return a.getColor(0, Color.rgb(0x1E, 0x88, 0xE5)) } finally { a.recycle() }
    }

    /** A route type for people: wss -> WSS. */
    fun routeLabel(c: Context, kind: String): String = when (kind) {
        "my" -> c.getString(R.string.route_my)
        "wss" -> "WSS"
        "iroh" -> "Iroh"
        "soar" -> "Soar"
        else -> kind
    }

    private fun rounded(radius: Float, fill: Int, stroke: Int = 0, strokeWidth: Int = 0) = GradientDrawable().apply {
        cornerRadius = radius
        setColor(fill)
        if (strokeWidth > 0) setStroke(strokeWidth, stroke)
    }

    /** One row of equal segments with [selected] highlighted; [choose] gets the tapped index. */
    fun segments(c: Context, labels: List<String>, selected: Int, choose: (Int) -> Unit): LinearLayout {
        val accent = accent(c)
        val radius = dp(c, 18).toFloat()
        return LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            background = rounded(radius, Color.TRANSPARENT, accent, dp(c, 1))
            val pad = dp(c, 2)
            setPadding(pad, pad, pad, pad)
            labels.forEachIndexed { i, label ->
                addView(TextView(c).apply {
                    text = label
                    gravity = Gravity.CENTER
                    textSize = 14f
                    maxLines = 1
                    setPadding(dp(c, 4), dp(c, 8), dp(c, 4), dp(c, 8))
                    if (i == selected) {
                        setTextColor(Color.WHITE)
                        setTypeface(typeface, Typeface.BOLD)
                        background = rounded(radius, accent)
                    } else {
                        setTextColor(accent)
                        setOnClickListener { choose(i) }
                    }
                    isSelected = i == selected
                    contentDescription = label
                }, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            }
        }
    }

    /**
     * The route choice: Auto, each route type in the link, Direct. A change
     * is saved and, while the proxy runs, applied through [run].
     */
    fun routeSelector(c: Context, settings: Settings, run: (Runnable) -> Unit, changed: () -> Unit): View {
        val choices = listOf("auto" to c.getString(R.string.mode_auto)) +
            settings.routeKinds.map { it to routeLabel(c, it) } +
            ("off" to c.getString(R.string.mode_off))
        val selected = choices.indexOfFirst { it.first == settings.mode }.coerceAtLeast(0)
        return segments(c, choices.map { it.second }, selected) { i ->
            val mode = choices[i].first
            settings.mode = mode
            if (PassageService.active) run(Runnable {
                try {
                    Native.setMode(mode.toByteArray(Charsets.UTF_8))
                } catch (_: Throwable) {
                }
            })
            changed()
        }
    }

    /**
     * Label/value rows in a shaded monospace block, like a code block in a
     * chat; tapping a row copies its value.
     */
    fun valueBlock(c: Context, rows: List<Pair<String, String>>, note: String? = null): LinearLayout =
        LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            background = rounded(dp(c, 8).toFloat(), Color.argb(0x14, 0, 0, 0))
            val pad = dp(c, 10)
            setPadding(pad, dp(c, 6), pad, dp(c, 6))
            for ((label, value) in rows) {
                addView(LinearLayout(c).apply {
                    orientation = LinearLayout.HORIZONTAL
                    gravity = Gravity.CENTER_VERTICAL
                    setPadding(0, dp(c, 5), 0, dp(c, 5))
                    addView(TextView(c).apply {
                        text = label
                        textSize = 12f
                        alpha = 0.6f
                        maxLines = 1
                    }, LinearLayout.LayoutParams(dp(c, 72), LinearLayout.LayoutParams.WRAP_CONTENT))
                    // Long values are shortened in the middle; a tap copies them whole.
                    addView(TextView(c).apply {
                        text = value
                        typeface = Typeface.MONOSPACE
                        textSize = 13f
                        maxLines = 1
                        ellipsize = TextUtils.TruncateAt.MIDDLE
                    }, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
                    setOnClickListener { copy(c, label, value, c.getString(R.string.value_copied, label)) }
                })
            }
            if (note != null) addView(hint(c, note).apply { setPadding(0, dp(c, 4), 0, dp(c, 4)) })
        }

    /** A borderless icon button tinted with the accent color. */
    fun iconButton(c: Context, icon: Int, description: String, onClick: () -> Unit): ImageButton =
        ImageButton(c).apply {
            setImageResource(icon)
            imageTintList = ColorStateList.valueOf(accent(c))
            background = rounded(dp(c, 20).toFloat(), Color.TRANSPARENT)
            contentDescription = description
            if (Build.VERSION.SDK_INT >= 26) tooltipText = description
            val pad = dp(c, 10)
            setPadding(pad, pad, pad, pad)
            setOnClickListener { onClick() }
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
