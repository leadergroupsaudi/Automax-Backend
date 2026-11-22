# Task C1 report: EO Documenta configuration and the folder column

## Built
- `config.Config.EODocumenta` (type `DocumentaConfig`), env `EO_DOCUMENTA_{BASE_URL,CLIENT_ID,CLIENT_SECRET,WORKSPACE_NAME,ENABLED}`. Every default is empty/false: no inherited URL, no inherited workspace, off. Also listed in `.env.example`.
- `models.EORecord.DocumentaFolderID string` -> `eo_records.documenta_folder_id VARCHAR(100) NOT NULL DEFAULT ''`.
- Migration: column added to the CREATE TABLE (fresh DBs), `ALTER TABLE eo_records ADD COLUMN IF NOT EXISTS ...` inside `recordsSQL` (existing DBs), and `{"documenta_folder_id","character varying",100,false}` in `eoRecordsColumns`.
- `cmd/server/main.go`: EO client constructed beside the other two (HTTP if `cfg.EODocumenta.Enabled`, else `storage.NewStubDocumentaClient()`).
- `TestEOWiringUsesSLAConstructors` now also pins `storage.NewHTTPDocumentaClient(cfg.EODocumenta,` and `cfg.EODocumenta.Enabled`.

## Required statements
1. **Drift assertion and preflight.** The migration passes its own `assertColumns` with the new column, on a fresh schema and on an upgraded one (`TestMigrateEOPlatformStillPassesAssertColumns` drops the column from a populated table, re-runs the migration, re-runs it again). `EOPreflightSQL()` renders from `eoAssertedTables`, so the operator's generated pre-deploy check covers the column; the test asserts the generated SQL contains `'documenta_folder_id'`. `docs/eo-platform-deploying.md` was not edited (it embeds nothing about this column; the query is generated).
2. **Nullability.** NOT NULL, default `''`. Empty string means "this record has no folder": records created before C2, and every record the pilot's tests make. Same convention as `priority`/`sensitivity`/`source` on this table; Go side is a plain `string`, not a pointer, so no scan can fail on it. Nothing may treat `""` as a folder id; the field comment says so. Existing rows are back-filled to `''` by the ADD COLUMN default (tested).
3. **The EO Documenta client is constructed but NOT consumed.** Nothing takes `eoDocumentaClient` until C2. In main.go it is held with `_ = eoDocumentaClient` because Go otherwise rejects the unused variable. I am not claiming it is wired. What does read the config: main.go reads `cfg.EODocumenta.Enabled` and passes `cfg.EODocumenta` to the HTTP constructor. C2 must add the client as a constructor argument to the record service (I did not touch `internal/services`).
4. **Mutations (single-line change -> test that fails).**
   - `TestEORecordsHasADocumentaFolderColumn`: delete the `{"documenta_folder_id", ...}` entry from `eoRecordsColumns` (migration then errors on the undeclared column); or make the column nullable (drop `NOT NULL` in the CREATE/ALTER). Both verified failing.
   - `TestMigrateEOPlatformStillPassesAssertColumns`: delete the `ALTER TABLE eo_records ADD COLUMN IF NOT EXISTS documenta_folder_id` line (verified); also fails on the two mutations above.
   - `TestEODocumentaConfigIsSeparateFromTheOthers`: change `getEnvAsBool("EO_DOCUMENTA_ENABLED", false)` to `"DOCUMENTA_ENABLED"` (fails the "default off" and "reads its own variables" subtests, verified) or to default `true` (fails "default off", verified). The "other blocks unaffected" subtest has no mutation of its own that the first two do not already catch, and I did not find one that only it catches (e.g. pointing Documenta's ClientID key at EO_ is not exercised by it either unless EO_DOCUMENTA_CLIENT_ID is set, which it is; I did not run that variant).
   - Main.go pin (extra): change `cfg.EODocumenta` to `cfg.Documenta` in the constructor line; `TestEOWiringUsesSLAConstructors` fails (verified).

## TDD evidence
- RED: `go test ./internal/config ./internal/repository -run 'Documenta|StillPasses'` -> build failures: `cfg.EODocumenta undefined`, `DocumentaFolderID undefined`.
- GREEN (with `AUTOMAX_TEST_DB_DSN` set): `go test ./internal/config ./internal/repository ./internal/handlers ./internal/database/...` all ok; `go build ./...` clean. `go build ./cmd/server` clean.

## Design choices the brief left open
- Column type VARCHAR(100) (Documenta ids are UUID strings; matches table's other string columns). Column placed before created_at in the CREATE; on upgraded DBs it is last, which the assertion (by name) does not care about.
- Config defaults empty, not the KPI default `https://mydocs.axionic.io`: an inherited real host is exactly the silent placement mistake the addendum describes. Consequence: `EO_DOCUMENTA_ENABLED=true` with no `BASE_URL` builds an HTTP client aimed at "/api/v1" and fails at first request. I did not add a boot guard; C2 decides what a folder failure means.
- Passed `nil` as the Redis client to the EO HTTP client (see concern 1).
- Did not add a shape-drift case for the new column to `TestMigrateEOPlatformDetectsShapeDrift`; the upgrade test covers it more strongly.

## Coverage (measured: `go test ./internal/repository -coverpkg=./internal/database/migrations`)
- `EOPreflightSQL` 100%; `MigrateEOPlatform` 76.3%. The only uncovered block near my change is the pre-existing `recordsSQL` exec-error return (eo_platform.go ~:148), unreachable without a broken connection. The ALTER is inside the executed SQL string.
- Not executed anywhere: the new main.go branches (HTTP vs stub client). No test runs main; the text assertion pins the HTTP line only. The `Enabled=true` path and stub path both compile; neither was run.
- Config: new block fully exercised by the three subtests.

## Concerns
1. **Pre-existing, found while reading:** `storage/documenta_http.go` uses constant Redis keys `documenta:oauth_token` and a folder cache key `workspaceName:parent:name` with no client identity. If Goals and KPI Documenta are both enabled with different credentials, they already share a cached token. This is why the EO client gets nil Redis (in-memory token only, no cross-client folder cache). Worth a separate ticket; I did not change it.
2. `.env` is modified locally (not by me) and was not staged. I briefly deleted and restored the tracked `server` binary while build-testing; it is restored, unchanged.
3. The "other blocks unaffected" subtest mutation note above.
