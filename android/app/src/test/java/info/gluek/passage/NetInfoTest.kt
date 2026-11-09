package info.gluek.passage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test

class NetInfoTest {
    @Test
    fun keyUsesResolverSet() {
        val first = NetInfo.Snapshot(listOf("a.example.org:53", "b.example.org:53"), "test", "wifi", false)
        val reordered = first.copy(dns = listOf("b.example.org:53", "a.example.org:53", "a.example.org:53"))
        assertEquals(first.key, reordered.key)
        assertNotEquals(first.key, first.copy(dns = listOf("c.example.org:53")).key)
        assertNotEquals(first.key, first.copy(operator = "other").key)
    }
}
