# Research: Amazon DynamoDB support

Collected 2026-10-28 by a research worker; the design dbscope adopted is in docs/ENGINES.md.

Collected 2026-10-28 from primary sources. Links are listed under "Sources" at the end. Nothing was run against AWS or DynamoDB Local, and no build or binary-size measurement was made. Claims without a primary source are marked "unverified". Lines marked **design** are proposals for dbscope, not facts about DynamoDB. Page content was rephrased for compliance with licensing restrictions. The links point to HTML pages; the content was read from the Markdown copies AWS publishes at the same paths with a `.md` suffix.

**Versions seen on the research date:**
- **AWS SDK for Go v2:** core v1.47.5, config v1.33.12, credentials v1.20.12, service/dynamodb v1.72.2, feature/dynamodb/attributevalue v1.21.16. All were tagged 2026-10-27 ([core][px-core], [config][px-config], [credentials][px-creds], [dynamodb][px-ddb], [attributevalue][px-av]).
- **DynamoDB Local:** 3.3.2, released 2026-10-15 ([release history][localhist]). On Docker Hub, `amazon/dynamodb-local:3.3.2` has the same digest as `latest` ([tags][hubtags]).

**These findings matter most for dbscope's design:**
- **IAM is the read-only barrier.** `ExecuteStatement` is authorized per statement kind by `PartiQLSelect`, `PartiQLInsert`, `PartiQLUpdate` and `PartiQLDelete` ([service reference JSON][iamjson]). A role with only Describe, Scan, Query and PartiQLSelect cannot write. This matches docs/ENGINES.md, where dbscope's own statement checks only refuse early.
- **Counts are stale.** `ItemCount`, `TableSizeBytes` and `IndexSizeBytes` refresh about every six hours ([TableDescription][td]).
- **Filters do not reduce cost.** Scan and Query charge for every item read before the filter runs ([Scan][scan], [Query filters][qfe]).
- **Key schemas are no longer one HASH plus at most one RANGE.** A GSI can have up to four partition-key and four sort-key attributes ([GSI][gsi]). `TableDescription` also has a `VectorIndexes` field now ([td]).
- **`BillingModeSummary` can be missing.** A table may need to have switched to on-demand once before it appears ([BillingModeSummary][bms]). DynamoDB Local always returns null for it ([Local usage notes][local]).

---

## 1. Catalog

### 1.1 ListTables
- **Pagination.** Each page returns at most 100 names. Pass `LastEvaluatedTableName` back as `ExclusiveStartTableName` until it is absent ([ListTables][lt]).
- **Scope.** Results cover the current account and endpoint, which means one Region ([lt]).
- **IAM.** `dynamodb:ListTables` is a List action with no resource type, so the policy needs `"Resource": "*"` ([IAM reference][iam]).
- **Rate.** DescribeTable, ListTables and GetResourcePolicy share 2,500 requests per second ([Constraints][cons]).
- **Quota.** The default is 2,500 tables per Region ([Quotas][quotas]).

### 1.2 DescribeTable output (TableDescription)
Fields come from [TableDescription][td] unless another source is cited.

| Field | Contents | Notes for dbscope |
|---|---|---|
| `KeySchema` | List of `{AttributeName, KeyType}`, where `KeyType` is `HASH` or `RANGE`. Minimum 1 element. | Parse it as a list. |
| `AttributeDefinitions` | Name and type, for key-schema attributes only. | Key types are S, N or B ([gsi], [lsi]). Other attribute types must come from sampling. |
| `GlobalSecondaryIndexes` | `IndexName`, `KeySchema`, `Projection` (`KEYS_ONLY`, `INCLUDE` or `ALL`, plus `NonKeyAttributes`), `IndexStatus` (`CREATING`, `UPDATING`, `DELETING` or `ACTIVE`), `Backfilling`, `ProvisionedThroughput`, `IndexSizeBytes`, `ItemCount`. | Size and count refresh about every 6 hours. No index information is returned while the table is `DELETING`. `Backfilling` appears only for indexes added after the table was created. |
| `LocalSecondaryIndexes` | `IndexName`, `KeySchema`, `Projection`, `IndexSizeBytes`, `ItemCount`. | No `IndexStatus` is documented. Up to 5 per table, created only with the table ([lsi]). |
| `BillingModeSummary` | `BillingMode` (`PROVISIONED` or `PAY_PER_REQUEST`) and `LastUpdateToPayPerRequestDateTime` ([bms]). | **design:** treat an absent value as `PROVISIONED` and label it "inferred". This inference is unverified. |
| `ProvisionedThroughput` | RCU, WCU, last increase and decrease times, `NumberOfDecreasesToday` ([ptd]). | Both are 0 on on-demand tables. 50 RCU allows 100 eventually consistent reads per second ([ptd]). |
| `OnDemandThroughput` | `MaxReadRequestUnits`, `MaxWriteRequestUnits`. A value of -1 removes the cap ([odt]). | A cap can throttle dbscope's sampling. |
| `TableSizeBytes`, `ItemCount` | Refreshed about every 6 hours. | Label them approximate. |
| `StreamSpecification` | `StreamEnabled`, and `StreamViewType` (`KEYS_ONLY`, `NEW_IMAGE`, `OLD_IMAGE` or `NEW_AND_OLD_IMAGES`) ([ss]). Also `LatestStreamArn` and `LatestStreamLabel`. | No Streams API call is needed. |
| `TableClassSummary` | `STANDARD` or `STANDARD_INFREQUENT_ACCESS`, plus `LastUpdateDateTime` ([tcs]). | Whether "absent" means STANDARD is unverified. GSIs share the base table's class ([gsi]). |
| `DeletionProtectionEnabled` | Boolean. | Report it. |
| `TableStatus` | `CREATING`, `UPDATING`, `DELETING`, `ACTIVE`, `INACCESSIBLE_ENCRYPTION_CREDENTIALS`, `ARCHIVING`, `ARCHIVED`, `REPLICATION_NOT_AUTHORIZED`. | The table remains usable while `UPDATING`. |
| `Replicas`, `GlobalTableVersion`, `MultiRegionConsistency`, `GlobalTableWitnesses` | Global-table details. | Report only. |
| `VectorIndexes` | Name, status, vector attribute, dimensions, distance function, projection, size and count. | Counts toward the default quota of 20 GSIs per table ([quotas]). |
| `WarmThroughput`, `SSEDescription`, `ArchivalSummary`, `RestoreSummary`, `TableArn`, `TableId` | Other metadata. | Not researched further. |

IAM: `dynamodb:DescribeTable`, access level Read, resource type `table` ([iam]).

### 1.3 DescribeTimeToLive
- **Output.** `AttributeName` and `TimeToLiveStatus` (`ENABLING`, `DISABLING`, `ENABLED` or `DISABLED`) ([TimeToLiveDescription][ttld]).
- **Rate.** Throttled at 10 read request units per second, with `ThrottlingException` beyond that ([cons]).
- **IAM.** `dynamodb:DescribeTimeToLive`, Read, resource type `table` ([iam]).
- **design:** call it once per table, serially, and cache the result.

### 1.4 DescribeContinuousBackups
- **Output.** `ContinuousBackupsStatus` and `PointInTimeRecoveryDescription`. The PITR fields are status, earliest and latest restorable time, and `RecoveryPeriodInDays` from 1 to 35 ([ContinuousBackupsDescription][cbd], [PITR][pitr]).
- **Which field to report.** Continuous backups are enabled on every table at creation, so report `PointInTimeRecoveryStatus` ([DescribeContinuousBackups][dcb]).
- **Rate.** At most 10 calls per second ([dcb]).
- **Error.** Returns `TableNotFoundException` for a missing table or the wrong Region ([dcb]).
- **IAM.** `dynamodb:DescribeContinuousBackups`, Read, resource type `table` ([iam]).
- **DynamoDB Local.** Local does not support PITR ([local]). What this call returns there is unverified.

### 1.5 Optional sources
- **Contributor Insights status.** `DescribeContributorInsights` is Read on a table or index. `ListContributorInsights` is List with Resource `*` ([iam]). Each allows up to 5 rps and should be called only periodically ([cons]).
- **Auto scaling.** This is not in DescribeTable. It is configured as an Application Auto Scaling scalable target ([aasddb]):
  - namespace `dynamodb`
  - dimension `dynamodb:{table|index}:{Read|Write}CapacityUnits`
  - resource ID `table/T` or `table/T/index/I`
  - `application-autoscaling:DescribeScalableTargets` and `DescribeScalingPolicies` are non-write and list no resource type ([aasjson]).
- **DescribeLimits.** Calling it more than once a minute can throttle ([cons]). **design:** skip it.
- **Caller identity.** `sts:GetCallerIdentity` needs no permissions and returns the Account and ARN ([sts]). **design:** show the account and principal before reading data.

### 1.6 IAM action per call

| Call | IAM action | Resource | Level | Source |
|---|---|---|---|---|
| ListTables | dynamodb:ListTables | `*` | List | [iam] |
| DescribeTable | dynamodb:DescribeTable | table | Read | [iam] |
| DescribeTimeToLive | dynamodb:DescribeTimeToLive | table | Read | [iam] |
| DescribeContinuousBackups | dynamodb:DescribeContinuousBackups | table | Read | [iam] |
| DescribeContributorInsights | dynamodb:DescribeContributorInsights | table, index | Read | [iam] |
| ListContributorInsights | dynamodb:ListContributorInsights | `*` | List | [iam] |
| Scan | dynamodb:Scan | table, index | Read | [iam] |
| Query | dynamodb:Query | table, index | Read | [iam] |
| ExecuteStatement SELECT | dynamodb:PartiQLSelect | table, index | Read. Condition key `dynamodb:FullTableScan` | [iam], [iamjson] |
| ExecuteStatement INSERT, UPDATE, DELETE | dynamodb:PartiQLInsert, PartiQLUpdate, PartiQLDelete | table | Write | [iam], [qliam] |
| GetInsightRuleReport | cloudwatch:GetInsightRuleReport | insight-rule/DynamoDBContributorInsights* | Read | [ciiam] |
| GetMetricData | cloudwatch:GetMetricData | `dataset` resource listed | non-write | [cwjson] |
| DescribeScalableTargets, DescribeScalingPolicies | application-autoscaling:Describe* | none listed | non-write | [aasjson] |
| GetCallerIdentity | none needed | none | n/a | [sts] |

### 1.7 Minimal read-only IAM policy (**design**, not run through an IAM validator)
Replace `REGION` and `ACCOUNT`. Other AWS partitions need a different ARN prefix, which was not researched.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "DbscopeList", "Effect": "Allow",
      "Action": ["dynamodb:ListTables", "dynamodb:ListContributorInsights"],
      "Resource": "*" },
    { "Sid": "DbscopeCatalogAndRead", "Effect": "Allow",
      "Action": ["dynamodb:DescribeTable", "dynamodb:DescribeTimeToLive",
                 "dynamodb:DescribeContinuousBackups", "dynamodb:DescribeContributorInsights",
                 "dynamodb:Scan", "dynamodb:Query", "dynamodb:PartiQLSelect"],
      "Resource": ["arn:aws:dynamodb:REGION:ACCOUNT:table/*",
                   "arn:aws:dynamodb:REGION:ACCOUNT:table/*/index/*"] },
    { "Sid": "DbscopeNoPartiQLFullScan", "Effect": "Deny",
      "Action": "dynamodb:PartiQLSelect",
      "Resource": ["arn:aws:dynamodb:REGION:ACCOUNT:table/*",
                   "arn:aws:dynamodb:REGION:ACCOUNT:table/*/index/*"],
      "Condition": { "Bool": { "dynamodb:FullTableScan": "true" } } },
    { "Sid": "DbscopeNoWrites", "Effect": "Deny",
      "Action": ["dynamodb:PartiQLInsert", "dynamodb:PartiQLUpdate", "dynamodb:PartiQLDelete",
                 "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem",
                 "dynamodb:BatchWriteItem"],
      "Resource": "*" }
  ]
}
```

Optional statements for section 4:

```json
{ "Sid": "DbscopeMetrics", "Effect": "Allow",
  "Action": ["cloudwatch:GetMetricData",
             "application-autoscaling:DescribeScalableTargets",
             "application-autoscaling:DescribeScalingPolicies"],
  "Resource": "*" },
{ "Sid": "DbscopeContributorInsightsRead", "Effect": "Allow",
  "Action": "cloudwatch:GetInsightRuleReport",
  "Resource": "arn:aws:cloudwatch:REGION:ACCOUNT:insight-rule/DynamoDBContributorInsights*" }
```

- **FullTableScan deny.** It follows the AWS example ([qliam]) and blocks PartiQL SELECTs typed by users that would scan the table. dbscope's sampling uses the Scan API, and the Scan action's condition keys do not include `FullTableScan` ([iam]).
- **design: the explicit Deny statements.** They only matter if another attached policy allows writes. A dedicated role carrying only this policy is safer.

---

## 2. Sampling cost

### 2.1 Facts
- **Units.** One read unit is one strongly consistent read per second, or two eventually consistent reads per second, of an item up to 4 KB. Provisioned RCU and on-demand RRU use the same sizes. 1 KB is 1,024 bytes ([cons]).
- **Consistency.** Scan is eventually consistent by default, and `ConsistentRead=true` doubles the cost ([scan]). Even with `ConsistentRead`, a Scan does not read a single snapshot ([Scan API][scanapi]).
- **What is charged.** Scan bills the size of items evaluated, not items returned. Projection and filter do not change the cost ([scan], [rw]). `Select=COUNT` costs the same as reading the items ([rw], [scanapi]).
- **Minimum charges.** A Scan consumes at least one read of an item up to 4 KB. Empty tables, and large tables with sparse keys, may be charged extra RCUs ([scan]). Query and Scan are charged even when no data exists ([rw]).
- **Page size.** At most 1 MB per call, measured before the filter. `Limit` caps the number of items evaluated, before the filter ([scan], [scanapi]).
- **Pagination.** Only a missing `LastEvaluatedKey` ends pagination. Pass it back as `ExclusiveStartKey` ([scan]). An empty page can still carry a `LastEvaluatedKey` ([Query API][queryapi]).
- **Counts.** `Count` and `ScannedCount` cover one page only ([scan]).
- **Order.** Partition-key values come back in arbitrary order. Within one partition key, items come in ascending sort-key order. A sequential Scan reads one partition at a time ([scan]).
- **Parallel scan.** `0 ≤ Segment < TotalSegments ≤ 1,000,000`, and each segment's `LastEvaluatedKey` is reused only with that same segment ([scanapi]). Items are assigned to segments by partition-key hash, so segments can be uneven ([scan]).
- **Throughput risk.** Many parallel workers can consume all provisioned throughput. Setting `Limit` per worker helps ([scan]).
- **Whose capacity.** A table scan uses table RCU, a GSI scan uses GSI RCU, and an LSI scan uses base-table RCU ([scan]).
- **ReturnConsumedCapacity.** One of `NONE` (the default), `TOTAL` or `INDEXES` ([scan]).
- **SDK paginators.** The SDK has `NewScanPaginator` and `NewQueryPaginator`, both with a `Limit` option ([dynamodb pkg][ddbpkg]).

### 2.2 Derived bounds (arithmetic, not AWS figures)
- **Full page.** A full 1 MB eventually consistent page costs 1,024 / 4 / 2 = 128 read units. A strongly consistent page costs 256.
- **Rounding.** Query rounds the total size of returned items up to 4 KB ([rw]). For Scan, the docs do not say whether rounding is per item or per page (unverified). For budgeting, assume the worst case: a page of `Limit=N` small items costs at most N x 0.5 units, capped at 128.
- **design:** always request `ReturnConsumedCapacity=TOTAL`, sum `ConsumedCapacity.CapacityUnits`, and stop at the budget.

### 2.3 Bounding cost (**design**)
- **Defaults.** Eventually consistent reads, `Limit` 100, and `ReturnConsumedCapacity=TOTAL`. Hard caps on items (e.g. 1,000), on units (e.g. 50), on number of requests, and on wall-clock time. All numbers here are proposals.
- **Spread.** Pick k random segments out of a large `TotalSegments` (e.g. 1,000) and read one page per segment, sequentially. Because segments are hash buckets, this spreads the sample across the keyspace ([scan]). Empty segments still pay the minimum charge. DynamoDB Local ignores segments ([local]), so read sequentially there.
- **Provisioned tables.** dbscope competes with the application for capacity ([scan]). Pace requests to a small share of `ReadCapacityUnits`, e.g. 10%.
- **On-demand tables.**
  - The server has no per-request cost cap, so every unit is billed. Pricing was not researched.
  - A `MaxReadRequestUnits` cap can throttle dbscope with `TableReadMaxOnDemandThroughputExceeded` ([treason], [throt]).
- **Consistency.** Never sample with `ConsistentRead=true`. GSIs reject it in any case ([queryapi]).
- **GSIs.** Do not infer table attributes from a GSI unless its projection is `ALL` ([gsi]).

### 2.4 Throttling behaviour
- **Exceptions.** Throttles arrive as `ProvisionedThroughputExceededException`, `ThrottlingException` or `RequestLimitExceeded`. All are HTTP 400 and carry `ThrottlingReasons` with the reason and the throttled resource's ARN ([ExecuteStatement][exec], [queryapi]).
- **Reason names.** A reason is built from three parts ([treason]):
  - resource: `Table` or `Index`
  - operation: `Read` or `Write`
  - limit: `ProvisionedThroughputExceeded`, `AccountLimitExceeded`, `KeyRangeThroughputExceeded` or `MaxOnDemandThroughputExceeded`
- **Burst capacity.** DynamoDB keeps up to 300 seconds of unused capacity for bursts, and may also use it for background work ([burst]).
- **SDK retries.**
  - The Go v2 standard retryer defaults to 3 attempts and a 20 s maximum backoff ([Go retries][goretry]).
  - The cross-SDK guide says DynamoDB clients default to 4 attempts with a 25 ms base delay, but marks Go v2 as "see tracking issue" ([SDK retry][sdkretry]). Whether Go v2 already uses 4 attempts is unverified.
  - `retry.AddWithMaxAttempts(r, 0)` retries forever ([goretry]).
- **design:** use 1 or 2 attempts for sampling. Stop at the first throttle and report it as a finding.

---

## 3. Query semantics for access-pattern checks

### 3.1 KeyConditionExpression
- **Partition key.** It must be compared with equality. Non-key attributes are not allowed in a key condition ([key conditions][kce]).
- **Sort key.** At most one condition, using `=`, `<`, `<=`, `>`, `>=`, `BETWEEN` or `begins_with` ([kce]). `begins_with` is not allowed on a Number sort key ([queryapi]).
- **Multi-attribute GSI keys.** Every partition-key attribute needs an equality. Sort-key attributes must be used left to right without gaps, and any inequality must come last ([gsi]).
- **Filter-only operators.** The filter docs list `<>`, `OR`, `CONTAINS`, `IN`, `EXISTS` and `SIZE` as available in filters ([qfe]). **design:** treat them as invalid in a key condition. This is an inference.
- **Ordering.** Results are ordered by sort key, numerically for Number and by UTF-8 bytes otherwise. `ScanIndexForward=false` reverses the order ([kce]).
- **Size and expression limits.** At most 1 MB per call, measured before filter and projection ([kce]). An expression string is limited to 4 KB, with at most 300 operators and 100 `IN` operands ([cons]).
- **Reserved words.** These need `ExpressionAttributeNames` ([kce], [cons]).

### 3.2 FilterExpression cost
- **Read before filter.** The filter runs after the read, so the cost is unchanged ([qfe], [scan]).
- **No key attributes.** A Query filter cannot reference partition-key or sort-key attributes ([qfe], [queryapi]).
- **Efficiency signal.** A high `ScannedCount` with a low `Count` signals waste ([scan], [queryapi]).
- **design:** estimate the filter's selectivity s from the sample, and report "reads about N, returns about N x s".

### 3.3 When a GSI or LSI can serve a query
**Facts:**
- **GSI keys.** A GSI has its own key schema of top-level S, N or B attributes. Its keys need not be unique, and the table's primary key is always projected ([gsi]).
- **Sparse GSIs.** A GSI omits items that lack its key attributes ([gsi]).
- **No fetches from a GSI.** A GSI query cannot fetch unprojected attributes ([gsi], [scanapi]).
- **GSI consistency.** GSIs are eventually consistent only, and `ConsistentRead=true` returns `ValidationException` ([queryapi]).
- **GSI read cost.** It is based on the size of index entries ([gsi]).
- **No GetItem on indexes.** GetItem and BatchGetItem cannot target any index ([gsi], [lsi]).
- **LSI keys.** An LSI has the same partition key as the table and one scalar sort key. The table's sort key is projected into it ([lsi]).
- **LSI fetches.** An LSI can fetch unprojected attributes. Each fetched item is charged as a whole item, rounded up to 4 KB ([lsi]).
- **LSI consistency.** LSIs support strongly consistent reads ([lsi]).
- **Missing or inactive targets.** These give `ResourceNotFoundException` ([queryapi]).

**design: candidate check.** Run it for the table and for each index:
1. The partition-key equality covers every partition-key attribute of the candidate.
2. Any sort-key condition is on the candidate's sort key, with a valid operator and position.
3. The requested and filtered attributes are within the projection. This is hard for a GSI. For an LSI it is soft, but the fetch cost is reported. Whether a GSI filter may use unprojected attributes is unverified, and it likely cannot.
4. A query that needs strong consistency excludes GSIs.
5. The candidate's status is `ACTIVE`.
6. Warn about sparse-index semantics.

If no candidate passes, the verdict is "needs a Scan". Among passing candidates, prefer the table, then the covering index with the smallest projection.

### 3.4 Scan versus Query
- **Scan** reads every item, up to 1 MB per call. Its filter may use any attribute, including keys ([scan]).
- **Query** reads one partition-key value, optionally narrowed by a sort-key range, and returns results in sort-key order ([queryapi]).

### 3.5 PartiQL
- **ExecuteStatement.** `Statement` is limited to 8,192 characters. Other inputs are `Parameters`, `ConsistentRead`, `Limit` (items evaluated), `NextToken` and `ReturnConsumedCapacity`. A SELECT processes at most 1 MB per call ([exec]).
- **When SELECT does not scan.** A SELECT avoids a full table scan only when WHERE has a partition-key `=` or `IN` ([PartiQL SELECT][qlsel]).
  - Not a scan: `OrderID = 100`, `OrderID = 100 AND Address = '...'`, `OrderID = 100 OR OrderID = 200`, `OrderID IN [...]`.
  - Full scan: `OrderID > 1`, `Address = '...'`, `OrderID = 100 OR Address = '...'`, and a SELECT with no WHERE.
- **Index targets.** Write `FROM "Table"."Index"`, with both names quoted ([qlsel]).
- **IAM guard.** IAM can deny scanning SELECTs with `dynamodb:FullTableScan` ([qliam]).
- **Statement set.** Only SELECT, UPDATE, INSERT and DELETE exist, plus batches and transactions ([PartiQL statements][qlstmt]).
  - UPDATE uses `SET` or `REMOVE`, optional `RETURNING`, and changes one item ([PartiQL UPDATE][qlupd]).
  - INSERT looks like `INSERT INTO T VALUE {...}` ([PartiQL batch][qlbatch]).
  - The DELETE syntax page was not read in detail.
- **Batches.** `BatchExecuteStatement` takes all reads or all writes, at most 25 statements ([qlbatch]).
- **Transactions.** `ExecuteTransaction` takes at most 100 statements, all reads or all writes. `EXISTS` is the one exception ([PartiQL transactions][qltx]).
- **Contributor Insights.** PartiQL requests are not recorded by Contributor Insights ([ci]).
- **design: refusal rules.**
  - Accept only a single statement whose first keyword is SELECT. Refuse everything else, including INSERT, UPDATE and DELETE.
  - Never call `BatchExecuteStatement` or `ExecuteTransaction`.
  - Always set `Limit`, and apply the sampling budget.
  - Comment syntax was not researched, so refuse any input that cannot be classified.
  - IAM remains the real barrier.

---

## 4. Risks worth flagging

| Risk | Facts | Signal (**design**) |
|---|---|---|
| Hot partition | Each partition maxes out at 3,000 read units and 1,000 write units per second ([partition design][pk], [burst]). Adaptive capacity raises a hot partition up to those maxima and can isolate hot items. It does not split item collections when the table has an LSI ([burst]). | CloudWatch `Read/WriteKeyRangeThroughputThrottleEvents`, which count requests throttled by partition limits ([metrics]). Contributor Insights most-throttled keys. `KeyRangeThroughputExceeded` during sampling. |
| Low-cardinality partition key | Status codes and dates rounded to a day, hour or minute are rated poor partition keys ([uniform load][pkuni]). A GSI keyed on status can be hot while the table is balanced ([GSI throttling][gsithrot]). | In the sample: distinct partition keys per item, and the share held by the top keys. Check each GSI partition key too. A sample cannot prove high cardinality. |
| Large items | 400 KB maximum, attribute names included. With an LSI, LSI entries count toward the 400 KB ([cons]). A 20 KB item costs 5 units per strongly consistent read, so about 600 reads/s per partition ([pk]). | Item size from the sample (UTF-8 name plus value bytes). The exact sizing of numbers, sets and nested types is unverified. Flag items over e.g. 100 KB. |
| LSI collection limit | 10 GB per partition-key value, counting table items and LSI entries ([lsi], [cons]). | An LSI is present and the partition key is skewed. |
| GSI back-pressure | A GSI short of capacity throttles writes to the base table. The reasons are `IndexWrite*`, and the ARN names the GSI ([gsithrot]). AWS advises GSI WCU at least equal to the table's ([gsi]). Contributor Insights does not count this kind of throttle ([ci]). | Provisioned GSI WCU below table WCU. Low cardinality of the GSI partition key in the sample. |
| Missing TTL on time-series data | TTL deletes expired items within a few days, at no WCU cost. The attribute must be a Number in epoch seconds, and other types are ignored ([TTL][ttl]). Values more than 5 years in the past are ignored ([computing TTL][ttlcomp]). | TTL disabled while a timestamp-like sort key or attribute grows monotonically in the sample. TTL enabled while sampled values are not N, or have 13 digits (likely milliseconds, an inference). |
| Provisioned without auto scaling | Auto scaling is an Application Auto Scaling target ([aasddb]). It triggers after 2 minutes over target and takes minutes to apply. A GSI added later is not auto-scaled while it backfills. AWS recommends scaling GSIs too ([auto scaling][as]). | A provisioned table or GSI with no scalable target. |
| Scans in application code | A Scan reads every item and can use up a table's throughput ([scan], [qlsel]). | Code search for Scan calls and for PartiQL without a partition-key equality, possibly reusing the docs/ORM-CAPTURE.md machinery. Whether CloudWatch separates Scan from Query per operation is unverified. |
| Other catalog flags | Deletion protection off, PITR disabled, status not `ACTIVE` ([td], [pitr]). | Read from the catalog. |

### 4.1 CloudWatch and Contributor Insights
- **Metrics.**
  - Namespace `AWS/DynamoDB` ([CloudWatch monitoring][cwmon]), with dimensions `TableName` and `GlobalSecondaryIndexName` ([metrics]).
  - These aggregate at 1 minute: consumed RCU and WCU, `ThrottledRequests`, `Read/WriteThrottleEvents`, the KeyRange, MaxOnDemand, Provisioned and AccountLimit throttle events, `ReturnedItemCount` and `TimeToLiveDeletedItemCount`. Other metrics aggregate at 5 minutes ([metrics]).
  - A GSI's provisioned-capacity metric needs both dimensions ([metrics]).
- **Contributor Insights modes.** The default is `ACCESSED_AND_THROTTLED_KEYS`. The other mode is `THROTTLED_KEYS` ([ci], [CI tutorial][citut]).
- **Rule names.** `DynamoDBContributorInsights-{PKC|PKT|SKC|SKT}-<resource>-<timestamp>` ([ci]). They are listed in `ContributorInsightsRuleList` ([citut]).
- **Report values.** `ConsumedThroughputUnits` = 3 x WCU + RCU. Reports return at most 25 contributors. Only `MaxContributorValue` and `Maximum` are meaningful ([ci]).
- **Enabling.** It needs `dynamodb:UpdateContributorInsights` and creates a service-linked role ([CI IAM][ciiam]). It is billed per rule and per event ([ci]). **design:** never enable it. Read reports only when status is `ENABLED`.
- **Privacy.** Key values are published to CloudWatch and visible to anyone with CloudWatch access, including keys protected by fine-grained access control ([ci]). **design:** treat reports as item data.
- **Permissions.** `cloudwatch:GetInsightRuleReport`, `dynamodb:DescribeContributorInsights` and `dynamodb:ListContributorInsights` ([ciiam]). `cloudwatch:GetMetricData` for metrics ([cwjson]).

---

## 5. DynamoDB Local

### 5.1 Image and setup
- **Docker Hub image.** The image is `amazon/dynamodb-local` ([Docker Hub][hub], [deploying Local][localdl]).
  - `3.3.2` and `latest` share index digest `sha256:6ada631ec7aa5fc83446855eb17233e3dad184fec47477e8140c6c569cfe0c86`.
  - Images exist for linux/amd64 and arm64, about 240 MB each, pushed 2026-10-15 ([tags][hubtags]).
- **ECR Public.** `public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local` is unverified. The gallery page was unreadable and the registry API needs a token.
- **Runtime.** The image bundles its dependencies ([hub]). The standalone jar, version 2.6.0 and later, needs JRE 17 or newer ([localdl]).
- **Options.** Default port 8000. Flags: `-inMemory`, `-sharedDb`, `-dbPath`, `-port`, `-delayTransientStatuses`, `-disableTelemetry`, `-version` ([local]).
- **Credentials.** Any values work, but `AWS_ACCESS_KEY_ID` must be letters and digits only ([local], [hub]). Without `-sharedDb` or `-inMemory`, the database file is named after the access key ID and Region ([local]).
- **Tests.** testcontainers-go has `modules/dynamodb` since v0.34.0, with `WithSharedDB` and `WithDisableTelemetry` ([testcontainers-go][tcgo]). dbscope already requires testcontainers-go v0.44.0.

### 5.2 Differences from the service

| Area | Local behaviour | Source |
|---|---|---|
| Capacity | Provisioned throughput is ignored, and UpdateTable is unlimited. | [local] |
| Billing mode | `BillingModeSummary` is always null. | [local] |
| Throttling | Not documented. Likely none, since capacity is ignored (inference, unverified). | |
| ItemCount and size freshness | Not documented (unverified). | |
| Table status | Always `ACTIVE`. GSIs pass through `CREATING` and `DELETING`. | [local] |
| Consistency | Eventually consistent, but usually looks strong. | [local] |
| Parallel scan | Segment and TotalSegments are ignored. | [local] |
| Index 1 MB page | Counts the full item. The service counts only projected attributes. | [local] |
| PartiQL | Supported. The usage notes say ExecuteStatement ignores `Limit` ([local]), but release 1.22.0 lists PartiQL Limit support ([localhist]). The two sources conflict, so test it. Batch and transaction support arrived in 2.5.0 ([localhist]). | [local], [localhist] |
| TTL | Undocumented whether DescribeTimeToLive works or items expire (unverified). | |
| Streams | Supported. Shard creation differs. `ShardFilter` since 3.2.0. | [local], [localhist] |
| PITR | Not supported. | [local] |
| Multi-attribute GSI keys | Since 3.3.0. | [localhist] |
| Deletion protection | Since 2.2.0. An UpdateTable bug was fixed in 3.2.0. | [localhist] |
| Vector indexes | Accepted on CreateTable and ignored. | [local] |
| Table names | Case-insensitive. The service is case-sensitive. | [local] |
| Regions, accounts, tags | Not distinguished. Tagging is unsupported. | [local] |
| Item collection metrics | Not tracked. | [local] |
| TransactionConflictException | Never thrown. | [local] |
| ReturnConsumedCapacity | Undocumented (unverified). | |
| CloudWatch, Contributor Insights, auto scaling | Not applicable to a local process (inference, unverified). | |

**design: test coverage.** Local is suitable for testing catalog parsing (including multi-attribute keys), pagination, type inference, PartiQL refusal, and endpoint and credential wiring. It is not suitable for cost, throttling, ItemCount freshness, parallel spread, PITR, billing mode or vector indexes.

---

## 6. AWS SDK for Go v2

### 6.1 Modules (seen 2026-10-28)

| Module | Version | Role | Source |
|---|---|---|---|
| github.com/aws/aws-sdk-go-v2 | v1.47.5 | `aws`, `aws/retry` | [px-core] |
| .../config | v1.33.12 | LoadDefaultConfig, profile, Region | [px-config] |
| .../credentials | v1.20.12 | Static provider for Local | [px-creds] |
| .../service/dynamodb | v1.72.2 | Client, paginators, types | [px-ddb] |
| .../feature/dynamodb/attributevalue | v1.21.16 | Go marshalling (optional) | [px-av] |
| .../service/cloudwatch | v1.75.2 | Optional metrics and CI reports | [px-cw] |
| .../service/applicationautoscaling | v1.51.7 | Optional auto scaling check | [px-aas] |
| github.com/aws/smithy-go | v1.28.7 | Transitive | [px-smithy] |

- **Go version.** The SDK needs Go 1.24 or later ([SDK README][sdkreadme]). dbscope declares `go 1.26.0`.
- **Dependencies of service/dynamodb.** Only the SDK core, internal configsources, endpoints/v2, accept-encoding, endpoint-discovery and smithy-go ([dynamodb go.mod][mod-ddb]).
- **Dependencies of config.** It adds credentials, ec2/imds, signin, sso, ssooidc and sts ([config go.mod][mod-config]).
- **Dependencies of attributevalue.** It adds dynamodbstreams ([attributevalue go.mod][mod-av]).
- **Raw types for inference.** `types.AttributeValue` is satisfied by `AttributeValueMemberB`, `BOOL`, `BS`, `L`, `M`, `N`, `NS`, `NULL`, `S` and `SS`, and `N` holds a string ([types pkg][types]). **design:** infer types from these unions, skip `attributevalue`, and keep numbers as strings, since DynamoDB numbers have 38 digits ([cons]).
- **CGO.** No primary source says the SDK is cgo-free. The go.mod files list only AWS and smithy modules, which suggests no cgo (unverified). Verify with `CGO_ENABLED=0 go build ./...`.
- **Binary size.** Not documented (unverified). Measure `go build -trimpath -ldflags="-s -w"` with and without the DynamoDB source.

### 6.2 Credentials and Region without stored secrets
- **Default chain.** In order: environment variables (static keys or a web identity token file), then the shared credentials and config files, then the ECS task role, then the EC2 instance role ([configure SDK][gocfg]).
- **Profile and Region.** `config.WithSharedConfigProfile(name)` selects a profile and `config.WithRegion(r)` sets the Region. There is no default Region ([gocfg]).
- **SSO and console login.** SSO profiles use `sso_session` with `aws sso login`, and the SDK refreshes credentials until the SSO session expires. Console login through `aws login` also works ([gocfg]).
- **Environment override.** Environment-variable credentials always override the selected profile ([gocfg]). **design:** detect and warn.
- **credential_process.** It runs an external program, and anyone who can edit the config file controls it ([process credentials][sdkproc]).
- **design: dbscope profile.**
  - Store `engine: dynamodb`, `aws_profile` (a name), `region` and an optional `endpoint`.
  - Never store keys, tokens or SSO caches.
  - Show the account and ARN from `sts:GetCallerIdentity` ([sts]). `service/sts` is already a config dependency ([mod-config]).
- **design: DynamoDB Local.** Use `credentials.NewStaticCredentialsProvider` with dummy alphanumeric values, only for loopback endpoints. AWS says static credentials are for testing only ([gocfg]).

### 6.3 Custom endpoint for DynamoDB Local
- **Client option.** `dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String("http://localhost:8000") })` ([configure endpoints][goep], [local]).
- **Environment and profile settings.** `AWS_ENDPOINT_URL`, `AWS_ENDPOINT_URL_DYNAMODB` and the profile's `endpoint_url` also redirect traffic, with the service-specific setting winning. An endpoint set explicitly on the client always wins. `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS` disables the environment and file settings ([service-specific endpoints][sdkep]).
- **design:** always show the effective endpoint, and warn when one came from the environment or profile. The matching Go `config` option name was not checked.

---

## Facts dbscope must encode

- [ ] KeySchema is a list. A GSI can have up to 4 HASH and 4 RANGE attributes. Query needs every HASH by equality and RANGE attributes left to right, with any inequality last ([gsi]).
- [ ] Key types are only S, N or B. Other types come from sampling ([gsi], [lsi]).
- [ ] ItemCount, TableSizeBytes and IndexSizeBytes are about 6 h stale ([td]).
- [ ] BillingModeSummary may be absent, and is always null on Local ([bms], [local]).
- [ ] ProvisionedThroughput is 0 on on-demand tables. OnDemandThroughput -1 means no cap ([ptd], [odt]).
- [ ] No index details are returned while the table is DELETING. Only ACTIVE targets can be queried ([td], [queryapi]).
- [ ] ListTables returns at most 100 names per page. Follow `LastEvaluatedTableName` ([lt]).
- [ ] Rate limits: DescribeTimeToLive 10 RRU/s, DescribeContinuousBackups 10/s, Contributor Insights calls 5 rps, DescribeTable and ListTables 2,500 rps shared ([cons], [dcb]).
- [ ] Read unit is 4 KB: eventually consistent costs 0.5, strongly consistent 1, transactional 2 ([rw]).
- [ ] Scan and Query bill items before the filter. Projection and COUNT do not reduce cost ([scan], [rw]).
- [ ] 1 MB per page. Limit counts items evaluated. Only a missing LastEvaluatedKey ends pagination. Empty pages can carry a key ([scan], [queryapi]).
- [ ] LSI scans use base-table RCU. GSI scans use GSI RCU ([scan]).
- [ ] Segments are partition-key hash buckets: 0 ≤ Segment < TotalSegments ≤ 1,000,000. Local ignores them ([scan], [scanapi], [local]).
- [ ] Request ReturnConsumedCapacity=TOTAL and enforce a client-side budget (**design**, based on [scan]).
- [ ] Sort-key operators are `=`, `<`, `<=`, `>`, `>=`, `BETWEEN` and `begins_with`. `begins_with` is not allowed on Number keys ([kce], [queryapi]).
- [ ] A Query filter cannot use key attributes ([qfe]).
- [ ] A GSI cannot fetch, cannot be read strongly consistent, and may be sparse. An LSI can fetch at full-item cost ([gsi], [lsi], [queryapi]).
- [ ] A PartiQL SELECT scans unless WHERE has a partition-key `=` or `IN`. No WHERE means a full read ([qlsel]).
- [ ] PartiQL has only SELECT, INSERT, UPDATE and DELETE. Refuse everything except a single SELECT. Never use the batch or transaction APIs ([qlstmt]).
- [ ] IAM: allow PartiQLSelect, deny PartiQL writes, and deny `dynamodb:FullTableScan` ([qliam], [iam]).
- [ ] Throttle reason = Table|Index + Read|Write + one of four limit types. The ARN can name a GSI while the write was to the table ([treason], [gsithrot]).
- [ ] Partition maximum is 3,000 read units/s and 1,000 write units/s ([pk]).
- [ ] Items are limited to 400 KB including names. With an LSI, LSI entries count too, and there is a 10 GB collection limit ([cons]).
- [ ] TTL needs a Number in epoch seconds. Other types and values more than 5 years old are ignored. Deletion happens within days ([ttl], [ttlcomp]).
- [ ] Auto scaling lives in Application Auto Scaling (`dynamodb`, `table/T`, `table/T/index/I`), not DescribeTable ([aasddb]).
- [ ] Never enable Contributor Insights. Treat its output as key data. Only Max statistics are meaningful, up to 25 contributors, and PartiQL is not counted ([ci], [ciiam]).
- [ ] Environment credentials override the profile. `credential_process` runs a program. Environment and profile endpoints can redirect traffic ([gocfg], [sdkproc], [sdkep]).
- [ ] A dbscope profile stores only the AWS profile name, Region and optional endpoint. Local needs dummy alphanumeric keys ([local]).
- [ ] Keep N values as strings (38 digits) ([types], [cons]).
- [ ] The Go standard retryer makes 3 attempts. Lower it for sampling, and never pass 0 to AddWithMaxAttempts ([goretry]).

## Sources

[td]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TableDescription.html
[lt]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ListTables.html
[bms]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_BillingModeSummary.html
[ptd]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ProvisionedThroughputDescription.html
[odt]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_OnDemandThroughput.html
[ss]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_StreamSpecification.html
[tcs]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TableClassSummary.html
[ttld]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TimeToLiveDescription.html
[dcb]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DescribeContinuousBackups.html
[cbd]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ContinuousBackupsDescription.html
[pitr]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_PointInTimeRecoveryDescription.html
[scanapi]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Scan.html
[queryapi]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html
[exec]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ExecuteStatement.html
[treason]: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ThrottlingReason.html
[iam]: https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html
[iamjson]: https://servicereference.us-east-1.amazonaws.com/v1/dynamodb/dynamodb.json
[aasjson]: https://servicereference.us-east-1.amazonaws.com/v1/application-autoscaling/application-autoscaling.json
[cwjson]: https://servicereference.us-east-1.amazonaws.com/v1/cloudwatch/cloudwatch.json
[qliam]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-iam.html
[cons]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html
[quotas]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ServiceQuotas.html
[scan]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Scan.html
[rw]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/read-write-operations.html
[kce]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html
[qfe]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.FilterExpression.html
[gsi]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/GSI.html
[lsi]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/LSI.html
[qlsel]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.select.html
[qlstmt]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.statements.html
[qlupd]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.update.html
[qlbatch]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.multiplestatements.batching.html
[qltx]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.multiplestatements.transactions.html
[pk]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/bp-partition-key-design.html
[pkuni]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/bp-partition-key-uniform-load.html
[burst]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/burst-adaptive-capacity.html
[throt]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/TroubleshootingThrottling.html
[gsithrot]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/gsi-throttling.html
[metrics]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/metrics-dimensions.html
[cwmon]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Monitoring-metrics-with-Amazon-CloudWatch.html
[ci]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/contributorinsights_HowItWorks.html
[citut]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/contributorinsights_tutorial.html
[ciiam]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Contributor_Insights_IAM.html
[ttl]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/TTL.html
[ttlcomp]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/time-to-live-ttl-before-you-start.html
[as]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/AutoScaling.html
[aasddb]: https://docs.aws.amazon.com/autoscaling/application/userguide/services-that-can-integrate-dynamodb.html
[local]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html
[localdl]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.DownloadingAndRunning.html
[localhist]: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocalHistory.html
[hub]: https://hub.docker.com/r/amazon/dynamodb-local
[hubtags]: https://hub.docker.com/v2/repositories/amazon/dynamodb-local/tags?page_size=10
[tcgo]: https://golang.testcontainers.org/modules/dynamodb/
[sts]: https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html
[sdkreadme]: https://github.com/aws/aws-sdk-go-v2/blob/main/README.md
[gocfg]: https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html
[goep]: https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html
[goretry]: https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-retries-timeouts.html
[sdkretry]: https://docs.aws.amazon.com/sdkref/latest/guide/feature-retry-behavior.html
[sdkep]: https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html
[sdkproc]: https://docs.aws.amazon.com/sdkref/latest/guide/feature-process-credentials.html
[types]: https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/dynamodb/types
[ddbpkg]: https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/dynamodb
[px-core]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/@latest
[px-config]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/config/@latest
[px-creds]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/credentials/@latest
[px-ddb]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/service/dynamodb/@latest
[px-av]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue/@latest
[px-cw]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/service/cloudwatch/@latest
[px-aas]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/@latest
[px-smithy]: https://proxy.golang.org/github.com/aws/smithy-go/@latest
[mod-ddb]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/service/dynamodb/@v/v1.72.2.mod
[mod-config]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/config/@v/v1.33.12.mod
[mod-av]: https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue/@v/v1.21.16.mod
