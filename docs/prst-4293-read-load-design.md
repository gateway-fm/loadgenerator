# PRST-4293 — read-query load in GasStorm: use cases and design

Status: **design, not yet implemented.** Branch `feat/prst-4293-read-load`, stacked on
`fix/prst-4262-nonce-drift-and-soak-duration` (PR #50).

Follow-on from PRST-4262. That campaign characterised the **write** path well and the
read path not at all: after 2.85M requests the method census was 98.2%
`eth_sendRawTransaction` and **nine** `eth_call`s. Every headline number we hold —
1,500 tx/s sustained, the ~1,660 tx/s config ceiling, the storage cost model, the
ZFS-vs-LVM comparison — describes a write-only workload.

---

## 1. Use cases — what the read generator has to answer

Ordered by what actually blocks a decision. UC1 is the only one with a deadline
attached (it gates a storage recommendation); the rest are cumulative value.

> **UPDATE 2026-08-13 — UC1 is no longer blocking; PRST-4262 answered it.** Since this doc
> was written, PRST-4262 merged (poc repo PR #4) with three findings that overtake UC1:
> the nodes have two SSD classes and the chain had been on the **slow Kingston DC400s**;
> `8x ARC does NOT fix it — the DC400 drives are the wall`; and re-run on the fast Micron
> mirror, `primarycache=metadata` **halves sustainable state — keep the default**
> (baseline `all`: 117.2 GiB, 6.04 h, 1,461.3 tx/s, never degraded).
>
> So the storage recommendation **has** been made (`primarycache=all`), and `metadata` lost
> on the **write** path — by halving sustainable state, not for want of read load. The
> paragraphs below overstate the case: read load may still move the margin, but it is no
> longer the blocking question, and "the comparison likely inverts" is now a weak claim
> against a variable that already lost decisively. R3a/R3b drop to nice-to-have.
>
> The consequence for the rest of this plan: the storage wall on the current PoC hardware is
> the **drives**, so read-capacity arms there would measure the DC400s, not the read path.
> The real read-capacity campaign belongs on the larger servers — the same place archive
> testing went (§3.7).

### UC1 — Decide `primarycache=all` vs `primarycache=metadata` (~~blocking~~ — see update above)

The reason this ticket exists. Current PRST-4262 measurements at 1500 tx/s, matched
elapsed:

| | `primarycache=all` | `primarycache=metadata` |
|---|---|---|
| ARC size | pinned at 8.0 GiB | 0.77 GiB |
| p50 | 623–627 ms | 675–690 ms |
| p95 | 797–803 ms | 890–908 ms |

`metadata` frees ~7.2 GiB of RAM per node for ~10% latency — measured against a
workload with nine state reads in 2.85M requests. ARC data caching exists to serve
reads; we deleted the cache and then did not read. With a 117 GiB working set,
Nitro's 2 GiB `database-cache` and no ARC data cache, most random state reads must
reach disk. **The expectation is that the comparison inverts.** We cannot make a
storage recommendation for an RPC-serving node until it is re-run under read load.

Requirement this imposes: reads must be **uncached and randomly distributed over
state** — otherwise we re-measure the ideal case for `metadata` a second time.

### UC2 — Size the read tier

PRST-4262 established that the RPC tier, not the sequencer, is the binding resource:
its disks saturate first (0.98/0.87 util vs 0.20 on the sequencer), and its CPU
*climbs with state* (0.6 → 2.2 cores over five minutes at constant 1000 tx/s). All of
that was measured under **write-forwarding** load only. Read serving is a different
access pattern — random state lookups against sequential feed-following — and in
production is likely the dominant cost. We need reads/s per replica per core, and
where the read tier falls behind the feed.

### UC3 — Does read load move the write ceiling?

The single most valuable new fact available. Reads and writes contend for the same
RPC replicas, the same disks and the same ARC. Pin writes at the known-good 1500 tx/s
and ladder reads independently. This is *why* the read rate must never be folded into
`targetTps` — a combined knob makes the question unaskable.

### UC4 — State capacity the way a customer buys it

"1,500 tx/s" is not a product. A customer buys "N writes/s **and** M reads/s at
p99 X ms". Today we can state half of that. UC4 is UC2+UC3 written down as a number
pair with a latency bound.

### UC5 — Characterise **archive and non-archive** read serving, as separate products

GasStorm must be able to load-test both, because Gateway sells both. This is a
first-class mode, not a probe that proves a failure.

The two are different workloads, not the same workload with a different flag:

| | non-archive (pruned) | archive |
|---|---|---|
| state reads target | `latest` / last ~128 blocks | **any block in history** |
| working set | recent state, fits cache tiers | **whole history — cache hit rate collapses** |
| storage | PRST-4262: pruning reclaims ~87% | no pruning, no reclaim; the cost model does not carry over |
| what binds | RPC CPU + write amplification | almost certainly random-read IOPS |

So the read generator needs a **block-selection dimension orthogonal to the method
mix**: the same `eth_call` is a cheap cached lookup at `latest` and a cold random
historical trie walk at block 40,000. Under archive selection, state-reading methods
take a historical block tag instead of `latest`, and `eth_getLogs` windows can span
far more than `blockWindow`.

Two things follow, and both are requirements:

- **Capability detection with fail-fast.** Archive selection against a pruned node
  returns `missing trie node` / state-unavailable on essentially every call. That must
  be detected at start and refused, **not** generated as a 100%-error run — an error
  storm rendered as latency percentiles is indistinguishable from a performance result,
  which is the same class of mistake as the 21,375 gas/tx arm.
- **The archive boundary becomes a measured output rather than a claim.** Sweeping
  depth until reads start failing locates the real horizon on a given node. For the
  PoC chain that yields the customer-facing sentence directly: *this configuration
  serves current state well and cannot serve historical state at all; archive is a
  separate node with a separate storage model.*

### UC6 — Do not invalidate the existing corpus

Every PRST-4262 arm must remain reproducible and comparable. A write-only arm with
`readLoad` absent must behave *identically* to `prst4262-7`.

---

## 2. What the code already gives us

Read before designing; three findings removed most of the anticipated work.

**`rpc.Client` already has a generic escape hatch.** `internal/rpc/client.go:22`:

```go
Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error)
BatchCall(ctx context.Context, calls []BatchRequest) ([]BatchResponse, error)
```

plus typed `GetBalance`, `GetCode`, `GetBlockByNumber`, `GetTransactionReceipt`.
So `eth_call` and `eth_getLogs` need **no new interface methods**. This matters more
than it looks: `Client` is a 17-method interface implemented by `HTTPClient`,
`nobatch_client` and the test mocks — every method added is churn across all of them,
for zero benefit. **Design rule: the read engine consumes `Call`/`BatchCall` only and
adds nothing to `rpc.Client`.**

**`ratelimit.Limiter` is a standalone struct** (`internal/ratelimit/limiter.go`) with
`New(rate)`, `Wait(ctx)`, `SetRate()`. A second, fully independent instance for reads
is three lines. Satisfies the ticket's "own rate knob" rule structurally rather than
by discipline.

**`metrics.StreamingLatencyStats`** (`internal/metrics/latency.go:51`) is independently
constructible with `Add(ms)` / `GetStats() *types.LatencyStats`. Read latency
histograms need **no change to the 37-method `metrics.Collector` interface** — the
read engine owns its own instances.

Contract addresses and the funded pool are already on the orchestrator:
`lg.erc20Contract`, `lg.gasConsumerContract` (`loadgen.go:52`), and
`accountMgr.GetAccounts()` / `GetDynamicAccounts()`. The 2,000 funded accounts and the
deployed ERC-20 are exactly the realistic-argument source a standalone tool would have
had to rediscover — which is the whole rationale for extending GasStorm.

---

## 3. Design

### 3.1 Isolation — the part that is easy to get wrong

The ticket asks for separate reporting. The code demands something stronger: **the
read path must not touch any field the write path's control loops read.** Two of these
are silent-corruption traps, not cosmetic issues.

`adaptiveController()` (`workers.go:345`) makes rate decisions from exactly four
shared fields:

| field | written by | read by | if reads touched it |
|---|---|---|---|
| `recentFails` / `recentSends` | send callback | circuit breaker | **read errors would trip the breaker at 30% and halve the write rate** |
| `pendingCount` | send path | adaptive rate control | reads would fake backpressure and suppress the write rate |
| `metricsCol` tx counters | send path | `/v1/status`, headline result | read volume would inflate `txSent`; `avgTps` becomes meaningless |
| `rateLimiter` | pattern/controller | sender workers | shared limiter couples the two rates — UC3 unanswerable |

A read error incrementing `recentFails` is the worst case: `eth_getLogs` erroring at
>30% would silently halve the offered write rate mid-arm, and the resulting "reads
lower the write ceiling" conclusion would be an artefact of our own instrumentation.
**Rule: the read engine shares the context and the account/contract addresses. Nothing
else.** Its own limiter, own counters, own latency stats, own goroutine pool, own `wg`.

### 3.2 Placement

A new package `internal/readload`, owning:

```
readload.Engine
  ├── limiter   *ratelimit.Limiter        // independent rate
  ├── client    rpc.Client                // own HTTP transport + conn pool
  ├── targets   snapshot of addresses / contract / recent blocks / tx hashes
  ├── perMethod map[string]*methodStats   // count, errors, StreamingLatencyStats
  └── workers   []goroutine
```

Started from `runInitialization` **after** contracts are deployed and accounts funded
(otherwise `eth_call` hits an address with no code — see §5), stopped by the same
`lg.ctx` cancel and joined before the report. `LoadGenerator` gains one nullable
field, `readEngine *readload.Engine`; when `readLoad` is absent it stays nil and no
goroutine, client or allocation exists. That is what makes UC6 structural.

Read workers are a **separate pool**, not extra work inside `senderWorker`.
`senderWorker` is pinned one-per-account and, since PR #50, serialises per-account
batches — bolting reads into it would rate-couple reads to writes and re-introduce the
very serialisation PR #50 removed.

### 3.3 Config — opt-in, default-off

Absent from every existing arm, so no recorded or future write-only result changes.

```json
"readLoad": {
  "enabled": true,
  "targetRps": 3000,
  "concurrency": 0,             // 0 = derive from targetRps x observed latency
  "rpcUrl": "",                 // "" = same public URL as writes
  "blockSelection": "recent",   // latest | recent | archive   (see §3.7)
  "blockWindow": 64,            // "recent": sample within this many blocks of head
  "archiveDepthPct": 100,       // "archive": sample the deepest N% of history
  "requireArchive": true,       // fail fast if the target is not archive
  "logsRangeBlocks": 16,        // bounded eth_getLogs span
  "fullBlocks": false,          // eth_getBlockByNumber includeTxs
  "mix": { "ethCall": 60, "getBalance": 15, "getLogs": 10,
           "getBlockByNumber": 10, "getReceipt": 5 }
}
```

`rpcUrl` defaults to the same public URL as writes — the read path we care about
includes the CCM LB and Envoy. An override exists because UC2 wants the option of
pointing reads at a dedicated replica to test the "third RPC replica" lever
PRST-4262 left on the table.

`concurrency` is explicit-or-derived because a fixed pool that is too small silently
caps read RPS below target and reads as a server limit. **The engine must log and
report when it cannot reach `targetRps`**, rather than quietly delivering less — the
generator-caps-look-like-chain-limits mistake has already cost this campaign several
arms.

### 3.4 The read mix, and why each method is shaped this way

Under the default `recent` selection, non-archive is the binding constraint:
**randomise addresses, not blocks** — randomising blocks against a pruned node mostly
produces errors and would measure our own error path. Under `archive` selection (§3.7)
both are randomised, which is precisely what makes it the harder workload: the address
randomises the slot and the block randomises the trie version, so nothing caches.

| method | share | argument source | why |
|---|---|---|---|
| `eth_call` | 60 | ERC-20 `balanceOf(random funded account)` at the selection's block tag | **the UC1 instrument.** A random account randomises the storage slot, so each call is a fresh random state read that ARC data-caching either serves or does not. Never `totalSupply()` — one hot slot, always cached, measures nothing |
| `eth_getBalance` | 15 | random funded account, at the selection's block tag | account-trie random read, different trie from `eth_call` |
| `eth_getLogs` | 10 | ERC-20 address + Transfer topic, random `logsRangeBlocks` window inside `blockWindow` | the only heavy read; **must be bounded** or it is a self-DoS |
| `eth_getBlockByNumber` | 10 | random block within `blockWindow`, `includeTxs=false` | cheap by default; see egress note |
| `eth_getTransactionReceipt` | 5 | recently-confirmed hash sampled from the generator's own tracking | realistic (clients poll receipts) and needs no extra bookkeeping |

Deliberately excluded from the default mix: `eth_getTransactionCount` (the generator's
own nonce resync reads it; adding synthetic load there muddies a diagnostic we rely
on) and `eth_estimateGas`. Both available, both off.

**Pair read load with a transaction type that deploys the ERC-20.** The default mix is
60% `eth_call` against that contract, so read load on a run that deploys nothing —
`constant` + `eth-transfer`, the most natural first thing to try — refuses to start with
*no ERC-20 contract address*. That refusal is deliberate (§5), but it is easy to hit:
either use a `realistic` mix containing `erc20Transfer`, or set `ethCall: 0` and
redistribute its share.

**Per-method latency reporting is mandatory, not a nicety.** `eth_getLogs` over 16
blocks at 1500 tx/s spans ~24,000 transactions and is orders of magnitude dearer than
`eth_call`. Folded into one histogram at a 10% share it dominates p99 and the headline
"read p99" becomes a statement about `getLogs` window size, not about the read path.

### 3.5 Reported separately

```json
"readLoad": {
  "targetRps": 3000, "readRps": 2987.4,
  "readsSent": 1792440, "readErrors": 118,
  "latency": { "p50": .., "p95": .., "p99": .. },
  "byMethod": {
    "eth_call": { "count": .., "errors": .., "p50": .., "p95": .., "p99": .. },
    "eth_getLogs": { ... }
  }
}
```

Sits beside the existing transaction counters in `/v1/status`, never inside them.

### 3.6 Egress is a real cost here, not a footnote

The servers.com LB bills EUR 0.04/GB and PRST-4262 measured 2,862 B/tx of egress
(~EUR 193/month at 650 tx/s). Reads invert the traffic shape: a request is ~100 B and a
response can be large. `eth_getBlockByNumber` with `includeTxs=true` at 1500 tx/s
returns ~375 full transactions per block — at 300 rps that is plausibly tens of MB/s
of egress, dwarfing the write-side figure. Hence `fullBlocks: false` by default, and
egress belongs in the per-arm capture list.

### 3.7 Block selection: archive and non-archive in one engine

`blockSelection` is orthogonal to `mix`. The mix decides *which* method; the selection
decides *at what block*, which is what actually determines whether a read is cached or
a cold trie walk.

| mode | state reads (`eth_call`, `getBalance`) | `getBlockByNumber` / `getLogs` | valid against |
|---|---|---|---|
| `latest` | block tag `"latest"` | head | any node |
| `recent` (default) | random block within `blockWindow` of head | same window | any node — stays inside the ~128-block non-archive horizon |
| `archive` | **random block over the deepest `archiveDepthPct`% of history** | full-history windows | archive nodes only |

Only the block-tag argument changes, so one engine and one mix serve both products.
`recent` is the default because it is safe everywhere; `archive` must be asked for.

**Capability probe at start, before any load.** Read the head, then attempt one
`eth_getBalance(<addr>, 0x1)` (or the shallowest block the mode will touch):

- probe succeeds → target is archive; proceed.
- probe fails with a state-unavailable error (`missing trie node`, `state not
  available`, Nitro's equivalent) → target is pruned. If `requireArchive` is true
  (default under `archive` selection), **refuse to start** and say so. Never emit an
  error-storm run dressed up as latency percentiles.
- `blockSelection` is `latest`/`recent` → probe is informational only; log what the
  target supports so every result carries it.

Record the detected capability in the run's `EnvironmentSnapshot`, so an archive and a
non-archive result can never be silently compared later.

**Depth sweep gives the boundary as a measurement.** Lowering `archiveDepthPct` walks
the sampling window from deep history toward the head; the depth at which reads stop
failing *is* the node's real horizon. That replaces the assumed "~128 blocks" with a
number, and produces the UC5 customer sentence as evidence.

**Archive validation is DEFERRED — decided 2026-08-13.** The capability ships now
(`blockSelection`, the probe, the depth sweep) but is not exercised in this campaign.
The PoC Nitro chain is deliberately pruned, and its storage nodes have only ~145 GiB
free on the `ssd` VG, so an un-pruned copy — which forgoes the ~87% pruning reclaim —
does not fit alongside the existing chain.

Archive gets tested on the larger servers, in the multi-day runs planned there. That is
the right home for it anyway: archive's defining cost is that the working set is the
whole history, which only becomes visible once history is large, so a short arm on a
small chain would measure the easy case and understate it — the same trap that makes a
12-minute storage arm report 13x write amplification where 4 hours reports 51x.

Default `blockSelection` is `recent`, so nothing here changes what the arms below do.
When archive testing starts, the target must be an archive node: the probe refuses
`archive` selection against a pruned one rather than producing an error storm.

---

## 4. Fix the `adaptive-realistic` trap in the same PR

PRST-4262 found that `PATTERN=adaptive-realistic` silently ignores `txTypeRatios` and
delivers the wrong mix (measured 21,375 gas/tx against 75,700 for the intended 80/20).
The mechanism, verified on this branch, is an **asymmetry between three gates in
`init.go` and one in `workers.go`**:

- `workers.go:39` — tx-type *selection* covers `PatternRealistic` **and**
  `PatternAdaptiveRealistic`.
- `init.go:372` — contract-*deployment* type derivation from `txTypeRatios` is gated on
  `PatternRealistic` **only**. For `adaptive-realistic`, `txTypeForDeploy` stays
  `req.TransactionType` (unset), so **Uniswap V3 is never deployed** while workers still
  select `uniswapSwap` 20% of the time.
- `loadgen.go:420` — `ValidateTxTypeRatios` is likewise gated on `PatternRealistic`
  only. So `adaptive-realistic` accepts a mix that does not sum to 100, and
  `SelectRandomTxType` (`workload.go:43`) falls through to `heavyCompute` for **all
  unallocated probability mass** — a mix summing to 20 silently becomes 80%
  heavy-compute at 500k gas.
- `init.go:483` — `InitRealisticMetrics()` also skipped; cosmetic only, since
  `RecordTxType` creates trackers lazily.

Fix: derive `txTypeForDeploy` and run `ValidateTxTypeRatios` for **both** realistic
patterns. Then `run-arm.sh` rejects the combination up front rather than producing an
incomparable result. Both belong here — the same class of silent-wrong-target failure
is what §3.3 and §5 guard the read path against.

I have **not** reproduced the exact 21,375 gas/tx figure against a live chain. It is
consistent with the intended non-eth types failing to build or landing as calls to an
address with no code, but which of those occurs should be confirmed when the fix is
verified, not assumed.

---

## 5. Failure modes to design against up front

Each mirrors a documented PRST-4262 cycle.

- **`eth_call` against an undeployed contract returns success and measures nothing.**
  Exactly how the write side produced 21,375 gas/tx. If `mix.ethCall > 0` and no
  ERC-20 address is set, **fail the run at start** — never send a read whose target is
  the zero address.
- **Client-side connection-pool starvation.** Nitro's `forwarder.max-idle-connections`
  = 1 exhausted ephemeral ports and cost this campaign its largest single
  misdiagnosis. The read engine adds a *second* concurrent HTTP client from one load
  host: size `MaxIdleConnsPerHost` to read concurrency, and treat any
  `EADDRNOTAVAIL`/`cannot assign requested address` on the generator side as this.
- **Reads that cannot reach `targetRps` must say so.** A silent shortfall reads as a
  server limit. Report offered vs achieved.
- **Read load starting before init completes** hits an empty chain and an undeployed
  contract. Start after `InitPhaseStartingWorkers`.
- **Historical queries in the steady mix** produce errors, not measurements. Deep
  history is UC5's separate one-shot probe.
- **`txpool_status` crashes Nitro's handler** and `txpool_content` always returns empty
  (no mempool). Neither belongs in any read mix.

---

## 6. Experiment plan

Two constraints from PRST-4262 dominate the schedule and are not negotiable:

1. **A ladder cannot run on one chain.** Each ~1500 tx/s arm adds ~4.5 GB of state in
   12 minutes and the sustainable rate is a function of state. Same 1650 target: 1618
   tx/s held on a fresh chain, aborted at 26.4% from 5.5 GB. **Rebuild between rungs**
   (`experiments/fresh-ns2.yaml`).
2. **A short arm cannot answer a storage question.** Write amplification is 13x at
   0→1.8 GiB and 51x at 12→38 GiB. UC1 needs grown state — realistically ~4–6h per arm,
   and the working set must exceed the ARC for `primarycache` to mean anything.

| arm | writes | reads | purpose |
|---|---|---|---|
| R0 | 1500 | `readLoad` absent | **UC6 regression gate.** Must reproduce the `prst4262-7` baseline. Run first; if it does not match, stop |
| R1 | 0 | ladder 500→ceiling | read path alone: reads/s per replica, where p99 breaks, where the RPC tier falls behind the feed |
| R2 | 1500 | 0 / 1000 / 3000 / 6000 | **UC3.** Fresh chain per rung. Does read load move the write ceiling |
| ~~R3a/R3b~~ | 1500 | chosen rate from R2 | **UC1 — demoted.** PRST-4262 settled `primarycache` (`all`; `metadata` halves sustainable state) and attributed the wall to the DC400 drives. Only worth re-running if reads are suspected of changing that margin, and then on the fast drives |
R0–R3 are this campaign. The archive arms below are **deferred to the multi-day runs on
the larger servers** (§3.7) and are listed so the plan is complete, not to be run now.

| ~~R4~~ | — | depth sweep, `blockSelection=archive` | **UC5a, deferred.** Locate a node's real pruning horizon |
| ~~R5a/R5b~~ | 1500 | chosen rate, `archive` selection | **UC5b, deferred.** Archive vs pruned RPC: what archive costs in disk and p99 |

R0 before anything else: it is the cheapest arm and the only one that can invalidate
all the others.

Per-arm capture adds to the PRST-4262 list: read RPS offered vs achieved, per-method
read latency, `readErrors` by method, ARC size/hit-ratio **split by data vs metadata**,
and LB egress bytes.

---

## 7. Acceptance criteria mapping

| ticket criterion | where |
|---|---|
| `readLoad` block, default-off, independent rate | §3.1, §3.2, §3.3 |
| `go build` / `go vet` / `gofmt` / `loadgen`+`transport`+`account` tests pass | implementation — but see §8 |
| new image tag after `prst4262-7` | implementation — `prst4293-1` |
| validated live: read RPS hits target, `eth_call` at expected share, errors ~0 | R1 |
| write-only arm identical to `prst4262-7` | **R0** |
| `run-arm.sh` read-rate parameter + read latency reported | implementation |
| re-run `primarycache` comparison with read load | **R3a/R3b** |
| `adaptive-realistic` guard | §4 (plus the underlying `init.go` fix) |
| *(added to scope)* support archive **and** non-archive RPC targets | §3.7 — capability ships now; archive *testing* deferred to the multi-day runs on the larger servers |

Note for the final write-up: existing PRST-4262 results must be **labelled
write-only**, not left to imply they cover a full RPC workload.

---

## 8. Two facts about the base branch, verified on it

Both were checked directly on `fix/prst-4262-nonce-drift-and-soak-duration` (HEAD
`037bd85`); `go build ./...`, `go vet ./...` and the `loadgen` / `transport` /
`account` test packages all pass there.

**`gofmt -l .` is not empty on this repo, and was not before PR #50.** 36 files are
unformatted: 12 only lack a trailing newline, 24 have real drift (e.g.
`loadgen.go`'s `LoadGenerator` struct field alignment). The same files are unformatted
on `origin/main`, so this is pre-existing repo state, **not** something PR #50
introduced. Consequence for the acceptance criterion: "gofmt passes" cannot mean
`gofmt -l .` is empty, or it fails before a line is written. Scope the check to files
this work touches, and leave the pre-existing drift to its own cleanup rather than
burying the read-load diff under a 36-file reformat.

**PR #50's branch is behind `main`.** `main` is at `132b216`
(`fix(deps): bump golang.org/x/sys to v0.44.0`, PR #51), which is not in this branch.
Per the repo-wide build-hygiene rule, rebase onto `origin/main` **before** building the
`prst4293-1` image — otherwise the image under test diverges from what will merge.
Sequence: rebase PR #50, then stack this work on it.

