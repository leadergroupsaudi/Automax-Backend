# EO Platform — Deploying

Companion to `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md`.
It covers one thing: what to check on a database **before** starting a server
built from this branch.

## Why there is a pre-deploy check at all

Two changes made during the Plan 1 fix wave turn conditions that used to be
silent into conditions that **stop the server from starting**. Both are
deliberate — each was a condition under which the platform looked healthy and
was not — but both mean a boot that used to "succeed" may now fail loudly on a
database that has been through an intermediate commit of this branch.

1. **`migrations.MigrateEOPlatform`'s error is now returned, not logged.**
   `internal/database/postgres.go` used to write `Warning: EO platform
   migration failed: …` and carry on. The migration is built to fail closed —
   its `relkind` guard, `assertPartitionKey` and `assertColumns` all return
   errors precisely so that production and the migration file cannot drift
   unnoticed — so a warning defeated the entire mechanism. The server now
   refuses to start if any of them fires.

2. **`eo_record_links` now carries `uq_eo_record_links_pair_type`**, a unique
   index on `(from_record_id, to_record_id, link_type_key)`. The duplicate
   check used to be application-level only, outside any transaction, so two
   concurrent `POST /eo-records/:id/links` could both write. Creating the index
   fails if duplicates already exist — which, combined with (1), means the
   server will not start until they are removed.

## The check

Run these against the target database **before** deploying. All four are
read-only.

```sql
-- 1. eo_records must be a PARTITIONED table ('p'), not a plain one ('r').
--    'r' means AutoMigrate created it: it has no composite primary key and no
--    per-partition UNIQUE (record_type_id, reference), so reference
--    uniqueness has not been enforced. Nothing is empty here: the table has to
--    be migrated by hand before this build can start.
SELECT relkind FROM pg_class WHERE oid = to_regclass('eo_records');

-- 2. ... and partitioned exactly this way.
SELECT pg_get_partkeydef(to_regclass('eo_records'));   -- expect: LIST (record_type_id)

-- 3. Duplicate links, which block the new unique index. Expect zero rows.
SELECT from_record_id, to_record_id, link_type_key, count(*)
FROM eo_record_links
GROUP BY 1, 2, 3
HAVING count(*) > 1;

-- 4. Is the EO seed marker present? The marker IS the base record type, and
--    the whole seed is one transaction, so this answers "is the platform
--    configured" exactly.
SELECT count(*) FROM eo_record_types WHERE key = 'eo_base';
```

If `eo_records` does not exist at all (`to_regclass` is NULL), this is a fresh
database and there is nothing to check: the migration creates everything.

### If (3) returns rows

Delete the surplus rows, keeping the oldest of each group, then redeploy:

```sql
DELETE FROM eo_record_links a
USING eo_record_links b
WHERE a.from_record_id = b.from_record_id
  AND a.to_record_id   = b.to_record_id
  AND a.link_type_key  = b.link_type_key
  AND (a.created_at, a.id) > (b.created_at, b.id);
```

### If (4) returns 0 on a database that has served EO traffic

The seed rolled back at some point and the platform is **absent**, not
partially present — the seed is one transaction, so there is no half-written
state to repair. Check the startup log for the three lines beginning
`EO platform seed FAILED` / `EO CONFIGURATION WAS NOT APPLIED`; the first names
the underlying error.

The cause is almost always a **leftover row from an intermediate commit of this
branch** colliding with one the seed is trying to write. The seed's marker row
is the base record type and is written first inside the transaction, so a
database that got partway through an older version of the seed and then failed
has no marker, retries the whole seed on the next boot, and collides again —
forever, and quietly, until this was made loud. Look for a pre-existing row
with one of the keys the seed writes:

```sql
SELECT key  FROM eo_record_types WHERE key LIKE 'eo_%';
SELECT code FROM permissions     WHERE code LIKE 'eo:%';
SELECT code FROM workflows       WHERE record_type = 'eo';
SELECT key  FROM eo_link_types;
```

Any of those present while `eo_base` is absent is a half-applied older seed.
Removing the leftovers lets the next boot seed the platform cleanly.

## The two switches

`EO_PLATFORM` and the `eo_platform` licence are independent, and each gates a
different half:

| | `EO_PLATFORM` | `eo_platform` licence |
|---|---|---|
| seeds types, workflows, permissions | yes | no |
| serves `/api/v1/eo-records` and the designer | no | yes |
| runs the durable-timer worker | no | yes |

Setting one without the other is legal and is sometimes what an operator wants
while staging a rollout, so neither is an error — but the server now says so at
startup (`cmd/server/main.go`, `eoWarnSwitchDisagreement`). A licence without
`EO_PLATFORM` serves the EO routes over an unseeded platform; `EO_PLATFORM`
without the licence seeds configuration nobody can reach.

`EO_TIMER_INTERVAL_SECONDS` (default 30) is read by `config.Load` alongside
them and sets the timer worker's poll interval.

## Per-type partitions

Plan 1 creates `eo_records` and its `DEFAULT` partition only. **No per-type
partition is ever created**, and every record of every type lives in
`eo_records_default`. This is fully functional — see spec §7.7 for why
promoting a type to its own partition after it has records is a one-way door
that needs a data move under lock, and why Plan 2 must create each type's
partition before that type's first record.
