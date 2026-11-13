package info.gluek.passage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test
import org.json.JSONObject

class ReportPrivacyTest {
    @Test fun reportMasksOwnIpv6Prefix() {
        val text = "read udp [2001:db8:10::8]:1234->[2001:db8:20::53]:53: timeout; [2001:db8:10::53]:53; via 2001:db8:10::4: refused"
        val prefix = NetInfo.Prefix(java.net.InetAddress.getByName("2001:db8:10::1"), 64)
        assertEquals("read udp [addr]->[2001:db8:20::53]:53: timeout; [addr]; via [addr]: refused", Diagnostics.reportText(text, listOf(prefix)))
    }

    @Test fun diagnosticsRedactSecretsAndLocalEndpoints() {
        val text = "password: two words\nread udp 192.0.2.4:40112->192.0.2.53:53: i/o timeout"
        val result = Diagnostics.redact(text)
        assertFalse(result.contains("two words"))
        assertEquals("password: [redacted]\nread udp [addr]->192.0.2.53:53: i/o timeout", result)
    }

    @Test fun reportRedactsEveryStringAndDeviceDns() {
        val report = JSONObject("""{"error":"token=private-value","network_test":["password=private-value"],"device_network":{"dns":["2001:db8:10::53"]}}""")
        val prefix = NetInfo.Prefix(java.net.InetAddress.getByName("2001:db8:10::1"), 64)
        StatusText.redactReportValue(report) { Diagnostics.reportText(it, listOf(prefix)) }
        assertFalse(report.toString().contains("private-value"))
        assertEquals("[addr]", report.getJSONObject("device_network").getJSONArray("dns").getString(0))
    }

    @Test fun secretsPreserveFollowingFields() {
        for (text in listOf("""{"token":abc,"status":"ok"}""", """{"token":"two words","status":"ok"}""")) {
            assertEquals("""{"token":[redacted],"status":"ok"}""", Diagnostics.redactSecrets(text))
        }
    }
}
