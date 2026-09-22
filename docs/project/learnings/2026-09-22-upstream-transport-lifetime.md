# 2026-09-22 — Match upstream connection lifetime to its client

**Context.** Inference dispatch and asynchronous media operations create a private HTTP client for each upstream request. Those transports inherited the default 90-second idle timeout even though no subsequent request could reuse them.

**Evidence.** The initial Linux load run reached 7,715 goroutines and 194.40 MiB sampled RSS. During the final three full minutes of 50-stream traffic, roughly 4,800 goroutines remained; they returned to 14 only after cooldown. Focused regression tests reproduced retained connections after completed HTTP/1.1 and HTTP/2 responses, a cancelled HTTP/2 stream, and a completed Replicate poll. The matching artifact and measurements are recorded in [benchmark](../benchmark.md).

**Change.** Disable idle connection reuse in both per-request transport factories. All native/translated inference and Replicate/Together/Gemini/custom media calls use those factories. Active response bodies, streaming, HTTP/2 negotiation, DNS checks, proxy rejection, and timeouts retain their existing paths. A shared transport pool would need its own policy and lifetime design; it is not introduced by this fix.

**Test learning.** The Anthropic cancellation mock originally flushed a response without consuming its POST body, then waited for request cancellation. Go detects a peer disconnect after request-body EOF. With `Connection: close`, the server no longer automatically drains that unread body during the flush, so only the mock's cleanup hung; gateway cancellation and accounting had already completed. The mock now consumes the input like a provider and explicitly verifies upstream cancellation with a bounded wait and cleanup fallback. The cancellation assertion remains enabled.

**Consequence.** Resource checks must include traffic long enough to expose connection retention, per-minute goroutine/RSS samples, and a cooldown. A successful request count or one initial idle-memory sample would miss this behavior.
