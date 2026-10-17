# Test database and timing hygiene

Database fixtures use private per-test databases and per-process migration
templates. Ordinary test cleanup removes them. An interrupted process can leave
its databases behind; automatic cleanup must distinguish abandoned fixtures from
other users' databases and live templates.

The fixture cleanup policy is shared without adding PostgreSQL dependencies to
the contracts module. A candidate needs an exact helper name and a versioned
helper marker whose database identity, creation time, owner and administration
scope all agree. Age alone and the `cx_` prefix are insufficient. The default age is 60 minutes;
`BLERG_TEST_DB_MAX_AGE_MINUTES` accepts whole values from 1 to 10080. Invalid
configuration disables sweeping. Unmarked older
fixtures and manually created test administration databases remain untouched. A
process cut off between `CREATE DATABASE` and its marker write can leave an
unmarked fixture; this is deliberately left for manual inspection.

Live fixture runs hold a lease on an administration connection. This protects a
template even while it has no connections of its own, which PostgreSQL requires
when cloning. Sweeping revalidates identity after acquiring a free lease and uses
a normal drop, without force, so a late connection still blocks deletion.

The timing tests use a service clock for OAuth token expiry and virtual time for
the Codex protocol reader's timeout. Real fake-process integration coverage stays
in place. The idle shutdown regression is also run 100 times with the race
detector while two CPU workers share the same two CPUs with the test process.

The idle regression passed all 100 race-detector repetitions in 113.61 seconds;
both load workers remained active throughout. All four modules passed full tests and vet with `GOWORK=off`, without `-p 1`.
The suites ran concurrently; these wall times are validation results, not a
controlled performance comparison.

| Module | Full test wall time |
|---|---:|
| Contracts | 3.37 s |
| Core | 95.01 s |
| Runner | 233.47 s |
| Board | 113.69 s |

All three database helpers passed PostgreSQL race tests twice; shared policy
races passed, and independent sweeper safety tests passed three repetitions.
Full OAuth races passed 119 tests with no skips; focused expiry and Codex
protocol/cancellation races passed ten repetitions. Independent negative controls
confirmed that removing the administrator-scope guard permits the deletion the
guard prevents. Pinned lint across all modules, scrub and link checks passed.
No real providers, owner daemon or services were used.
