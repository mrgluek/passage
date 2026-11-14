package info.gluek.passage

import android.content.Context
import android.widget.LinearLayout
import android.widget.Switch
import android.widget.Toast

object WhatsAppUi {
    fun controls(context: Context, settings: Settings, home: Boolean, changed: () -> Unit): LinearLayout =
        LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            val form = LinearLayout(context).apply { orientation = LinearLayout.VERTICAL }
            fun render() {
                form.removeAllViews()
                if (home && !settings.whatsappEnabled) {
                    form.addView(Ui.hint(context, context.getString(R.string.whatsapp_turn_on)))
                    return
                }
                if (home) form.addView(Ui.hint(context, context.getString(R.string.whatsapp_how)))
                val rows = mutableListOf<Pair<String, String>>()
                if (home) rows += context.getString(R.string.proxy_host) to "localhost"
                rows += context.getString(R.string.whatsapp_chat_port) to settings.whatsappChatPort.toString()
                rows += context.getString(R.string.whatsapp_media_port) to settings.whatsappMediaPort.toString()
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
