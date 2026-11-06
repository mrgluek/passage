package info.gluek.passage

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class SubscriptionsTest {
    @Test fun closedRowUsesPublicMetadata() {
        assertTrue(Subscriptions.isClosed(JSONObject("""{"closed":true,"last_error":"network"}""")))
        assertFalse(Subscriptions.isClosed(JSONObject()))
        assertFalse(Subscriptions.isClosed(JSONObject("""{"closed":false}""")))
    }

    @Test fun routeChangesCountIdsRatherThanTypesOrPriorities() {
        val before = JSONObject("""{"routes":[{"id":"a","type":"wss","priority":10},{"id":"b","type":"wss"}]}""")
        val after = JSONObject("""{"routes":[{"id":"b","type":"wss","priority":20},{"id":"c","type":"wss"}]}""")
        assertEquals(1 to 1, Subscriptions.delta(before, after))
        assertEquals(0 to 0, Subscriptions.delta(after, after))
        assertEquals(0 to 2, Subscriptions.delta(after, JSONObject()))
    }

    @Test fun ownershipUsesExactRouteIds() {
        val config = JSONObject("""{"subscriptions":[{"name":"Example","route_ids":["abc"]}]}""")
        assertEquals("Example", Subscriptions.owner(config, "abc"))
        assertEquals("", Subscriptions.owner(config, "ab"))
        assertEquals("", Subscriptions.owner(config, "manual"))
    }

    @Test fun refreshErrorsAndUnchangedAreMapped() {
        assertNull(Subscriptions.errorResource(""))
        assertNull(Subscriptions.errorResource("unchanged"))
        val codes = listOf("network", "http", "size", "decrypt", "signature", "rollback", "collision", "required")
        assertEquals(codes.size, codes.map { Subscriptions.errorResource(it) }.toSet().size)
        assertEquals(R.string.sub_error_network, Subscriptions.errorResource("unexpected"))
    }

    @Test fun subscriptionSecretsAreRedacted() {
        val text = Diagnostics.redact("""{"read_key":"throwaway-secret","signer":"throwaway-signer","sources":["https://example.org/sub"]}""")
        assertFalse(text.contains("throwaway-secret"))
        assertFalse(text.contains("throwaway-signer"))
        assertFalse(text.contains("example.org"))
    }
}
