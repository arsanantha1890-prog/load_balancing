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
