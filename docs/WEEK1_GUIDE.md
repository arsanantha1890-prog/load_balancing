# Week 1 Guide — from zero Go to a working load balancer

Read this top to bottom with the code open beside it. Every section tells you
what to run, what you should see, and what Go concept you just used.

Budget: **5–6 focused hours**, or two evenings.

---

## 0. Install Go (15 minutes)

Download from <https://go.dev/dl/>. Verify:

```bash
go version   # want go1.22 or newer
```

Then unzip this project and:

```bash
cd gobalance
go mod tidy    # downloads gopkg.in/yaml.v3, the only dependency
go test ./...  # everything should pass
```

If `go test` passes, your toolchain is fine and the code is fine. Now you can
read it with confidence instead of wondering whether something is broken.

**Rename the module before you push to GitHub.** Open `go.mod`, change
`github.com/yourname/gobalance` to your actual GitHub path, then:

```bash
grep -rl "github.com/yourname/gobalance" . | xargs sed -i 's|github.com/yourname/gobalance|github.com/YOURNAME/gobalance|g'
go mod tidy && go test ./...
```

---

## 1. The five Go concepts this project actually uses

You do not need to "learn Go" first. You need these five things. Everything else
you will pick up by reading the code.

### 1.1 Structs and methods

```go
type Backend struct {
    Name string
    URL  *url.URL
}

func (b *Backend) String() string { return b.Name }
```

No classes, no inheritance. A struct is data; a method is a function with a
receiver (`b *Backend`) attached. Capitalised names are exported (public);
lowercase names are package-private. That is the entire access-control system.

### 1.2 Interfaces are implicit

```go
type Strategy interface {
    Name() string
    Pick(r *http.Request, healthy []*backend.Backend) (*backend.Backend, error)
}
```

There is no `implements` keyword. `*RoundRobin` satisfies `Strategy` simply by
having those two methods. This is why adding a strategy in week 2 requires
**zero changes** to `proxy.go`.

Read `internal/strategy/strategy.go` now. That one file is the architecture.

### 1.3 Errors are values

```go
b, err := backend.New(name, url, weight, dial, respHdr)
if err != nil {
    return nil, fmt.Errorf("building backend: %w", err)
}
```

No exceptions, no try/catch. `%w` wraps an error so the caller can still inspect
the original with `errors.Is`. You will type `if err != nil` several hundred
times and then stop noticing it.

### 1.4 Goroutines and the race problem

```go
go func() { ... }()   // runs concurrently, costs ~2KB
```

`net/http` starts **one goroutine per request**, automatically. So the moment
you have traffic, many goroutines touch the same `Backend` at once. That is why
`internal/backend/backend.go` uses `atomic.Int64` for the in-flight counter and
`internal/pool/pool.go` uses a `sync.RWMutex` for the backend list.

Prove it to yourself:

```bash
go test -race ./...
```

Then break it deliberately — change `inflight atomic.Int64` to `inflight int64`
and the `Add` calls to `b.inflight++` — and run `-race` again. Watch it fail.
**Do this exercise.** It is the single best twenty minutes you will spend this
week, and "I have used the Go race detector to catch a real data race" is a
sentence that lands in interviews.

### 1.5 `defer`

```go
target.IncInflight()
defer target.DecInflight()
```

`defer` runs when the function returns — including on panic. This is how the
in-flight counter is guaranteed to come back down. Forget it, and
least-connections in week 2 slowly concludes every backend is overloaded and
your load balancer stops balancing. `TestInflightReturnsToZero` exists to catch
exactly that.

---

## 2. Read the code in this order

Do not read it alphabetically. Follow the request.

1. **`internal/config/config.go`** — how a YAML file becomes a validated struct.
   Note that defaults live in one function, and that `KnownFields(true)` turns a
   typo into a startup error instead of a mystery.
2. **`internal/backend/backend.go`** — one upstream. Note `httputil.ReverseProxy`
   with a `Rewrite` function: **this is the half of the project the standard
   library gave you for free.** You are not writing an HTTP parser.
3. **`internal/pool/pool.go`** — the RWMutex. Read the comment about why `Set`
   copies the incoming slice.
4. **`internal/strategy/strategy.go`** — the interface, the `Observer` split,
   the registry. Then `roundrobin.go` (atomic counter, no lock) and `random.go`
   (five lines, proves the plugin system).
5. **`internal/proxy/proxy.go`** — the hot path, ~60 lines. Everything else in
   the project exists to serve this function.
6. **`cmd/gobalance/main.go`** — wiring, two servers, graceful shutdown.

---

## 3. Run it

Terminal 1:

```bash
make backends   # alpha :9001 (10ms), beta :9002 (10ms), gamma :9003 (80ms)
```

Terminal 2:

```bash
make run
```

Terminal 3:

```bash
make smoke
```

Expected: a perfect `alpha → beta → gamma` rotation, then a JSON dump of the
pool.

Then try these by hand:

```bash
# see the routing headers
curl -i localhost:8080/ | head -20

# ask a backend to be slow for one request
curl "localhost:8080/?delay=2s"

# ask a backend to fail
curl -i "localhost:8080/?status=500"

# watch the failure counter move
curl -s localhost:8081/backends
```

**The experiment that motivates the entire project.** Fire 30 concurrent
requests and time them:

```bash
time (for i in $(seq 1 30); do curl -s localhost:8080/ > /dev/null & done; wait)
```

Round-robin sends exactly 10 requests to `gamma`, which is 8× slower than the
others. Your p99 is gamma's latency, and no amount of spare capacity on alpha
and beta helps. **That is the problem statement of your entire project, and you
can now demonstrate it in one command.** In week 2 you will run the same command
with `strategy: p2c` and watch the tail collapse.

---

## 4. Your exercises for the week

Do these rather than just reading. Each one is small and each one teaches
something you will be asked about.

1. **Break the race detector** (section 1.4). Then fix it back.
2. **Add a `least_recently_used` strategy** — track a `lastUsed` timestamp per
   backend, pick the oldest. ~20 lines. If you can do this without touching
   `proxy.go`, you have understood the architecture.
3. **Add an `X-Request-Id` header** in `proxy.go` if the client did not send one
   (use `crypto/rand`). Real proxies do this; it makes distributed tracing possible.
4. **Make the admin API report the strategy's pick count per backend** and check
   `/backends` after `make smoke`.
5. **Write a test** that asserts a 500 from the upstream increments
   `Failures()`. Copy the shape of `TestDeadUpstreamReturns502`.

---

## 5. Commit hygiene (this is on your resume too)

Recruiters open the commit history. Make it look deliberate:

```bash
git init
git add .
git commit -m "feat: proxy core, backend pool and strategy plugin interface"
```

Small, typed commits (`feat:`, `test:`, `docs:`, `fix:`), one per logical change.
Push after every session. A repo with 60 meaningful commits over eight weeks
reads completely differently from one with three commits titled "final".

Push to GitHub now — the CI workflow in `.github/workflows/ci.yml` will run
`go vet` and `go test -race` on every push, and a green badge on the README
costs you nothing and signals a lot.

---

## 6. What "done" looks like for week 1

- [ ] `go test -race ./...` passes
- [ ] `make backends && make run && make smoke` shows a clean rotation
- [ ] You can explain why `pool` uses `RWMutex` but `RoundRobin` does not
- [ ] You can explain what `Observer` is for and why it is a separate interface
- [ ] You have run the 30-concurrent-request experiment and seen the tail latency
- [ ] Pushed to GitHub with CI green

Then week 2 is four new files in `internal/strategy/` and nothing else.

---

## 7. Interview answers you already own after week 1

- *Why is there no mutex in round-robin?* One atomic counter is sufficient; a
  mutex would serialise every request through one lock.
- *Why does `Observer` exist separately from `Strategy`?* Interface segregation
  — round-robin needs no feedback, so forcing it to implement empty callbacks
  would be noise. Strategies opt in via a type assertion resolved once at
  construction, not per request.
- *What does `statusRecorder.Unwrap` do?* It lets `http.ResponseController`
  reach the underlying `ResponseWriter`, so flushing and hijacking still work
  through the wrapper. Without it, streaming responses break.
- *Why two HTTP servers?* An admin path on the proxy port would shadow real
  customer URLs, and operational endpoints should not share an exposure surface
  with traffic.
- *Why graceful shutdown?* A Kubernetes rolling update sends SIGTERM; without
  draining, every in-flight request during a deploy becomes a client error.
