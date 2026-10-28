# Research: Redis and Valkey support

Collected 2026-10-28 by a research worker from primary sources (cited); nothing was run against a server during research, and claims it could not verify are marked "unverified". The design dbscope adopted is in docs/ENGINES.md.

Collected 2026-10-28 from primary sources, cited inline. Claims without a primary source are marked "unverified". Lines marked **design** are proposals for dbscope, not facts about Redis or Valkey. Content from the cited pages was rephrased for compliance with licensing restrictions.

**Versions seen on the research date:**
- **Redis Open Source:** the docs cover up to 8.10 ([what's new](https://redis.io/docs/latest/develop/whats-new/)). go-redis lists 8.0 through 8.10 as supported ([go-redis README](https://github.com/redis/go-redis/blob/master/README.md)).
- **Valkey:** the newest GitHub release is 9.1.3 ([releases API](https://api.github.com/repos/valkey-io/valkey/releases?per_page=8)). Later 9.x changes are noted where they affect dbscope.
- **go-redis:** v9.23.0 ([release](https://github.com/redis/go-redis/releases/tag/v9.23.0)).
- **valkey-go:** v1.0.79, published 2026-10-15 ([release](https://github.com/valkey-io/valkey-go/releases/tag/v1.0.79)).

**These findings matter most for dbscope's design:**
- **Most cheap metadata needs no key scan.** `INFO keyspace` reports `keys`, `expires` and `avg_ttl` per database. `INFO memory` and `INFO stats` give memory, policy and eviction counters ([INFO](https://redis.io/docs/latest/commands/info/)).
- **The key scan must never read values.** dbscope needs only SCAN, TYPE, PTTL, the O(1) length commands, OBJECT and MEMORY USAGE with the default sample count. `+@read` would also grant KEYS, HGETALL, SMEMBERS and GET, so the ACL user has to be an explicit allowlist (3.6).
- **Valkey reports `redis_version:7.2.4`.** Feature detection has to read `server_name` and `valkey_version` ([Valkey INFO](https://valkey.io/commands/info/)).
- **Hot keys have three different sources.** `OBJECT FREQ` works only under an LFU policy. Redis 8.6+ has `HOTKEYS`, which needs `@admin`. Valkey appears to have no `HOTKEYS` command.
- **In cluster mode, SCAN, DBSIZE and INFO are per node.** Valkey 9.1 adds `CLUSTERSCAN`.

### 1. Safe keyspace iteration

#### 1.1 SCAN semantics ([SCAN](https://redis.io/docs/latest/commands/scan/), [Valkey SCAN](https://valkey.io/commands/scan/))

| Property | What the docs say |
|---|---|
| Complexity | O(1) per call. O(N) for a full iteration, which is the run of calls until the cursor returns to 0. |
| ACL categories | `@keyspace @read @slow` |
| Completeness | Returns every element present for the whole iteration. |
| No phantoms | Never returns an element that was absent for the whole iteration. |
| Duplicates | Possible. The client must deduplicate. |
| Churn | Elements added or removed mid-iteration may or may not appear. |
| Per-call size | Not guaranteed. A call may return zero keys, so only cursor 0 means done. |
| COUNT | A hint for work per call. The default is 10, and it can change between calls. |
| MATCH and TYPE | Applied after fetching, so they do not reduce server work. |
| State | No server state. Parallel iterations and abandoning a scan halfway are safe. |
| Termination | Guaranteed only if the collection stays within a bounded size. |
| TYPE quirk | Geo, HyperLogLog and bitmap keys report as zset or string. |

- **Single-slot patterns.** In cluster mode, a pattern like `{abc}*` scans only one slot. This optimization arrived in Redis 8.0 ([cluster spec](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/)).
- **Accumulation bias.** Valkey warns about this effect. If a client deletes scanned keys while another writer refills the collection, buckets can become skewed. If the client also restarts from cursor 0 after errors, a single call can return far more than COUNT. Resume from the last cursor instead ([Valkey SCAN](https://valkey.io/commands/scan/)).
- **`hash-seed`.** Valkey 9.1 adds this startup setting. If all nodes share it, SCAN order stays the same across nodes, so a scan can resume after a failover ([Valkey SCAN](https://valkey.io/commands/scan/)).
- **Small collections.** SSCAN, HSCAN and ZSCAN return small listpack or intset collections in one call. Top-level SCAN never does this.

#### 1.2 Per-key metadata

| Command | Complexity | ACL | Notes |
|---|---|---|---|
| TYPE | O(1) | `@keyspace @read @fast` | string, list, set, zset, hash, stream, vectorset ([TYPE](https://redis.io/docs/latest/commands/type/)). Module types return names such as `ReJSON-RL`, `MBbloomCF` and `TDIS-TYPE` ([redis-cli docs](https://redis.io/docs/latest/develop/tools/cli/)). |
| TTL / PTTL | O(1) | `@keyspace @read @fast` | -2 means missing, -1 means no expiry ([TTL](https://redis.io/docs/latest/commands/ttl/)) |
| OBJECT ENCODING | O(1) | `@keyspace @read @slow` | raw, int, embstr (up to 44 bytes), listpack, quicklist, intset, hashtable, skiplist, stream ([doc](https://redis.io/docs/latest/commands/object-encoding/)) |
| OBJECT FREQ | O(1) | `@keyspace @read @slow` | Only under LFU policies ([doc](https://redis.io/docs/latest/commands/object-freq/), [Valkey](https://valkey.io/commands/object-freq/)) |
| OBJECT IDLETIME | O(1) | `@keyspace @read @slow` | Only when the policy is not LFU ([doc](https://redis.io/docs/latest/commands/object-idletime/)) |
| MEMORY USAGE [SAMPLES n] | O(samples) | `@read @slow` | Averages nested samples, default 5. `SAMPLES 0` samples everything. 8.10+ adds each key's share of compact hash templates ([doc](https://redis.io/docs/latest/commands/memory-usage/), [Valkey](https://valkey.io/commands/memory-usage/)). |

- **MEMORY USAGE accuracy.** The 5-sample result for aggregates is an estimate, and no error bound is documented (unverified). `SAMPLES 0` costs O(elements) on a single key.
- **Worked sizes.** On Redis 7.2 64-bit jemalloc, an empty string key measures 56 bytes and a 100-byte value measures 160 bytes ([doc](https://redis.io/docs/latest/commands/memory-usage/)).
- **CLIENT NO-TOUCH ON.** This command (7.2.0+, `@slow @connection`) stops LRU and LFU updates for the connection ([doc](https://redis.io/docs/latest/commands/client-no-touch/)). redis-cli sends it before inspecting keys and ignores the error on older servers ([redis-cli.c](https://github.com/redis/redis/blob/unstable/src/redis-cli.c), unstable branch). Whether TYPE, PTTL or OBJECT update LRU or LFU without it is unverified.

#### 1.3 Why KEYS, DEBUG and full reads are dangerous

- **KEYS** is O(N) in one blocking call and is in `@dangerous`. The docs warn it can ruin performance on large databases. Their example is 1M keys in about 40 ms on a laptop ([KEYS](https://redis.io/docs/latest/commands/keys/)).
- **DEBUG** is an internal command (`@admin @slow @dangerous`) ([DEBUG](https://redis.io/docs/latest/commands/debug/)). redis.conf blocks it by default with `enable-debug-command no` ([redis.conf](https://github.com/redis/redis/blob/unstable/redis.conf), unstable; the version that added this default is unverified). The docs use `DEBUG SLEEP` to create latency spikes ([LATENCY LATEST](https://redis.io/docs/latest/commands/latency-latest/)). The cost of `DEBUG OBJECT` is unverified.
- **SMEMBERS and HGETALL** are O(N) ([SMEMBERS](https://redis.io/docs/latest/commands/smembers/), [HGETALL](https://redis.io/docs/latest/commands/hgetall/)). The SCAN page says commands like these can block for seconds on big collections.
- **Full `--bigkeys` passes** never block on a single call, but they add load in proportion to keyspace size. A vendor recommends off-peak runs ([Tencent Cloud](https://intl.cloud.tencent.com/document/product/239/54756), practice).

#### 1.4 Bounding impact

- **redis-cli throttle.** `-i <sec>` sleeps after every 100 SCAN calls in the scan modes. The docs say `-i 0.01` makes server load negligible ([redis-cli](https://redis.io/docs/latest/develop/tools/cli/)).
- **redis-cli batching.** Each batch is SCAN, then pipelined TYPE, then pipelined size commands or MEMORY USAGE ([redis-cli.c](https://github.com/redis/redis/blob/unstable/src/redis-cli.c)).
- **Signals to watch.** `instantaneous_ops_per_sec`, `INFO latencystats` (on by default) and `LATENCY LATEST`. `latency-monitor-threshold` defaults to 0 (off) ([redis.conf](https://github.com/redis/redis/blob/unstable/redis.conf)).
- **Design.**
  - Use one connection per node and sequential batches, with SCAN COUNT 100.
  - Pipeline TYPE, PTTL and MEMORY USAGE per page, under a command-rate cap.
  - Back off when round-trip time rises above the PING baseline.
  - Enforce time and key budgets, and report partial results with the cursor reached.
  - Whether a partial SCAN is a fair random sample is unverified. Extrapolate with a caveat, and use DBSIZE or INFO for exact counts.

### 2. Big and hot keys

#### 2.1 redis-cli modes ([docs](https://redis.io/docs/latest/develop/tools/cli/), [redis-cli.c](https://github.com/redis/redis/blob/unstable/src/redis-cli.c))

| Mode | Measures | How |
|---|---|---|
| `--bigkeys` | Element count (bytes for strings) | SCAN, TYPE, then the length command. Module types show `0 ?`. |
| `--memkeys` | Bytes | SCAN, TYPE, then MEMORY USAGE. `--memkeys-samples` sets SAMPLES. |
| `--keystats` | Both, plus percentiles and top-N | `--cursor` resumes and `--top` sets N. Present in the Redis docs, not on the Valkey CLI page. |
| `--hotkeys` | LFU counter | SCAN, then OBJECT FREQ. Only works under `*lfu` policies. Valkey keeps the top 16 by default ([Valkey CLI](https://valkey.io/topics/cli/)). |

- **Options and replicas.** The default `--count` is 10. redis-cli sends `READONLY`, so the modes run on cluster replicas.
- **Elements are not memory.** `--bigkeys` counts elements, not bytes ([Tencent](https://intl.cloud.tencent.com/document/product/239/54756)).

#### 2.2 Length commands

STRLEN, LLEN, HLEN, SCARD, ZCARD and XLEN are all O(1), `@read @<type> @fast` ([STRLEN](https://redis.io/docs/latest/commands/strlen/), [LLEN](https://redis.io/docs/latest/commands/llen/), [HLEN](https://redis.io/docs/latest/commands/hlen/), [SCARD](https://redis.io/docs/latest/commands/scard/), [ZCARD](https://redis.io/docs/latest/commands/zcard/), [XLEN](https://redis.io/docs/latest/commands/xlen/)).
- **Zero results.** Each returns 0 for a missing key. XLEN can also return 0 for a stream that exists.
- **Type changes.** A key can change type between TYPE and the length call. redis-cli treats this as size 0.
- **New types.** Length commands for vector sets and the 8.8 array type are unverified.

#### 2.3 Hot keys

- **LFU path.** `OBJECT FREQ` needs `allkeys-lfu` or `volatile-lfu`. Changing the policy changes eviction order ([Tencent](https://intl.cloud.tencent.com/document/product/239/54756)), so dbscope must never change it.
- **Redis 8.6+ HOTKEYS.** `HOTKEYS START METRICS n [CPU] [NET] [COUNT k] [DURATION s] [SAMPLE ratio] [SLOTS ...]` tracks keys by CPU-time share and network-byte share in a probabilistic top-K. Only one session runs at a time. START and GET are `@admin @slow @dangerous`, STOP keeps the data and RESET frees it ([HOTKEYS](https://redis.io/docs/latest/commands/hotkeys/), [START](https://redis.io/docs/latest/commands/hotkeys-start/), [GET](https://redis.io/docs/latest/commands/hotkeys-get/), [STOP](https://redis.io/docs/latest/commands/hotkeys-stop/)).
- **Cluster clients.** The go-redis and redis-py cluster clients do not support HOTKEYS, so connect to each node directly.
- **Valkey.** The [hotkeys page](https://valkey.io/commands/hotkeys/) returns 404, so the command appears not to exist. Valkey 8.1+ `COMMANDLOG GET n large-reply` gives a partial signal ([COMMANDLOG GET](https://valkey.io/commands/commandlog-get/)).
- **Fallback.** Without LFU or HOTKEYS, report hot keys as "not available".

### 3. Server introspection

#### 3.1 INFO (O(1), `@slow @dangerous`) ([Redis](https://redis.io/docs/latest/commands/info/), [Valkey](https://valkey.io/commands/info/))

| Section | Fields |
|---|---|
| server | `redis_version`, `redis_mode`. Valkey adds `server_name`, `valkey_version` and `valkey_release_stage` (8.1+). |
| memory | `used_memory`, `used_memory_rss`, `used_memory_dataset`, `used_memory_overhead`, `maxmemory`, `maxmemory_policy`, `mem_fragmentation_ratio` (RSS/used, including process overhead), `mem_fragmentation_bytes`, `allocator_frag_ratio` and `allocator_frag_bytes` (the docs call these the true fragmentation metric), `mem_not_counted_for_evict`, `lazyfree_pending_objects` |
| keyspace | `dbN:keys,expires,avg_ttl,subexpiry` (when `subexpiry` was added is unverified) |
| keysizes | Base-2 histograms of item counts per type. Redis 8.6 adds memory histograms via `key-memory-histograms` ([8.6](https://redis.io/docs/latest/develop/whats-new/8-6/)). The first version with this section is unverified, and Valkey does not list it. |
| stats | `evicted_keys`, `expired_keys`, `keyspace_hits/misses`, `instantaneous_ops_per_sec`, `latest_fork_usec`, `total_eviction_exceeded_time`, `current_eviction_exceeded_time`. Redis 8.8 adds slowlog counters. |
| persistence | `rdb_last_bgsave_status`, `rdb_changes_since_last_save`, `rdb_last_save_time`, `aof_enabled`, `aof_last_write_status`, `aof_last_bgrewrite_status` |
| replication | `role`, `connected_slaves`, `master_link_status`, `master_last_io_seconds_ago`, `slave_read_only` |
| commandstats | `calls`, `usec`, `usec_per_call`, `rejected_calls`, `failed_calls`. Redis 8.8 adds `slowlog_count/time_ms_sum/max`. |
| latencystats | p50, p99 and p99.9 per command |
| cluster | `cluster_enabled` |

- **Parsing.** Skip unknown fields and tolerate missing ones.
- **Fragmentation guidance.** A ratio of 1.5 or more is not a problem when the bytes are only a few MB. Used memory far above RSS means swapping and latency.

#### 3.2 CONFIG GET (`@admin @slow @dangerous`, O(params)) ([doc](https://redis.io/docs/latest/commands/config-get/), [Valkey](https://valkey.io/commands/config-get/), which sorts the reply since 9.0)

| Setting | Default |
|---|---|
| `maxmemory` | 0, meaning unlimited on 64-bit ([eviction](https://redis.io/docs/latest/develop/reference/eviction/)) |
| `maxmemory-policy` | noeviction ([redis.conf](https://github.com/redis/redis/blob/unstable/redis.conf)) |
| `save` | `3600 1 300 100 60 10000` |
| `stop-writes-on-bgsave-error` | yes |
| `appendonly`, `appendfsync` | no; everysec ([persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)) |
| `lazyfree-lazy-eviction/expire/server-del/user-del/user-flush`, `replica-lazy-flush` | Redis: no. Valkey 8.0+: yes ([valkey.conf](https://github.com/valkey-io/valkey/blob/unstable/valkey.conf)). |
| `slowlog-log-slower-than`, `slowlog-max-len` | 10000 µs; 128 |
| `latency-monitor-threshold` | 0 |
| `databases` | redis-cli falls back to 16 |

- **Design.** Request named parameters only. Whether CONFIG GET reveals `requirepass` or `masterauth` is unverified. ACL cannot limit which parameters are read.
- **Defaults source.** These defaults come from the unstable branch. Release-branch defaults are unverified.

#### 3.3 Slow log, latency, clients

| Command | ACL |
|---|---|
| SLOWLOG GET [n] (O(n)) | `@admin @slow @dangerous`. Fields: ID, timestamp, µs, args, client address, client name, plus the arg count in 8.10+. Args are truncated past 32 ([doc](https://redis.io/docs/latest/commands/slowlog-get/)). The doc calls the setting `slowlog-max-argc` and redis.conf calls it `slowlog-entry-max-argc`; which name is right is unverified. |
| COMMANDLOG GET n slow/large-request/large-reply | `@admin @dangerous @slow`, Valkey 8.1+ |
| LATENCY DOCTOR / LATEST | `@admin @slow @dangerous` ([DOCTOR](https://redis.io/docs/latest/commands/latency-doctor/), [LATEST](https://redis.io/docs/latest/commands/latency-latest/)) |
| CLIENT LIST | `@admin @slow @dangerous @connection` ([doc](https://redis.io/docs/latest/commands/client-list/)) |
| MEMORY STATS / DOCTOR | `@slow` ([STATS](https://redis.io/docs/latest/commands/memory-stats/), [DOCTOR](https://redis.io/docs/latest/commands/memory-doctor/)) |
| DBSIZE | `@keyspace @read @fast` ([doc](https://redis.io/docs/latest/commands/dbsize/)) |
| CLUSTER NODES / SHARDS | `@slow`. SHARDS (7.0+) is the documented choice for routing ([NODES](https://redis.io/docs/latest/commands/cluster-nodes/), [SHARDS](https://redis.io/docs/latest/commands/cluster-shards/)). |

**Design.** Log entries contain arguments, which may include key names and values. Redact arguments by default.

#### 3.4 ACL categories ([ACL](https://redis.io/docs/latest/operate/oss_and_stack/management/security/acl/))

- **@read and @write** cover reading and writing keys, values and metadata. Commands that do not touch keys have neither.
- **@keyspace** covers type-agnostic key commands, including DEL, KEYS, SCAN, TTL and FLUSHALL. Commands that modify data also carry @write.
- **@admin** covers CONFIG, DEBUG, SAVE, MONITOR, ACL, SHUTDOWN and others.
- **@dangerous** covers FLUSHALL, KEYS, CLIENT, DEBUG, INFO, CONFIG, SORT, RESTORE and others.
- **@slow** is every command that is not @fast.
- **Redis 8.0 breaking change.** Module commands now belong to the standard categories, and new categories were added (json, search, bloom, etc.) ([8.0](https://redis.io/docs/latest/develop/whats-new/8-0/)).
- **Subcommands.** Since 7.0, `+cmd|sub` works for every command except DEBUG.
- **Key permissions.** `%R~` grants read only, and R excludes metadata such as STRLEN and TYPE. Commands without key flags still need R or W, so a metadata-only user needs `%R~*`.
- **Keyless commands.** FLUSHALL, FLUSHDB and SWAPDB ignore key patterns.
- **AUTH** is always allowed. Whether HELLO needs a grant is unverified.
- **Valkey 9.1 per-DB ACL.** Valkey 9.1 adds `db=` rules according to [AWS](https://docs.aws.amazon.com/AmazonElastiCache/latest/mem-ug/engine-versions.html), which is a secondary source.

#### 3.5 Read-only access

Several needed commands are in @admin or @dangerous: INFO, CONFIG GET, SLOWLOG, LATENCY and COMMANDLOG. A rule like `+@read -@dangerous` drops these and still allows GET and HGETALL.

#### 3.6 Proposed dbscope user (design)

```
ACL SETUSER dbscope on #<sha256-of-password> resetkeys %R~* resetchannels -@all
  +ping +hello +info +dbsize +scan +type +ttl +pttl
  +strlen +llen +hlen +scard +zcard +xlen
  +object|encoding +object|freq +object|idletime
  +memory|usage +memory|stats +memory|doctor
  +config|get +slowlog|get +slowlog|len +latency|doctor +latency|latest
  +client|no-touch +client|setinfo +client|setname
  +cluster|nodes +cluster|shards +cluster|info +readonly
```

- **Optional grants.** Add `+commandlog|get` on Valkey 8.1+. `+client|list` is optional. HOTKEYS stays out by default.
- **Password.** `#<sha256>` keeps the clear-text password out of the ACL file.
- **Unverified grants.** Whether `+hello` and `+readonly` are strictly needed is unverified. Also verify that `ACL DRYRUN` rejects GET for this user; the DRYRUN syntax itself is unverified.

### 4. Risk findings

**4.1 Memory and eviction** ([eviction](https://redis.io/docs/latest/develop/reference/eviction/), [redis.conf](https://github.com/redis/redis/blob/unstable/redis.conf))
- **maxmemory 0.** Memory is unlimited, and what the OS does at exhaustion is unverified.
- **noeviction at the limit.** Writes that need memory get errors, and reads still work. On replicated setups this applies only to the primary.
- **volatile-\* with few TTL keys.** These policies act like noeviction when no keys have a TTL. The evidence is `expires` far below `keys`, plus the scan's per-pattern share of `PTTL = -1`.
- **allkeys-\*.** Keys without a TTL can be evicted, which is a risk if they are primary data (design judgment).
- **LRM policies.** Redis 8.6 adds `allkeys-lrm` and `volatile-lrm`.
- **Pressure signals.** `evicted_keys` growing or `current_eviction_exceeded_time` above 0. The hit ratio is hits / (hits + misses).
- **Headroom.** Leave room for buffers measured by `mem_not_counted_for_evict`.
- **Replicas** ignore maxmemory by default since Redis 5.

**4.2 Big keys**
- **Vendor thresholds (practice).** Tencent flags a string over 10 MB, or a collection over 10,000 elements or 100 MB. At design time it recommends at most 10 KB per string and 5,000 elements per collection ([Tencent](https://intl.cloud.tencent.com/document/product/239/54756)).
- **Alibaba.** Alibaba DAS uses a default of 2,000 elements, based on a search snippet only ([DAS](http://help.aliyun.com/en/das/user-guide/key-analysis/)).
- **Redis itself** sets no threshold.
- **DEL cost.** DEL on a collection is O(M) and blocks ([DEL](https://redis.io/docs/latest/commands/del/)). UNLINK frees in a background thread ([UNLINK](https://redis.io/docs/latest/commands/unlink/)).
- **Implicit deletes.** Expiry and eviction of big keys block unless `lazyfree-*` is on.
- **Resharding.** Resharding with big keys hurts latency ([cluster spec](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/)).

**4.3 Dangerous commands in use** (from commandstats and the slow log or command log)
- **KEYS.** Any `cmdstat_keys` calls are worth reporting.
- **FLUSHALL / FLUSHDB.** These run synchronously unless ASYNC or `lazyfree-lazy-user-flush` is set, and FLUSHALL also clears the RDB file ([FLUSHALL](https://redis.io/docs/latest/commands/flushall/)).
- **Slow DEL.** DEL with a high `usec_per_call` while `lazyfree-lazy-user-del` is no.
- **Full-collection reads.** HGETALL, SMEMBERS, LRANGE or ZRANGE in the slow log, or a Valkey large-reply entry.
- **Admin commands.** DEBUG, MONITOR, SAVE and CONFIG SET, reported as information.

**4.4 Persistence** ([persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/))
- **No persistence.** `save ""` with `appendonly no` loses all data on restart. Report this as information for caches.
- **Failed BGSAVE.** A failed BGSAVE with `stop-writes-on-bgsave-error yes` makes the server reject writes.
- **AOF errors.** Watch `aof_last_write_status` and `aof_last_bgrewrite_status`.
- **appendfsync.** `always` is very slow. `no` leaves flushing to the OS, about every 30 s on Linux. `everysec` can lose up to 1 s.
- **Loss window.** A large `rdb_changes_since_last_save` means a large window of possible data loss.

**4.5 Fragmentation** ([INFO](https://redis.io/docs/latest/commands/info/))
- **Which metric.** Judge by `allocator_frag_ratio` plus bytes, not the raw ratio.
- **Small instances.** Ignore high ratios when only a few MB are involved.
- **Swap.** A ratio below 1 suggests swapping and is high severity.
- **Threshold.** A numeric threshold is unverified, so make it configurable.

**4.6 Cluster and replicas**
- **Slots.** 16384 slots, computed as CRC16 mod 16384, with `{tag}` hash tags ([spec](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/)).
- **Per-shard commands.** SCAN, KEYS and DBSIZE cover only the current shard ([multi-key](https://redis.io/docs/latest/develop/using-commands/multi-key-operations/)).
- **go-redis.** It keeps SCAN and INFO node-local, and its `ForEachMaster` runs concurrently ([osscluster.go](https://github.com/redis/go-redis/blob/master/osscluster.go)). **Design:** visit nodes one at a time.
- **Replica reads.** `READONLY` allows stale reads on replicas. Whether replica LFU and IDLETIME reflect primary traffic is unverified.
- **Valkey.** 9.0+ allows numbered DBs in cluster mode. 9.1 adds CLUSTERSCAN (`@keyspace @read @slow`), which steps through slots, follows MOVED and has a SLOT option ([CLUSTERSCAN](https://valkey.io/commands/clusterscan/)).
- **Imbalance.** Compare `used_memory` and `DBSIZE` across primaries.

### 5. Version differences

| Area | Redis 7.x | Redis 8.x | Valkey 8.x / 9.x |
|---|---|---|---|
| License | ≤7.2 BSD-3. 7.4 to 7.8 RSALv2/SSPLv1 ([licenses](https://redis.io/legal/licenses/)) | RSALv2, SSPLv1 or AGPLv3 | BSD-3 ([COPYING](https://github.com/valkey-io/valkey/blob/unstable/COPYING)) |
| Detection | `redis_version` | `redis_version` | `server_name`, `valkey_version`. `redis_version` is fixed at 7.2.4. |
| Types | Stack modules separate | Bundled Search, JSON, TimeSeries, probabilistic. `vectorset` (beta). Array in 8.8. | Modules separate. Bloom and search per AWS (OSS status unverified). |
| ACL | No module categories | Module commands in standard categories (breaking) | Same as Redis 7.2 for core (unverified). Per-DB ACL in 9.1 (AWS). |
| Eviction | 8 policies | +LRM in 8.6 | LRM not found (unverified) |
| Hot keys | LFU only | HOTKEYS in 8.6 | LFU only. No HOTKEYS page. |
| Key stats | None | keysizes (start version unverified). Memory histograms in 8.6. | Not listed |
| Slow log | SLOWLOG | +stats 8.8, +argc 8.10 | +COMMANDLOG 8.1 |
| Lazy free | no | no | yes since 8.0 |
| Hash field TTL | 7.4 | 8.0 adds HGETEX/HSETEX/HGETDEL | 9.0 |
| Memory per key | Baseline | 8.6 hash/zset savings. 8.10 compact hashes. | 8.1 hashtable saves ~20 to 30 B per key ([blog](https://valkey.io/blog/valkey-8-1-0-ga/)) |
| Cluster scan | Per node | Per node. Single-slot MATCH in 8.0. | Per node. DBs in 9.0. CLUSTERSCAN in 9.1. |

**Design.** Show the server version next to every memory figure, and store encodings as opaque strings.

### 6. Go client

**go-redis v9.23.0**
- **Go version.** go.mod says `go 1.26.0`, which matches dbscope ([go.mod](https://github.com/redis/go-redis/blob/master/go.mod)).
- **Dependencies.** xxhash, xxh3, atomic, x/sys and cpuid. No cgo dependency appears, but I did not build with `CGO_ENABLED=0` (unverified).
- **License.** BSD-2-Clause ([licenses](https://redis.io/legal/licenses/)).
- **Server support.** Officially Redis 8.0 to 8.10. The README says it should work with Redis 7.0+.
- **Protocol.** `Protocol` 2 or 3, default 3.
- **Auth and TLS.** `Username`/`Password` and `TLSConfig`. A `rediss://` URL sets TLS 1.2 minimum ([options.go](https://github.com/redis/go-redis/blob/master/options.go)).
- **Cluster.** ClusterClient supports ReadOnly, RouteByLatency and RouteRandomly.
- **Sentinel.** `NewFailoverClient` and `NewUniversalClient` ([pkg.go.dev](https://pkg.go.dev/github.com/redis/go-redis/v9)).
- **Connect side effects.** go-redis sends `CLIENT SETINFO` on connect and ignores failure; `DisableIdentity` turns it off. A `client maint_notifications` call at connect appears in the pkg.go.dev example; its effect under a restricted ACL is unverified.
- **Per-connection setup.** The `OnConnect` hook can send `CLIENT NO-TOUCH ON`.
- **Valkey.** No compatibility statement found. HOTKEYS does not work through the cluster client.

**valkey-go v1.0.79**
- **Go version.** go.mod says `go 1.25.0` ([go.mod](https://github.com/valkey-io/valkey-go/blob/main/go.mod)).
- **Protocol.** Tries RESP3 first, and `AlwaysRESP2` forces RESP2.
- **Options.** `ClientNoTouch`, `ReplicaOnly` and `ForceSingleClient` ([valkey.go](https://github.com/valkey-io/valkey-go/blob/main/valkey.go)).
- **Topology.** Supports cluster and Sentinel. A `ClusterScanner` helper handles CLUSTERSCAN.
- **API.** A `valkeycompat` adapter gives a go-redis-like API.
- **License and CGO.** Unverified.

**Recommendation (design).** Use go-redis v9 for both engines, with explicit pipelines and NO-TOUCH sent in `OnConnect`. Send CLUSTERSCAN and HOTKEYS as raw commands on single-node clients. valkey-go is the fallback if Valkey-specific helpers become important.

### Facts dbscope must encode

- [ ] SCAN only. Deduplicate, and treat only cursor 0 as done. Resume from the last cursor after errors.
- [ ] MATCH and TYPE do not reduce server work.
- [ ] Never send KEYS, DEBUG, MONITOR, FLUSH\*, CONFIG SET, `SAMPLES 0`, or any value-returning command.
- [ ] Per key: TYPE, PTTL, an O(1) length command, and MEMORY USAGE with the default samples. Treat a type change between calls as size 0.
- [ ] Record unknown TYPE names, with size "unknown".
- [ ] PTTL: -1 means no TTL, -2 means the key is gone.
- [ ] OBJECT FREQ needs LFU. OBJECT IDLETIME needs a non-LFU policy.
- [ ] Send `CLIENT NO-TOUCH ON` on 7.2+ and ignore an error on older servers.
- [ ] One connection per node, sequential batches, a rate cap, back-off on rising RTT, and partial results with the cursor reached.
- [ ] Take exact key and expiry counts from `INFO keyspace`.
- [ ] Detect Valkey by `server_name` and `valkey_version`.
- [ ] Parse INFO leniently.
- [ ] Encode the findings in section 4.
- [ ] Read lazyfree settings and do not assume them. The defaults differ between Redis and Valkey.
- [ ] Cluster: discover with CLUSTER SHARDS and visit each primary or replica in turn. On Valkey 9.0+, cover every DB. CLUSTERSCAN is an option on 9.1+.
- [ ] Hot keys: LFU, or HOTKEYS only with a user grant. Never change the policy.
- [ ] Redact log arguments.
- [ ] Use named CONFIG GET parameters only.
- [ ] Ship the allowlist ACL user from 3.6.
- [ ] Show the server version next to memory figures.
