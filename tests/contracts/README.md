# T00-05 · Contract tests

This folder provides **executable contract fixtures**, not a running Agent or a production database migration.

## Run offline after dependencies are installed

From a local checkout of EasyHostAgent:

~~~bash
python3 -m pip install "jsonschema>=4.20,<5"
python3 -m unittest discover -s tests/contracts -p "test_*.py" -v
~~~

The tests need no network access, Docker daemon, database server, EasyTier account, or FnOS machine. They validate:

- The OpenAPI v1 JSON schemas, API endpoint security headers and seven JSON examples from docs/api/contract-v1.md.
- Docker inventory fixtures: portainer.json (1-container Compose), sub2api.json (3-container Compose), standalone.json (single external container), and docker-error.json (non-authoritative failed scan).
- The **entire** docs/schema/T00-04-proposed.sql, applied to an **in-memory miniature legacy schema** with SQLite foreign keys turned on. Tests cover device archive/Agent unbind, no cascading loss of services, tasks or apps, tenant separation, duplicate app/Agent constraints, and service-link exclusivity.

## Deploy repository (separate private repo)

From a local checkout of CiBeDerORG/deploy:

~~~bash
python3 -m pip install "PyYAML>=6,<7" "jsonschema>=4.20,<5"
python3 -m unittest discover -s tests/contracts -p "test_*.py" -v
~~~

The Deploy tests validate its **own source files**, not a copied private repository snapshot: JSON Schema Draft 2020-12, schema/examples/*.app.yaml, declared scripts and lib/ dependencies, Compose project/service names, published host ports, data bind mounts and global .env.example / .secrets.example variable ownership. Tests also ensure unsafe paths, unknown manifest properties and plaintext secret defaults are rejected.

## Boundaries

These are schema and SQLite **contract tests**, not execution tests for Go, Rust, Docker, bash, Linux systemd, GitHub credential rotation, HTTP TLS or installation. Real Agent APIs, migration into EasyTier's production SeaORM migrator, and the actual installation of Portainer/Sub2API remain later tasks.

No test must remove or restart real containers; never run an auto-rollback process. A failed task retains its local state and logs per product requirements.
