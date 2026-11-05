package info.gluek.passage

import android.app.Activity
import android.content.Context
import android.content.res.Configuration
import android.os.Bundle

/** Apply the saved theme before any views or dialogs are created. */
abstract class ThemedActivity : Activity() {
    private lateinit var appliedTheme: String

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(newBase)
        appliedTheme = Settings(newBase).theme
        val night = when (appliedTheme) {
            "light" -> Configuration.UI_MODE_NIGHT_NO
            "dark" -> Configuration.UI_MODE_NIGHT_YES
            else -> return
        }
        applyOverrideConfiguration(Configuration().apply {
            fontScale = 0f
            uiMode = night
        })
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        setTheme(R.style.Theme_Passage)
        super.onCreate(savedInstanceState)
    }

    override fun onResume() {
        super.onResume()
        // The home screen may have been stopped while settings changed.
        if (appliedTheme != Settings(this).theme) recreate()
    }
}
