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
   migration failed: …` and carry on. The migration is built to fail closed, so
   a warning defeated the entire mechanism. The server now refuses to start if
   **any** of its guards fires, and there are three kinds:

   - the `relkind` guard — `eo_records` exists but is not a partitioned table;
   - `assertPartitionKey` — it is partitioned, but not by `record_type_id`;
   - `assertColumns` — a table's live columns do not match what the migration
     declares. **This one runs on six tables**: `eo_records`,
     `eo_record_links`, `eo_record_history`, `eo_reference_sequences`,
     `eo_timers` and `eo_escalation_events`.

   Every `CREATE TABLE` in that file is `IF NOT EXISTS`, so on a database that
   already has a table, editing a column list up there changes nothing —
   `assertColumns` is the only thing that notices. That is exactly the shape of
   an upgrade from an intermediate commit of this branch: a column changed
   mid-branch, the table is already present, the create is a no-op, and the
   assertion fires at boot.

2. **`eo_record_links` now carries `uq_eo_record_links_pair_type`**, a unique
   index on `(from_record_id, to_record_id, link_type_key)`. The duplicate
   check used to be application-level only, outside any transaction, so two
   concurrent `POST /eo-records/:id/links` could both write. Creating the index
   fails if duplicates already exist — which, combined with (1), means the
   server will not start until they are removed.

## The check

Five things to establish. All are read-only; none writes or locks anything.
**Run all five**: each covers a different guard, and a clean result from one
says nothing about the others.

### 1 — the six tables' columns

This is the guard a hand-written query cannot cover honestly, because the
expected column list lives in Go. So the query is **generated from the very
slices `assertColumns` compares against**, by `migrations.EOPreflightSQL`.
There is no second copy of the list to drift, which is the failure this whole
mechanism exists to prevent.

```sh
# Run it directly. Exit 0 = clear, 1 = the deployment would fail, 2 = could not connect.
go run ./cmd/eo-preflight -dsn "postgres://user:pass@host:5432/dbname?sslmode=disable"

# Or print the SQL and run it however you like — it takes no parameters.
go run ./cmd/eo-preflight | psql "$DSN"
```

**Zero rows / `OK` means no column assertion will fail at boot.** Any row names
the table, the column, what the database has and what this build expects, in
the same wording the startup error would use:

```
eo_timers.capability_key is character varying(40) NOT NULL; expected character varying(100) NOT NULL
eo_escalation_events.stray_column exists in the database but is not declared in this migration
```

A table that does not exist yet is deliberately not reported: the migration
creates it, so a fresh database is silent rather than a wall of noise.

If the Go toolchain is not available on the target host, run
`go run ./cmd/eo-preflight` anywhere with the source and paste the printed SQL
into a psql session on the target. The SQL is self-contained and schema-
relative (`current_schema()`).

Each row is resolved by writing an `ALTER TABLE` migration, **or** by
correcting the expected column list in
`internal/database/migrations/eo_platform.go` — whichever of the two is
actually the mistake. Do not simply edit the expected list to match a drifted
production table without first establishing why they differ; that list is the
checksum, and editing it to agree with whatever is there defeats it.

### 2 — partitioning

```sql
-- eo_records must be a PARTITIONED table ('p'), not a plain one ('r').
SELECT relkind FROM pg_class WHERE oid = to_regclass('eo_records');

-- ... and partitioned exactly this way.
SELECT pg_get_partkeydef(to_regclass('eo_records'));   -- expect: LIST (record_type_id)
```

If `to_regclass` is NULL, `eo_records` does not exist: this is a fresh database
and the migration creates it correctly. Nothing to do.

**If `relkind` is `'r'`** the table is a plain, unpartitioned one. It has no
composite primary key and no per-partition
`UNIQUE (record_type_id, reference)`, so reference uniqueness has never been
enforced on it. There is no in-place upgrade — `PARTITION BY` can only be given
at creation — so fixing it is a hand migration: create the partitioned table
under a new name with the shape in `eo_platform.go`, create its `DEFAULT`
partition, resolve any duplicate `(record_type_id, reference)` pairs (the new
constraint will reject them), `INSERT INTO … SELECT` the rows, drop
`eo_record_links`' and `eo_record_history`'s foreign keys, swap the names, and
recreate the foreign keys. That is a maintenance window, not a deploy step.

**This should not happen on this branch.** The only thing that creates a plain
`eo_records` is AutoMigrate, and `models.EORecord` has never been in the
AutoMigrate list in `internal/database/postgres.go` — its doc comment says so,
the model's own comment says so, and
`repository.TestMigrateEOPlatformRejectsANonPartitionedTable` covers the guard.
Treat an `'r'` here as evidence that something outside this branch created the
table, and find out what, before migrating it.

### 3 — duplicate links

```sql
-- Blocks the new unique index. Expect zero rows.
SELECT from_record_id, to_record_id, link_type_key, count(*)
FROM eo_record_links
GROUP BY 1, 2, 3
HAVING count(*) > 1;
```

If it returns rows, delete the surplus, keeping the oldest of each group, then
redeploy:

```sql
DELETE FROM eo_record_links a
USING eo_record_links b
WHERE a.from_record_id = b.from_record_id
  AND a.to_record_id   = b.to_record_id
  AND a.link_type_key  = b.link_type_key
  AND (a.created_at, a.id) > (b.created_at, b.id);
```

### 4 — the escalation-event unique index

```sql
-- uq_eo_escalation_events_step blocks on existing duplicates the same way.
-- Expect zero rows.
SELECT record_id, state_id, policy_id, step_index, breached_at, count(*)
FROM eo_escalation_events
GROUP BY 1, 2, 3, 4, 5
HAVING count(*) > 1;
```

Only relevant on a database where the table predates that index. De-duplicate
the same way as (3) if it returns anything.

### 5 — is the platform seeded?

```sql
-- The marker IS the base record type, and the whole seed is one transaction,
-- so this answers "is the platform configured" exactly.
SELECT count(*) FROM eo_record_types WHERE key = 'eo_base';
```

This one does not block the boot; it tells you whether the EO configuration is
present at all.

**If it returns 0 on a database that has served EO traffic**, the seed rolled
back at some point and the platform is **absent**, not partially present — the
seed is one transaction, so there is no half-written state to repair. Check the
startup log for the three lines beginning `EO platform seed FAILED` /
`EO CONFIGURATION WAS NOT APPLIED`; the first names the underlying error.

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
them and sets the timer worker's poll interval. A value that is not a number,
and one that is zero or negative, both fall back to 30 — and both are named in
a startup warning, so a typo is not silent.

## Per-type partitions

Plan 1 creates `eo_records` and its `DEFAULT` partition only. **No per-type
partition is ever created**, and every record of every type lives in
`eo_records_default`. This is fully functional. See spec §7.7 for why promoting
a type to a partition of its own is a one-way door, and why it is a maintenance
operation on a populated register even for a type that holds no records of its
own.
