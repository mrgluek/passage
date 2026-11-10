package info.gluek.passage

import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipDescription
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
import android.os.PersistableBundle
import android.text.TextUtils
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.ImageButton
import android.widget.PopupMenu
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

    fun card(c: Context): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.VERTICAL
        background = rounded(dp(c, 16).toFloat(), c.getColor(R.color.card), c.getColor(R.color.card_border), dp(c, 1))
        val pad = dp(c, 18)
        setPadding(pad, pad, pad, pad)
        layoutParams = LinearLayout.LayoutParams(-1, -2).apply { bottomMargin = dp(c, 12) }
    }

    fun dot(c: Context, color: Int): View = View(c).apply {
        background = rounded(dp(c, 7).toFloat(), color)
        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        layoutParams = LinearLayout.LayoutParams(dp(c, 14), dp(c, 14)).apply { marginEnd = dp(c, 10) }
    }

    private fun styleButton(button: Button, primary: Boolean, outlined: Boolean) = with(button) {
        val c = context
        val ink = accent(c)
        isAllCaps = false
        textSize = 14f
        typeface = Typeface.SANS_SERIF
        minHeight = dp(c, 48)
        setTextColor(if (primary) c.getColor(R.color.on_accent) else ink)
        background = when {
            primary -> rounded(dp(c, 24).toFloat(), ink)
            outlined -> rounded(dp(c, 24).toFloat(), Color.TRANSPARENT, ink, dp(c, 1))
            else -> rounded(dp(c, 24).toFloat(), Color.TRANSPARENT)
        }
    }

    fun textButton(c: Context, text: String, click: () -> Unit): Button = Button(c).apply {
        this.text = text
        styleButton(this, primary = false, outlined = false)
        setOnClickListener { click() }
    }

    fun primaryButton(c: Context, text: String, click: () -> Unit): Button = Button(c).apply {
        this.text = text
        styleButton(this, primary = true, outlined = false)
        setOnClickListener { click() }
    }

    fun outlinedButton(c: Context, text: String, click: () -> Unit): Button = Button(c).apply {
        this.text = text
        styleButton(this, primary = false, outlined = true)
        setOnClickListener { click() }
    }

    fun styleDialogButtons(dialog: AlertDialog, primary: Boolean) {
        dialog.getButton(AlertDialog.BUTTON_POSITIVE)?.let { styleButton(it, primary, outlined = !primary) }
        dialog.getButton(AlertDialog.BUTTON_NEGATIVE)?.let { styleButton(it, primary = false, outlined = false) }
        dialog.getButton(AlertDialog.BUTTON_NEUTRAL)?.let { styleButton(it, primary = false, outlined = false) }
    }

    fun chip(c: Context, text: String, click: () -> Unit): TextView = textButton(c, text, click).apply {
        background = rounded(dp(c, 24).toFloat(), c.getColor(R.color.value_background))
    }

    fun cardHeading(c: Context, text: String): TextView = TextView(c).apply {
        this.text = text
        textSize = 18f
        setTypeface(Typeface.SANS_SERIF, Typeface.BOLD)
    }

    fun body(c: Context, text: String): TextView = TextView(c).apply {
        this.text = text
        textSize = 16f
        typeface = Typeface.SANS_SERIF
    }

    fun heading(c: Context, text: String): TextView = cardHeading(c, text).apply {
        setPadding(0, dp(c, 20), 0, dp(c, 8))
    }

    fun hint(c: Context, text: String): TextView = body(c, text).apply {
        textSize = 14f
        setTextColor(c.getColor(R.color.muted))
    }

    fun downloadLine(c: Context, label: String, url: String): TextView = hint(c, "").apply {
        text = android.text.SpannableString("$label $url").apply {
            setSpan(android.text.style.URLSpan(url), label.length + 1, length, android.text.Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        }
        setLinkTextColor(accent(c))
        movementMethod = android.text.method.LinkMovementMethod.getInstance()
    }

    fun setupRow(c: Context, text: String, number: Int, done: Boolean, current: Boolean,
        expanded: Boolean, click: (() -> Unit)? = null): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        minimumHeight = dp(c, 44)
        val weight = if (current) Typeface.BOLD else Typeface.NORMAL
        if (done) {
            addView(android.widget.ImageView(c).apply {
                setImageResource(R.drawable.ic_check)
                imageTintList = ColorStateList.valueOf(c.getColor(R.color.ok))
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                scaleType = android.widget.ImageView.ScaleType.CENTER_INSIDE
            }, LinearLayout.LayoutParams(dp(c, 28), dp(c, 24)))
        } else {
            addView(body(c, "$number.").apply { setTypeface(Typeface.SANS_SERIF, weight) }, LinearLayout.LayoutParams(dp(c, 28), -2))
        }
        addView(body(c, text).apply {
            setTypeface(Typeface.SANS_SERIF, weight)
            if (!done && !current) setTextColor(c.getColor(R.color.muted))
        }, LinearLayout.LayoutParams(0, -2, 1f))
        if (click != null) {
            addView(body(c, if (expanded) "▴" else "›"), LinearLayout.LayoutParams(dp(c, 24), -2))
            isFocusable = true
            setOnClickListener { click() }
        } else addView(View(c), LinearLayout.LayoutParams(dp(c, 24), 1))
    }

    fun setupRows(c: Context, rows: List<View>): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(0, dp(c, 10), 0, dp(c, 12))
        rows.forEachIndexed { index, row ->
            addView(row, LinearLayout.LayoutParams(-1, -2).apply { if (index < rows.lastIndex) bottomMargin = dp(c, 12) })
        }
    }

    /** Shows an input's placeholder dimmer than typed text, so it never reads as a value. */
    fun dimHint(edit: android.widget.EditText) {
        edit.setHintTextColor(edit.context.getColor(R.color.muted))
    }

    /**
     * A scroll view that keeps its content off the system bars. From Android
     * 15 the window is edge to edge and the title bar overlays the content;
     * the top inset then covers status and title bar. Before that the
     * content already starts below the title bar.
     */
    fun scroll(activity: Activity, content: View, bottomSpacing: Int = 0): ScrollView = ScrollView(activity).apply {
        addView(content)
        val contentBottom = content.paddingBottom
        if (bottomSpacing > 0) content.setPadding(content.paddingLeft, content.paddingTop, content.paddingRight,
            maxOf(contentBottom, dp(activity, bottomSpacing)))
        setOnApplyWindowInsetsListener { v, insets ->
            val edgeToEdge = Build.VERSION.SDK_INT >= 35
            @Suppress("DEPRECATION")
            v.setPadding(
                insets.systemWindowInsetLeft,
                if (activity.actionBar == null || edgeToEdge) insets.systemWindowInsetTop else 0,
                insets.systemWindowInsetRight,
                if (bottomSpacing > 0) 0 else insets.systemWindowInsetBottom,
            )
            if (bottomSpacing > 0) content.setPadding(content.paddingLeft, content.paddingTop, content.paddingRight,
                maxOf(contentBottom, insets.systemWindowInsetBottom + dp(activity, bottomSpacing)))
            insets
        }
    }

    /** The theme's accent color. */
    fun accent(c: Context): Int = c.getColor(R.color.accent)

    /** A route type for people: wss -> WSS. */
    fun routeLabel(c: Context, kind: String): String = when (kind) {
        "my" -> c.getString(R.string.route_my)
        "http" -> "HTTP"
        "wss" -> "WSS"
        "turn" -> "TURN"
        "iroh" -> "Iroh"
        "soar" -> "Soar"
        else -> kind
    }

    fun rounded(radius: Float, fill: Int, stroke: Int = 0, strokeWidth: Int = 0) = GradientDrawable().apply {
        cornerRadius = radius
        setColor(fill)
        if (strokeWidth > 0) setStroke(strokeWidth, stroke)
    }

    /** Route buttons; [stretch] fills the remaining width when supplied. */
    fun segments(c: Context, labels: List<String>, selected: Int, stretch: Int = -1, wrapLabels: Boolean = false, choose: (Int) -> Unit): LinearLayout {
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
                    typeface = Typeface.SANS_SERIF
                    if (!wrapLabels) {
                        maxLines = 1
                        ellipsize = TextUtils.TruncateAt.END
                    }
                    minHeight = dp(c, 48)
                    minWidth = dp(c, 48)
                    setPadding(dp(c, 4), dp(c, 8), dp(c, 4), dp(c, 8))
                    if (i == selected) {
                        setTextColor(c.getColor(R.color.on_accent))
                        setTypeface(typeface, Typeface.BOLD)
                        background = rounded(radius, accent)
                    } else {
                        setTextColor(accent)
                    }
                    setOnClickListener { choose(i) }
                    isSelected = i == selected
                    contentDescription = label
                }, if (stretch < 0 || i == stretch)
                    LinearLayout.LayoutParams(0, if (wrapLabels) LinearLayout.LayoutParams.MATCH_PARENT else LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
                else LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT))
            }
        }
    }

    fun appTabs(c: Context, labels: List<String>, selected: Int, choose: (Int) -> Unit): View {
        val measure = TextView(c).apply { textSize = 11f; setTypeface(Typeface.SANS_SERIF, Typeface.BOLD) }
        val minimumWidth = labels.maxOf { measure.paint.measureText(it) }.toInt() + dp(c, 8)
        val available = dp(c, c.resources.configuration.screenWidthDp - 68) - dp(c, 4)
        val columns = HomeSetup.appTabColumns(c.resources.configuration.fontScale, available, minimumWidth)
        return LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            background = rounded(dp(c, 18).toFloat(), Color.TRANSPARENT, accent(c), dp(c, 1))
            val pad = dp(c, 2)
            setPadding(pad, pad, pad, pad)
            labels.indices.toList().chunked(columns).forEach { indices ->
                addView(LinearLayout(c).apply {
                    orientation = LinearLayout.HORIZONTAL
                    indices.forEach { index ->
                        addView(TextView(c).apply {
                            text = labels[index]
                            textSize = 13f
                            setTypeface(Typeface.SANS_SERIF, if (index == selected) Typeface.BOLD else Typeface.NORMAL)
                            maxLines = 1
                            ellipsize = null
                            setHorizontallyScrolling(false)
                            gravity = Gravity.CENTER
                            minHeight = dp(c, 44)
                            setPadding(dp(c, 4), dp(c, 6), dp(c, 4), dp(c, 6))
                            if (Build.VERSION.SDK_INT >= 26) setAutoSizeTextTypeUniformWithConfiguration(11, 13, 1, android.util.TypedValue.COMPLEX_UNIT_SP)
                            else {
                                val sample = TextView(c).apply { textSize = 13f; setTypeface(Typeface.SANS_SERIF, if (index == selected) Typeface.BOLD else Typeface.NORMAL) }
                                var size = 13f
                                while (size > 11f && sample.paint.measureText(labels[index]) > available / columns - dp(c, 8)) {
                                    size -= 1f
                                    sample.textSize = size
                                }
                                textSize = size
                            }
                            if (index == selected) {
                                setTextColor(c.getColor(R.color.on_accent))
                                background = rounded(dp(c, 18).toFloat(), accent(c))
                            } else setTextColor(accent(c))
                            isSelected = index == selected
                            contentDescription = labels[index]
                            setOnClickListener { choose(index) }
                        }, LinearLayout.LayoutParams(0, -2, 1f))
                    }
                }, LinearLayout.LayoutParams(-1, -2))
            }
        }
    }

    /** Auto, optional My, installed routes, Direct. */
    fun routeSelector(c: Context, settings: Settings, run: (Runnable) -> Unit, equalWeights: Boolean = true, changed: () -> Unit): View {
        val routes = settings.installedRoutes
        val mode = settings.mode
        val fixed = routes.firstOrNull { it.mode == mode }
        val choices = mutableListOf("auto" to c.getString(R.string.mode_auto))
        if (settings.myRoute.isNotEmpty()) choices += "my" to c.getString(R.string.route_my)
        val menuIndex = if (routes.isEmpty()) -1 else choices.size
        if (routes.isNotEmpty()) choices += "routes" to (fixed?.id ?: c.getString(R.string.route_menu))
        choices += "off" to c.getString(R.string.mode_off)
        val selected = if (fixed != null) menuIndex else choices.indexOfFirst { it.first == mode }.coerceAtLeast(0)
        fun choose(value: String) {
            settings.mode = value
            if (PassageService.active) run(Runnable {
                try {
                    Native.setMode(value.toByteArray(Charsets.UTF_8))
                } catch (_: Throwable) {
                }
            })
            changed()
        }
        lateinit var bar: LinearLayout
        bar = segments(c, choices.map { it.second }, selected, if (equalWeights) -1 else menuIndex) { i ->
            if (i == menuIndex) {
                PopupMenu(c, bar.getChildAt(i)).apply {
                    routes.forEachIndexed { index, route ->
                        val label = if (route.id == route.type && route.subscription.isEmpty()) "${route.priority} ${route.id}"
                            else "${route.priority} ${route.id} (${route.type}${route.subscription.takeIf { it.isNotEmpty() }?.let { " · $it" }.orEmpty()})"
                        menu.add(0, index, index, label).apply {
                            isCheckable = true
                            isChecked = route.mode == mode
                        }
                    }
                    menu.setGroupCheckable(0, true, true)
                    setOnMenuItemClickListener { item -> choose(routes[item.itemId].mode); true }
                    show()
                }
            } else choose(choices[i].first)
        }
        if (menuIndex >= 0) (bar.getChildAt(menuIndex) as TextView).apply {
            setCompoundDrawablesRelativeWithIntrinsicBounds(0, 0, R.drawable.ic_dropdown, 0)
            compoundDrawablePadding = dp(c, 2)
            compoundDrawableTintList = ColorStateList.valueOf(if (fixed == null) accent(c) else c.getColor(R.color.on_accent))
            contentDescription = if (fixed == null) c.getString(R.string.route_menu)
                else "${fixed.priority} ${fixed.id} (${fixed.type}${fixed.subscription.takeIf { it.isNotEmpty() }?.let { " · $it" }.orEmpty()})"
        }
        return bar
    }

    /**
     * Label/value rows in a shaded monospace block, like a code block in a
     * chat; tapping a row copies its value.
     */
    fun valueBlock(c: Context, rows: List<Pair<String, String>>, note: String? = null): LinearLayout =
        LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            background = rounded(dp(c, 8).toFloat(), c.getColor(R.color.value_background))
            val pad = dp(c, 10)
            setPadding(pad, dp(c, 6), pad, dp(c, 6))
            for ((label, value) in rows) {
                addView(LinearLayout(c).apply {
                    orientation = LinearLayout.HORIZONTAL
                    gravity = Gravity.CENTER_VERTICAL
                    setPadding(0, dp(c, 5), 0, dp(c, 5))
                    addView(TextView(c).apply {
                        text = label
                        textSize = 14f
                        typeface = Typeface.SANS_SERIF
                        setTextColor(c.getColor(R.color.muted))
                    }, LinearLayout.LayoutParams(dp(c, 72), LinearLayout.LayoutParams.WRAP_CONTENT))
                    // Long values are shortened in the middle; a tap copies them whole.
                    addView(TextView(c).apply {
                        text = value
                        typeface = Typeface.MONOSPACE
                        textSize = 16f
                        maxLines = 1
                        ellipsize = TextUtils.TruncateAt.MIDDLE
                    }, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
                    setOnClickListener { copy(c, label, value, c.getString(R.string.value_copied, label)) }
                })
            }
            if (note != null) addView(hint(c, note).apply { setPadding(0, dp(c, 4), 0, dp(c, 4)) })
        }

    fun passwordRow(c: Context, password: String, visible: Boolean, toggle: () -> Unit): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        setPadding(0, dp(c, 5), 0, dp(c, 5))
        setOnClickListener { copy(c, c.getString(R.string.proxy_pass), password,
            c.getString(R.string.value_copied, c.getString(R.string.proxy_pass)), sensitive = true) }
        addView(hint(c, c.getString(R.string.proxy_pass)),
            LinearLayout.LayoutParams(dp(c, 72), -2))
        addView(TextView(c).apply {
            text = HomeSetup.password(password, visible)
            typeface = Typeface.MONOSPACE
            textSize = 16f
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.MIDDLE
            minHeight = dp(c, 48)
            gravity = Gravity.CENTER_VERTICAL
        }, LinearLayout.LayoutParams(0, -2, 1f))
        addView(textButton(c, c.getString(if (visible) R.string.home_hide else R.string.home_show), toggle).apply {
            minWidth = 0
            minimumWidth = 0
        }, LinearLayout.LayoutParams(-2, -2))
    }

    /** A rounded notice with a close button on the right; [text] is updated by the caller, [close] is called by the button. */
    fun bubble(c: Context, text: TextView, close: () -> Unit): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        background = rounded(dp(c, 14).toFloat(), c.getColor(R.color.notice_background))
        setPadding(dp(c, 14), dp(c, 4), dp(c, 4), dp(c, 4))
        addView(text, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        addView(iconButton(c, android.R.drawable.ic_menu_close_clear_cancel, c.getString(R.string.close)) { close() })
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

    fun copy(c: Context, label: String, text: String, toast: String, sensitive: Boolean = false) {
        val cm = c.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        val clip = ClipData.newPlainText(label, text)
        if (sensitive && Build.VERSION.SDK_INT >= 33) {
            clip.description.extras = PersistableBundle().apply {
                putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true)
            }
        }
        cm.setPrimaryClip(clip)
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


fun AlertDialog.showStyled(primary: Boolean = false): AlertDialog = apply {
    show()
    Ui.styleDialogButtons(this, primary)
}

fun AlertDialog.Builder.showStyled(primary: Boolean = false): AlertDialog = create().showStyled(primary)
