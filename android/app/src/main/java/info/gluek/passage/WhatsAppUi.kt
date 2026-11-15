package info.gluek.passage

import android.content.Context
import android.widget.LinearLayout
import android.widget.Switch
import android.widget.Toast

object WhatsAppUi {
    internal fun availablePorts(read: () -> Pair<Int, Int>): Pair<Int, Int>? =
        try { read() } catch (_: IllegalStateException) { null }

    fun controls(context: Context, settings: Settings, home: Boolean, changed: () -> Unit): LinearLayout =
        LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            val ports = availablePorts { settings.whatsappChatPort to settings.whatsappMediaPort }
            if (ports == null) {
                addView(Switch(context).apply {
                    text = context.getString(R.string.whatsapp_switch)
                    isChecked = false
                    isEnabled = false
                })
                addView(Ui.hint(context, context.getString(R.string.whatsapp_ports_busy)))
                return@apply
            }
            addView(Ui.hint(context, context.getString(R.string.whatsapp_ports_help)))
            val form = LinearLayout(context).apply { orientation = LinearLayout.VERTICAL }
            fun render() {
                form.removeAllViews()
                if (home && !settings.whatsappEnabled) {
                    form.addView(Ui.hint(context, context.getString(R.string.whatsapp_turn_on)))
                    return
                }
                if (home) form.addView(Ui.hint(context, context.getString(R.string.whatsapp_how)))
                val rows = mutableListOf<Pair<String, String>>()
                if (home) rows += context.getString(R.string.proxy_host) to "127.0.0.1"
                rows += context.getString(R.string.whatsapp_chat_port) to ports.first.toString()
                rows += context.getString(R.string.whatsapp_media_port) to ports.second.toString()
                form.addView(Ui.valueBlock(context, rows))
            }
            addView(Switch(context).apply {
                text = context.getString(R.string.whatsapp_switch)
                isChecked = settings.whatsappEnabled
                setOnCheckedChangeListener { _, on ->
                    try {
                        settings.setWhatsApp(context, on)
                        render()
                        changed()
                    } catch (_: IllegalStateException) {
                        isChecked = settings.whatsappEnabled
                        Toast.makeText(context, R.string.whatsapp_no_ports, Toast.LENGTH_LONG).show()
                    }
                }
            })
            addView(form)
            render()
        }
}
