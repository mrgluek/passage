package info.gluek.passage

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Bundle
import android.widget.Toast

/**
 * Takes the installer's answer, shows nothing itself. Not exported, unlike
 * the settings screen: the answer names a screen to open.
 */
class InstallResultActivity : Activity() {
    override fun attachBaseContext(newBase: Context) = super.attachBaseContext(AppLanguage.wrap(newBase))

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT)?.let { startActivity(it) }
            }
            PackageInstaller.STATUS_SUCCESS -> Updater.clear(this)
            PackageInstaller.STATUS_FAILURE_ABORTED -> {} // the user said no
            else -> {
                val message = getString(R.string.update_install_failed)
                val reason = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: status.toString()
                Updater.fail(message, IllegalStateException(reason))
                Toast.makeText(this, message, Toast.LENGTH_LONG).show()
            }
        }
        finish()
    }
}
