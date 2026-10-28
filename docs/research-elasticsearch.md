# Research: Elasticsearch and OpenSearch support

Collected 2026-10-28 by a research worker from primary sources (cited); nothing was run against a server during research, and claims it could not verify are marked "unverified". The design dbscope adopted, and what testing showed, is in docs/ENGINES.md.

**Versions on 2026-10-28**
- **Elasticsearch 9.x:** the Docker install page uses `docker.elastic.co/elasticsearch/elasticsearch:9.5.5` as "latest" ([es docker 9][es-docker9]).
- **Elasticsearch 8.x:** the 8.19 Docker page uses `8.19.23` ([es docker 8][es-docker8]).
- **OpenSearch 3.x:** the Docker page uses `opensearchproject/opensearch:3.9.0` as its example of a specific version ([os docker][os-docker]). The newest 2.x tag is unverified.
- **go-elasticsearch:** `v9.5.2` (2026-09-01) and `v8.19.7` (2026-08-04) ([proxy v9][proxy-es9], [proxy v8][proxy-es8]).
- **opensearch-go:** a `v5` module exists at `v5.1.3` (2026-10-26), and `v4` is at `v4.8.1` (2026-10-09) ([proxy os v5][proxy-os5], [proxy os v4][proxy-os4]). The user guide on `main` already shows `/v5` imports ([os user guide][os-guide]). The task named v4, so v5 is a new major version to account for.

**What matters most for the design**
- **The Elasticsearch Go client refuses OpenSearch.** It checks that the `X-Elastic-Product` response header equals `Elasticsearch`. Otherwise it errors with "the client noticed that the server is not Elasticsearch" ([es client source][es-client-src]). One official client cannot serve both servers.
- **Plain `net/http` looks like the simplest single code path.** dbscope needs only about a dozen read endpoints. SigV4 signing is available from `github.com/aws/aws-sdk-go-v2/aws/signer/v4` (`Signer.SignHTTP`) ([sigv4][sigv4]). dbscope's `go.mod` already requires `aws-sdk-go-v2 v1.47.3`.
- **Server-side read-only comes from a role, not a client mode.**
  - Elasticsearch: index privileges `read`, `view_index_metadata` and `monitor`, plus the cluster privilege `monitor` ([es privileges][es-priv]).
  - OpenSearch: the built-in role `readall_and_monitor`, or a custom role ([os roles][os-roles]).
- **Defaults differ between the two products.**
  - `index.mapping.nested_fields.limit` is 100 on Elasticsearch ([es mapping limits][es-maplimit]) and 50 on OpenSearch ([os mapping explosion][os-mapexp]).
  - OpenSearch has `dynamic` modes that Elasticsearch lacks: `false_allow_templates` and `strict_allow_templates`.
  - Elasticsearch has a `runtime` mode, which the OpenSearch page does not list ([es dynamic][es-dynamic], [os dynamic][os-dynamic]).
- **Elasticsearch search has no timeout by default.** The `timeout` parameter applies per shard. `search.default_search_timeout` is a cluster-wide fallback. Elasticsearch cancels a search when the client's HTTP connection closes ([es search api][es-search-guide]). So dbscope should send `timeout` and also cancel on the client side.
- **Both SQL engines can show the query without running it.** Elasticsearch has `POST /_sql/translate` ([es sql translate][es-sql-translate]). OpenSearch has `POST _plugins/_sql/_explain` and `POST _plugins/_ppl/_explain` ([os sql api][os-sql]).

## 1. Catalog APIs

| API | What it returns (fields dbscope needs) | Source |
|---|---|---|
| `GET /` | OpenSearch: `version.distribution: "opensearch"`, `version.number`, `tagline`. Elasticsearch: responses carry `X-Elastic-Product: Elasticsearch`, which its client checks. | [os docker][os-docker], [es client source][es-client-src] |
| `GET _cat/indices?format=json&h=...` | Columns include `health`, `status`, `index`, `uuid`, `pri`, `rep`, `docs.count`, `docs.deleted`, `creation.date`, `store.size`, `pri.store.size`, `dataset.size`. Here `expand_wildcards` defaults to `all`; most other APIs default to `open`. | [es cat indices][es-cat-indices] |
| `_cat/*` in general | Elastic says the cat APIs are for humans only. Applications should use the matching JSON API. | [es cat][es-cat] |
| `GET _resolve/index/<pattern>` | `indices[]` (`name`, `aliases`, `attributes`, `data_stream`, `mode`), `aliases[]` (`name`, `indices`), `data_streams[]` (`name`, `timestamp_field`, `backing_indices`). | [es resolve][es-resolve] |
| `GET _data_stream/<name>` | `data_streams[]` with `generation`, `hidden`, `ilm_policy`, `next_generation_managed_by`, and `indices[]` (`index_name`, `index_uuid`, `managed_by`, `index_mode`). The last entry in `indices` is the current write index. | [es data stream][es-ds] |
| `GET <index>/_mapping` | Shape: `{ "<index>": { "mappings": { "dynamic"?, "properties": {...}, "runtime"? } } }`. OpenSearch returns `dynamic` as a string (`"true"`). | [es types removal][es-types], [os dynamic][os-dynamic], [es mapping limits][es-maplimit] |
| `GET <pattern>/_field_caps?fields=*` | `indices[]`, then `fields.<name>.<type>` with `searchable`, `aggregatable`, `indices`, `non_searchable_indices`, `non_aggregatable_indices`, `meta`. | [es field caps][es-fieldcaps] |
| `GET _cluster/health` | `status`, `active_primary_shards`, `active_shards`, `active_shards_percent_as_number`, plus `wait_for_*` parameters. `timeout` defaults to 30s. | [es health][es-health] |
| `GET <index>/_stats` | Index stats. Elastic shows `level=shards` with `filter_path` for checking per-shard doc counts. | [es shard sizing][es-shards] |
| `GET _cluster/stats` | `indices.mappings.total_deduplicated_mapping_size*`, `indices.shards.total`. | [es shard sizing][es-shards] |

- **Field type conflicts.** In the field caps response, each field object is keyed by type. Based on the schema, a field with more than one type key is a conflict. The `indices` list under each type says which indices use that type ([es field caps][es-fieldcaps]).
- **OpenSearch field caps.** The API exists: its permission list includes `indices:data/read/field_caps` ([os permissions][os-perms]). Whether its response shape matches Elasticsearch's is unverified.
- **Cost on big clusters.** I found no primary source that ranks these APIs by cost. Unverified. **Design:** use `filter_path` and explicit `h=` columns.
- **Mapping types.** Elasticsearch said types would be removed from responses, and `include_type_name` removed, in 8.0 ([es types removal][es-types]). OpenSearch says the `type` parameter was removed from all endpoints ([os breaking][os-breaking]). That the removal happened in OpenSearch 2.0 comes only from a forum quote of the release notes, so the exact version is unverified.
- **flattened vs flat_object.**
  - Elasticsearch `flattened` has `depth_limit` (default 20), `ignore_above`, and typed `properties` sub-fields ([es flattened][es-flattened]).
  - OpenSearch `flat_object` (introduced in 2.7) stores the whole object as a string. Its subfields are not indexed for fast lookup, can't be aggregated with dot notation, and can't be filtered on ([os flat_object][os-flat]).
- **dense_vector vs knn_vector.**
  - Elasticsearch `dense_vector` uses `dims` (maximum 4096). Unmapped float arrays of length 128 to 4096 are dynamically mapped as `dense_vector` ([es dense_vector][es-dense]).
  - OpenSearch `knn_vector` uses `dimension` (1 to 16,000). Approximate k-NN search needs `index.knn: true` ([os knn_vector][os-knn]).
  - OpenSearch 3.9 adds dynamic `knn_vector` mapping behind `knn.dynamic_mapping.enabled`, which is off by default ([os knn_vector][os-knn]).
- **Search replicas.** OpenSearch adds `index.number_of_search_replicas` (default 0) ([os index settings][os-idx]).

## 2. Mapping risks

| Setting | Elasticsearch default | OpenSearch default |
|---|---|---|
| `index.mapping.total_fields.limit` | 1000 ([es][es-maplimit]) | 1000 ([os][os-mapexp]) |
| `index.mapping.depth.limit` | 20 ([es][es-maplimit]) | 20, range 1 to 100 ([os][os-mapexp]) |
| `index.mapping.nested_fields.limit` | 100 ([es][es-maplimit]) | 50 ([os][os-mapexp]) |
| `index.mapping.nested_objects.limit` | 10000 ([es][es-maplimit]) | 10000 ([os][os-mapexp]) |
| `index.mapping.nested_parents.limit` | 50 ([es][es-maplimit]) | not listed |
| `index.mapping.field_name_length.limit` | Long.MAX_VALUE ([es][es-maplimit]) | 50000 ([os][os-mapexp]) |
| `index.mapping.total_fields.ignore_dynamic_beyond_limit` | false ([es][es-maplimit]) | not listed |

- **How Elasticsearch counts fields.** The limit counts every mapper: each object level, each leaf, each multi-field, each alias and each runtime field. Metadata fields don't count. For example, `host.os.name` counts as 3. With `subobjects: false` it counts as 1 ([es mapping limits][es-maplimit]).
- **How OpenSearch counts fields.** The page says regular fields, object mappings and aliases count ([os mapping explosion][os-mapexp]). Whether multi-fields count separately on OpenSearch is unverified.
- **What happens at the limit (Elasticsearch).** The index request fails with "Limit of total fields [X] has been exceeded". If `ignore_dynamic_beyond_limit` is true, the extra fields are dropped from the mapping and listed in `_ignored` instead ([es mapping limits][es-maplimit]).
- **Dynamic modes.**
  - Elasticsearch: `true` (default), `runtime`, `false`, `strict`. Inner objects inherit the setting from their parent ([es dynamic][es-dynamic]).
  - OpenSearch: `true`, `false`, `false_allow_templates`, `strict`, `strict_allow_templates`, plus an index setting `index.mapper.dynamic` (default true) ([os dynamic][os-dynamic], [os mapping explosion][os-mapexp]).
  - OpenSearch's default string mapping is `text` with a `keyword` sub-field (`ignore_above: 256`) ([os dynamic][os-dynamic]).
- **Design: "dynamic: true on a hot index".** Compute the inherited `dynamic` value, then check whether the index is a write index. "Hot = write index" is a dbscope choice, not a vendor definition.
- **Design: text fields used for aggregations.** Flag a `text` field that has no aggregatable sub-field, and confirm with field caps `aggregatable: false` ([es field caps][es-fieldcaps]). The fielddata background rule is unverified.
- **Shard sizing (Elasticsearch).**
  - Aim for 10GB to 50GB per shard and under 200M documents per shard.
  - Hard maximum: 2,147,483,519 documents per shard, counting `docs.count` plus `docs.deleted`.
  - By default a node holds at most 1000 non-frozen shards.
  - Keep fewer than 3000 indices per GB of master-node heap.
  - Source for all four: [es shard sizing][es-shards].
- **Shard sizing (OpenSearch).** The project blog covers ideal shard size ([os shard blog][os-shard-blog]). AWS recommends 10 to 30 GiB for search-latency workloads and 30 to 50 GiB for write-heavy ones ([aws sharding][aws-shards]). I found no equivalent page on docs.opensearch.org.
- **Replicas 0.** Elastic warns this can mean temporary availability loss on node restart, or permanent data loss if data is corrupted ([es index modules][es-idxmod]).
- **Blocks.**
  - Elasticsearch: `read_only`, `read_only_allow_delete`, `read`, `write`, `metadata`, each with its own `FORBIDDEN/<n>` error ([es blocks][es-blocks]).
  - `read_only_allow_delete` is set and removed automatically by the disk-based allocator at the flood-stage watermark ([es blocks][es-blocks]).
  - OpenSearch lists `index.blocks.write` and `index.blocks.read_only` ([os index settings][os-idx]).

## 3. Query safety and cost

**Read-only enforcement (Elasticsearch)**
- **`read` (index):** count, explain, get, mget, msearch, scroll, search, suggest, and ES|QL queries ([es privileges][es-priv]).
- **`view_index_metadata` (index):** aliases, field caps, mappings, get index, get data stream, settings, validate query ([es privileges][es-priv]).
- **`monitor` (index):** recovery, segments, index stats. **`monitor` (cluster):** health, state, node and cluster stats ([es privileges][es-priv]).
- **API keys.** A key's permissions are the intersection of its `role_descriptors` and the owner's permissions. The `encoded` field is base64 of `id:api_key` ([es api key][es-apikey]). The Go client supports basic auth, API key and service token ([es client source][es-client-src]).

**Read-only enforcement (OpenSearch)**
- **Action groups.**
  - `read` = `indices:data/read*`, `indices:admin/mappings/fields/get*`, `indices:admin/resolve/index`.
  - `cluster_monitor` = `cluster:monitor/*`.
  - `indices_monitor` = `indices:monitor/*`.
  - Source: [os action groups][os-groups].
- **Extra permissions.** `GET _mapping` needs `indices:admin/mappings/get`. Get index needs `indices:admin/get`. Get data stream needs `indices:admin/data_stream/get` ([os permissions][os-perms]).
- **Built-in roles.** `readall` and `readall_and_monitor` ([os roles][os-roles]). Whether `readall` covers `indices:admin/mappings/get` is unverified.

**Async results write state.** Elasticsearch SQL stores results when `keep_on_completion` or `wait_for_completion_timeout` is used ([es sql query][es-sql]). **Design:** dbscope never sets either.

**Timeouts and caps**
- **Elasticsearch `timeout`:** per shard, no default. The two Elastic pages disagree on what a timeout does:
  - The guide says a timed-out shard returns partial results with `"timed_out": true` ([es search api][es-search-guide]).
  - The API reference says the request "fails and returns an error" ([es search op][es-search-op]).
  - **Design:** check `timed_out` in every response, and send `allow_partial_search_results=false` where an error is wanted.
- **`search.default_search_timeout`:** its resolution is about 200ms, set by `thread_pool.estimated_time_interval` ([es search api][es-search-guide]).
- **OpenSearch `timeout`:** the page contradicts itself. The query-parameter table says the default is 1m, and the request-body table says there is no default. OpenSearch also has `cancel_after_time_interval` (default -1) ([os search][os-search]).
- **`terminate_after`:** maximum documents collected per shard. Default 0, which means off ([es search op][es-search-op], [os search][os-search]).
- **`index.max_result_window`:** caps `from + size`. Default 10000 on both products ([es index modules][es-idxmod], [os index settings][os-idx]).
- **Other index caps:** `max_inner_result_window` 100, `max_terms_count` 65536, `max_regex_length` 1000, `max_script_fields` 32, `max_docvalue_fields_search` 100. Both products list these with the same defaults ([es index modules][es-idxmod], [os index settings][os-idx]).
- **`search.max_buckets`:** 65,536 ([es search settings][es-search-settings]).
- **`track_total_hits`:** default 10000 ([es search api][es-search-guide]).
- **SQL limits.** Elasticsearch SQL: `fetch_size` (default 1000), `request_timeout`, `page_timeout` ([es sql query][es-sql]). OpenSearch SQL: `fetch_size` (default 1000, jdbc format only) ([os sql api][os-sql]).

**Expensive queries**
- **Elasticsearch list:**
  - script queries;
  - queries on fields that aren't indexed but have doc values;
  - fuzzy, regexp, prefix and wildcard, except on `wildcard` fields;
  - range queries on text or keyword fields;
  - joining queries;
  - queries on runtime fields;
  - `script_score` and `percolate`.
  - Source: [es expensive][es-expensive].
- **Blocking them on Elasticsearch.** `search.allow_expensive_queries: false` blocks these queries. Setting it needs the `manage` cluster privilege, and it can't be changed on Serverless ([es expensive][es-expensive]).
- **OpenSearch list:** fuzzy, prefix, range on text or keyword, regexp, wildcard, `query_string` queries that become prefix queries, and `geo_shape` on prefix-tree fields. The setting has the same name ([os query dsl][os-qdsl]).
- **Leading wildcards.** The pages I fetched don't say leading wildcards are worse than trailing ones. Unverified.
- **Index targets.** Leaving the index empty, or using `*` or `_all`, searches everything ([es search op][es-search-op]).
- **Query nesting depth.** OpenSearch: `index.query.max_nested_depth` (default 20) ([os index settings][os-idx]). Elasticsearch: `indices.query.bool.max_nested_depth` (default 30) ([es search settings][es-search-settings]).

**Profile and translate**
- **Profile.** `"profile": true` returns per-shard query trees with `type`, `description`, `time_in_nanos` and `breakdown`, plus `rewrite_time`, collectors, aggregations and fetch timing. Elastic warns it adds significant overhead ([es profile][es-profile]). The query still runs.
- **OpenSearch profile.** OpenSearch accepts `profile` in the request body ([os search][os-search]).
- **Elasticsearch translate.** `POST /_sql/translate` returns the search request that SQL would run ([es sql translate][es-sql-translate]).
- **OpenSearch explain.** `_plugins/_sql/_explain` and `_plugins/_ppl/_explain` return DSL. With Calcite enabled (3.0+, `plugins.calcite.enabled`), they return logical and physical plans in `standard`, `simple`, `cost` or `extended` format ([os sql api][os-sql]).
- **Privileges for translate.** Whether translate needs privileges beyond `read` is unverified.

## 4. Go clients

| | go-elasticsearch v9 | opensearch-go v5 |
|---|---|---|
| Latest | v9.5.2 ([proxy][proxy-es9]) | v5.1.3 ([proxy][proxy-os5]) |
| `go` directive | 1.25 ([mod][mod-es9]) | 1.26.0 ([mod][mod-os5]) |
| Direct deps | `elastic-transport-go/v8 v8.9.0`, `otel/trace v1.35.0` ([mod][mod-es9]) | `aws-sdk-go-v2 v1.47.3`, `aws-sdk-go-v2/config v1.33.9`, `rs/dnscache`, `testify`, `jsondiff`, `x/mod`, `x/sync` ([mod][mod-os5]) |
| Server check | Rejects servers that lack `X-Elastic-Product: Elasticsearch` ([source][es-client-src]) | Whether it rejects Elasticsearch is unverified |
| Auth | Basic, API key, service token, CA cert, cert fingerprint ([source][es-client-src]) | Basic, TLS, SigV4 signer (`signer/awsv2`, service `es` or `aoss`) ([os user guide][os-guide]) |

- **Version compatibility.** Elastic says its clients work with Elasticsearch of the same or a newer minor version ([es client readme][es-client-readme]). So a v9 client talking to an 8.x server is not promised to work.
- **Compatibility header.** The v9 client sends `compatible-with=9` only when compatibility mode is enabled ([es client source][es-client-src]).
- **When SigV4 is required.** Every request to the Amazon OpenSearch Service configuration API must be signed. Requests to OpenSearch APIs must be signed when access policies name IAM users or roles ([os user guide][os-guide]).
- **CGO.** Neither module lists a dependency that I know to need CGO. I did not check the sources for `import "C"`. Unverified.
- **Design:** use plain `net/http` for both products.
  - Detect the product from `GET /`.
  - Sign AWS requests with `aws/signer/v4`.
  - This avoids carrying two clients, the product check, and the v8/v9 split.
  - opensearch-go v5 pins the same AWS SDK versions that dbscope's `go.mod` already uses, so adding it later would cost little.

## 5. Test servers

**Elasticsearch**
- **Images:** `docker.elastic.co/elasticsearch/elasticsearch:9.5.5` and `:8.19.23` ([es docker 9][es-docker9], [es docker 8][es-docker8]).
- **Default container:** with `-m 1GB`, Elasticsearch turns on security automatically. It prints the `elastic` password once and serves HTTPS signed by `config/certs/http_ca.crt` ([es docker 9][es-docker9]).
- **Single node:** `discovery.type=single-node` keeps the node from joining other clusters ([es minimal security][es-minsec]).
- **Security switch:** `xpack.security.enabled` is a static setting and defaults to true ([es security settings][es-secset]). Settings can be passed as environment variables by their names ([es docker config][es-docker-cfg]).
- **Heap:** `ES_JAVA_OPTS="-Xms1g -Xmx1g"` is fine for testing but not recommended for production ([es docker prod][es-docker-prod]).
- **Startup time:** not stated by Elastic. Unverified.
- **Design: read-only role or API key.** `cluster: ["monitor"]`, and `indices: [{names: ["*"], privileges: ["read","view_index_metadata","monitor"]}]` ([es privileges][es-priv], [es api key][es-apikey]).

**OpenSearch**
- **Images:** `opensearchproject/opensearch:3` and the mirror `public.ecr.aws/opensearchproject/opensearch:3` ([os docker][os-docker]).
- **Admin password:** 2.12 and later require `OPENSEARCH_INITIAL_ADMIN_PASSWORD`, and the password must pass a strength check ([os docker][os-docker]).
- **No security:** set `DISABLE_INSTALL_DEMO_CONFIG=true` and `DISABLE_SECURITY_PLUGIN=true` ([os docker][os-docker]).
- **Heap:** the development compose file sets `OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m` ([os docker][os-docker]).
- **Host settings:** Linux needs `vm.max_map_count=262144`. Docker Desktop should have at least 4 GB ([os docker][os-docker]).
- **Startup time:** the page says to wait "a few minutes" ([os docker][os-docker]).
- **Read-only user:** create it through `_plugins/_security/api/` (`internalusers`, `roles`, `rolesmapping`) ([os security api][os-secapi]). Map it to `readall_and_monitor` or a custom role ([os roles][os-roles]).

## Facts dbscope must encode

- [ ] Detect the product from `GET /`: an `X-Elastic-Product: Elasticsearch` header means Elasticsearch, `version.distribution: "opensearch"` means OpenSearch ([es client source][es-client-src], [os docker][os-docker]).
- [ ] Count mapping fields the Elasticsearch way (objects, leaves, multi-fields, aliases, runtime fields; no metadata fields) and compare against `total_fields.limit`, default 1000 ([es mapping limits][es-maplimit]).
- [ ] Use per-product defaults: `nested_fields.limit` is 100 on Elasticsearch and 50 on OpenSearch. `depth.limit` is 20 and `nested_objects.limit` is 10000 on both ([es][es-maplimit], [os][os-mapexp]).
- [ ] Parse `dynamic` as either a bool or a string. Accept `runtime` (Elasticsearch) and `false_allow_templates`/`strict_allow_templates` (OpenSearch). Inherit it down the object tree ([es dynamic][es-dynamic], [os dynamic][os-dynamic]).
- [ ] Treat a field with more than one type key under `fields.<name>` in `_field_caps` as a type conflict ([es field caps][es-fieldcaps]).
- [ ] Shard rules: 10GB to 50GB per shard, under 200M documents, a hard maximum of 2,147,483,519 documents, and 1000 non-frozen shards per node ([es shard sizing][es-shards]).
- [ ] Flag `number_of_replicas: 0` and any `index.blocks.*` set to true. Treat `read_only_allow_delete` as a disk-watermark signal ([es index modules][es-idxmod], [es blocks][es-blocks]).
- [ ] Reject a query whose `from + size` exceeds `max_result_window` (default 10000) before sending it ([es index modules][es-idxmod]).
- [ ] Flag these query types: script, fuzzy, regexp, prefix, wildcard, range on text or keyword, joining, runtime-field, script_score, percolate ([es expensive][es-expensive], [os query dsl][os-qdsl]).
- [ ] Flag terms aggregations that could exceed `search.max_buckets` (65,536) ([es search settings][es-search-settings]).
- [ ] Flag empty, `*` and `_all` index targets ([es search op][es-search-op]).
- [ ] Always send `timeout`, set a client-side context deadline, and check `timed_out` in the response ([es search api][es-search-guide]).
- [ ] Never set `keep_on_completion` or `wait_for_completion_timeout` on SQL requests ([es sql query][es-sql]).
- [ ] To explain a query without running it, use `POST /_sql/translate` on Elasticsearch, and `_plugins/_sql/_explain` or `_plugins/_ppl/_explain` on OpenSearch ([es sql translate][es-sql-translate], [os sql api][os-sql]).
- [ ] Read-only privilege sets ([es privileges][es-priv], [os action groups][os-groups], [os permissions][os-perms]):
  - Elasticsearch: index `read`, `view_index_metadata` and `monitor`, plus cluster `monitor`.
  - OpenSearch: `read`, `indices:admin/mappings/get`, `indices:admin/get`, `indices:admin/data_stream/get`, `indices_monitor` and `cluster_monitor`.
- [ ] The API key header value is the `encoded` field, which is base64 of `id:api_key` ([es api key][es-apikey]).
- [ ] On AWS, sign with SigV4 using service `es` for managed domains or `aoss` for Serverless ([os user guide][os-guide]).

[es-docker9]: https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-elasticsearch-docker-basic
[es-docker8]: https://www.elastic.co/guide/en/elasticsearch/reference/8.19/docker.html
[es-docker-cfg]: https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-elasticsearch-docker-configure
[es-docker-prod]: https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-elasticsearch-docker-prod
[es-minsec]: https://www.elastic.co/docs/deploy-manage/security/set-up-minimal-security
[es-secset]: https://www.elastic.co/docs/reference/elasticsearch/configuration-reference/security-settings
[es-maplimit]: https://www.elastic.co/docs/reference/elasticsearch/index-settings/mapping-limit
[es-dynamic]: https://www.elastic.co/docs/reference/elasticsearch/mapping-reference/dynamic
[es-shards]: https://www.elastic.co/docs/deploy-manage/production-guidance/optimize-performance/size-shards
[es-priv]: https://www.elastic.co/docs/reference/elasticsearch/security-privileges
[es-idxmod]: https://www.elastic.co/docs/reference/elasticsearch/index-settings/index-modules
[es-search-settings]: https://www.elastic.co/docs/reference/elasticsearch/configuration-reference/search-settings
[es-search-guide]: https://www.elastic.co/docs/solutions/search/the-search-api
[es-search-op]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-search
[es-expensive]: https://www.elastic.co/docs/reference/query-languages/querydsl
[es-blocks]: https://www.elastic.co/docs/reference/elasticsearch/index-settings/index-block
[es-cat]: https://www.elastic.co/docs/api/doc/elasticsearch/group/endpoint-cat
[es-cat-indices]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-cat-indices
[es-resolve]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-indices-resolve-index
[es-ds]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-indices-get-data-stream
[es-health]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-cluster-health
[es-fieldcaps]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-field-caps
[es-types]: https://www.elastic.co/guide/en/elasticsearch/reference/7.17/removal-of-types.html
[es-flattened]: https://www.elastic.co/docs/reference/elasticsearch/mapping-reference/flattened
[es-dense]: https://www.elastic.co/docs/reference/elasticsearch/mapping-reference/dense-vector
[es-profile]: https://www.elastic.co/docs/reference/elasticsearch/rest-apis/search-profile
[es-sql-translate]: https://www.elastic.co/docs/reference/query-languages/sql/sql-translate
[es-sql]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-sql-query
[es-apikey]: https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-security-create-api-key
[es-client-readme]: https://github.com/elastic/go-elasticsearch/blob/main/README.md
[es-client-src]: https://github.com/elastic/go-elasticsearch/blob/v9.5.2/elasticsearch.go
[proxy-es9]: https://proxy.golang.org/github.com/elastic/go-elasticsearch/v9/@latest
[proxy-es8]: https://proxy.golang.org/github.com/elastic/go-elasticsearch/v8/@latest
[mod-es9]: https://proxy.golang.org/github.com/elastic/go-elasticsearch/v9/@v/v9.5.2.mod
[proxy-os5]: https://proxy.golang.org/github.com/opensearch-project/opensearch-go/v5/@latest
[proxy-os4]: https://proxy.golang.org/github.com/opensearch-project/opensearch-go/v4/@latest
[mod-os5]: https://proxy.golang.org/github.com/opensearch-project/opensearch-go/v5/@v/v5.1.3.mod
[os-guide]: https://github.com/opensearch-project/opensearch-go/blob/main/USER_GUIDE.md
[sigv4]: https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws/signer/v4
[os-docker]: https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/
[os-mapexp]: https://docs.opensearch.org/latest/mappings/mapping-explosion/
[os-dynamic]: https://docs.opensearch.org/latest/mappings/mapping-parameters/dynamic/
[os-idx]: https://docs.opensearch.org/latest/install-and-configure/configuring-opensearch/index-settings/
[os-flat]: https://docs.opensearch.org/latest/mappings/supported-field-types/flat-object/
[os-knn]: https://docs.opensearch.org/latest/mappings/supported-field-types/knn-vector/
[os-qdsl]: https://docs.opensearch.org/latest/query-dsl/
[os-search]: https://docs.opensearch.org/latest/api-reference/search-apis/search/
[os-sql]: https://docs.opensearch.org/latest/sql-and-ppl/sql-and-ppl-api/index/
[os-roles]: https://docs.opensearch.org/latest/security/access-control/users-roles/
[os-groups]: https://docs.opensearch.org/latest/security/access-control/default-action-groups/
[os-perms]: https://docs.opensearch.org/latest/security/access-control/permissions/
[os-secapi]: https://docs.opensearch.org/latest/security/access-control/api/
[os-breaking]: https://docs.opensearch.org/latest/breaking-changes/
[os-shard-blog]: https://opensearch.org/blog/optimize-opensearch-index-shard-size/
[aws-shards]: https://docs.aws.amazon.com/opensearch-service/latest/developerguide/bp-sharding.html
