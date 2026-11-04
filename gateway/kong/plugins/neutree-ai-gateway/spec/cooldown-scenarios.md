# Passive target failover acceptance

| Scenario | Expected behavior |
| --- | --- |
| Primary healthy | Existing priority, weight and concurrency admission apply. |
| Primary returns 429/502/503/504 or fails before response headers | Subsequent selection skips it for 15 seconds; the failing request is not replayed. |
| Cooldown expires | One request per target and Kong instance probes; concurrent requests use other eligible targets. |
| Current probe receives 200 / retryable failure | Restore eligibility / start another cooldown. |
| Old ordinary response or expired probe completes | Cannot clear or overwrite a newer health decision. |
| All candidates cooling, probing or at capacity | Return 503; never bypass health or concurrency admission. |
| Selected probe rejected by local access/quota, or canceled | Release its probe admission without marking the target unhealthy. |
| DNS, buffered and streaming transport failures | Attribute to the selected target; local validation/limits are not upstream failures. |
| Partial SSE after 200 | No replay; HTTP-header health does not claim successful completion of inference. |
| Multiple primaries/backups and weights | Filter unhealthy candidates, then retain existing scheduling. |
| Health storage unavailable | Surface the error; do not silently bypass health admission or evict inflight counters. |
| Target removed from configuration | Remove historical health state without deleting live concurrency leases. |

State is instance-local and reset on gateway restart. Requests already selected
before the failure was recorded may also fail. Buffered requests can outlive a
client disconnect and subsequently report a real upstream timeout. Probe lease
expiry permits a replacement; it does not prove the previous GPU work stopped.
