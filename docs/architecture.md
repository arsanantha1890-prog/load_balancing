# Architecture notes

Living document. Each week adds a section explaining a decision and its
alternatives — this is the raw material for the final report.

## Week 1

### Why `httputil.ReverseProxy` instead of writing the proxy by hand

Writing an HTTP/1.1 parser is a solved problem and getting it wrong is a
security incident (request smuggling, header injection). `ReverseProxy` handles
hop-by-hop header stripping, connection reuse, HTTP/2, streaming bodies and
context cancellation. The project's contribution is the *selection* logic, not
the byte handling — so the interesting code stays interesting.

### Why the `Strategy` / `Observer` split

`Pick` is required; feedback is optional. Round-robin and random need no
feedback at all, least-connections needs start/finish, EWMA needs measured
latency. Putting all of it into one interface would force empty method bodies on
two of five implementations. The proxy resolves the optional interface **once**
at construction (`New`), so the request path performs no type assertion.

### Why `RWMutex` on the pool but `atomic` on the counters

The pool is read once per request and written on config reload or endpoint
change — read-heavy by orders of magnitude, which is what `RWMutex` is for.
Counters are written on *every* request by definition, so a mutex would be a
global serialisation point; `sync/atomic` gives lock-free increments.

### Why the pool copies slices on read and write

`Healthy()` returns a fresh slice so the caller can iterate without holding a
lock. `Set()` copies its input so the caller cannot mutate pool state after the
fact. This costs one allocation per request, which is measurable but not
significant at week-1 scale. If week 6 shows it matters, the fix is a cached
healthy-list invalidated on health transitions — but only with a benchmark
justifying it.

### Known limitations (deliberate, to be removed later)

| Limitation | Removed in |
|---|---|
| Backends are assumed healthy forever | week 3 |
| No retries on transport failure | week 3 |
| No metrics beyond the admin JSON | week 4 |
| Backend list is static | weeks 4–5 |
| No benchmark | week 6 |



# Architecture notes — week 2

Append this to `docs/architecture.md`.

## What was added

Four strategies, plus a corrected LRU. **No file outside `internal/strategy/`
was touched** except a two-line CLI flag in `main.go`. That is the plugin
interface from week 1 paying for itself.

| Strategy | Signal used | Cost per pick | Needs `Observer` |
|---|---|---|---|
| `round_robin` | none | O(1) | no |
| `random` | none | O(1) | no |
| `weighted_round_robin` | static config | O(n), one mutex | no |
| `least_connections` | in-flight count | O(n) | no |
| `p2c` | in-flight count | **O(1)** | no |
| `ewma` | measured latency + in-flight | **O(1)**, one mutex | **yes** |
| `lru` | completion timestamp | O(n), one mutex | yes |

## A correction to the week-1 design note

Week 1 predicted that least-connections would need `Observer`. It does not.

The in-flight counter lives on `Backend`, and `proxy.ServeHTTP` already
maintains it — increment before forwarding, decrement via `defer`. So the state
least-connections depends on is a **side effect of the request path**, not
something the strategy has to track. Only EWMA genuinely needs `Observer`,
because measured latency is the one signal nothing else records.

The design still holds; the reasoning for it was sharper than expected. Worth
recording honestly rather than quietly fixing.

## Why smooth WRR rather than weight expansion

Expanding weights 5:1:1 into `[a a a a a b c]` gives the correct ratio and a
terrible order — five consecutive requests to one backend. The smooth algorithm
(nginx's) produces `a a b a c a a`: identical ratio, no burst. Ratio is not the
only property that matters; **arrival pattern shapes queueing**.

## Why P2C rather than always choosing the global minimum

Two reasons, and the second is the one that matters at scale:

1. **Cost.** Least-connections is O(n) per request; P2C is O(1). At 10,000
   backends that is a scan versus two lookups.
2. **Herd behaviour.** When several load balancer replicas each independently
   pick the globally least-loaded backend, they pick the *same* one at the same
   instant and stampede it, then move together to the next victim. Randomised
   sampling makes replica decisions uncorrelated, so the herd never forms.

The theory: with `n` balls into `n` bins, random placement gives a maximum load
of `Θ(log n / log log n)`; best-of-two gives `Θ(log log n)`. Exponentially
better, from one extra sample. Going to `d = 3` buys almost nothing — the entire
gain is between `d = 1` and `d = 2`.

## Why EWMA is *peak* EWMA, and why cost includes in-flight

A symmetric moving average takes as long to notice degradation as recovery. That
trade-off is wrong: the damage from routing to a degraded backend is immediate.
So the score jumps straight up on a slow response and decays only on fast ones.
React instantly to bad news, cautiously to good news. Finagle calls this Peak
EWMA.

Cost is `smoothed_latency × (inflight + 1)`. Pure historical latency is
backward-looking — it cannot see requests dispatched two milliseconds ago that
have not returned. The multiplication converts the score into an **estimated
queue time**, which is what makes the strategy stable under bursts instead of
oscillating.

Decay is weighted by **elapsed time**, not request count, so a backend that goes
quiet is forgiven at the same rate as one under constant traffic. A fixed alpha
would make recovery speed depend on how busy the system happens to be.

`ewmaTau = 10s` is Finagle's default and is currently a guess. Week 6 should
tune it with data — "the decay constant is a tunable I benchmarked" is a much
better sentence than "I picked 10s".

## The week-1 exercise that turned into a lesson

The first LRU recorded timestamps at **pick** time, and collapsed into
round-robin exactly. "Least recently dispatched to" *is* "whose turn it is".

Moving the timestamp to **completion** time makes a slow backend hold a stale
timestamp for the whole time it is working, so it stops looking attractive
precisely while busy. That requires `Observer` — which is what `Observer` is
for. The corrected version also keys on `b.Name` rather than the `*Backend`
pointer, because weeks 4 and 5 rebuild `Backend` values on every reconcile;
pointer keys would leak memory and silently lose all state.

LRU is still weaker than least-connections — one timestamp cannot distinguish
"idle" from "handling twenty requests" — and it is kept as a worked example of
the Observer pattern, not as a contender.

## Known limitation, carried forward to week 6

The mock backend calls `time.Sleep`, so it has **unlimited concurrency**. Gamma
is *slow* but never *saturates*, which means these measurements show latency
avoidance, not congestion relief — and real tail latency comes mostly from
queueing. Week 6 must add a concurrency limit to the mock backend before the
benchmark can claim to model a real system.

