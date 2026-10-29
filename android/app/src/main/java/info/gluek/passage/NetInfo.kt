package info.gluek.passage

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import android.telephony.TelephonyManager
import java.net.Inet6Address

/**
 * The resolvers the current network handed out and the SIM's operator code.
 * The tunnel puts the network's resolvers first.
 */
object NetInfo {
    data class Snapshot(
        val dns: List<String>,
        val operator: String,
        val transport: String,
        val privateDns: Boolean,
    ) {
        /** A change of either restarts the tunnel (soar's path set is fixed per client). */
        val key: Pair<List<String>, String> get() = dns to operator
    }

    fun snapshot(context: Context): Snapshot {
        val dns = ArrayList<String>()
        var transport = "none"
        var privateDns = false
        try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            val net = cm.activeNetwork
            val lp = net?.let { cm.getLinkProperties(it) }
            lp?.dnsServers?.forEach { addr ->
                // Link-local IPv6 needs a scope id the tunnel can't use.
                if (addr.isLoopbackAddress || addr.isLinkLocalAddress) return@forEach
                val host = addr.hostAddress?.substringBefore('%') ?: return@forEach
                dns.add(if (addr is Inet6Address) "[$host]:53" else "$host:53")
            }
            if (lp != null && Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                privateDns = lp.isPrivateDnsActive
            }
            val caps = net?.let { cm.getNetworkCapabilities(it) }
            transport = when {
                caps == null -> "none"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
                else -> "other"
            }
        } catch (_: Exception) {
        }
        var operator = ""
        try {
            val tm = context.getSystemService(Context.TELEPHONY_SERVICE) as TelephonyManager
            // MCC+MNC of the SIM, e.g. 25001; the network's when there is no SIM code.
            operator = tm.simOperator?.takeIf { it.isNotEmpty() } ?: tm.networkOperator ?: ""
        } catch (_: Exception) {
        }
        return Snapshot(dns, operator, transport, privateDns)
    }
}
