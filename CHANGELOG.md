# Changelog

Each section is a versioned component. A version appears in the scan or map
output, so it changes whenever that component's output changes. Newest first.

## Extractor (pipeline)

- 0.4.0: the release version of the binary, which a release tag must equal.
  The extractor finds the same facts as 0.3.0. Only the version in the scan,
  and the hashes that follow from it, change. The plugins this release adds or
  changes (.NET, the local viewer) carry their own versions below.
- Unversioned: `cernly sbom` reads a single-string `roots` attribute as a
  one-item list, so a transitive dependency written that way now reaches its
  service. The SBOM has no version of its own and the scan output does not
  change, so the extractor version stays 0.3.0.
- 0.3.0: the commands plugin reads sourced files and arrays built by functions.
- 0.2.0: byte order marks are stripped, `max_file_bytes` limits file size,
  files are never read through symlinks, only tracked files in a subfolder are
  read, and the scan records whether the working tree is dirty.

## Rules (map)

- 0.17.2: production metrics look a stack's role up by its full key, so two
  stacks of the same name in different kinds (helm and kubernetes) keep their
  own role.
- 0.17.1: an endpoint row whose registrations disagree on anonymous access
  (auth `unknown` with an `AllowAnonymous` marker) gets no fallback policy.
- 0.17.0: a link or CI image whose recipe builds .NET entrypoints is narrowed
  to their project closure (`narrowing`, `narrowed` and `excluded` on
  `links[]` and `ci_images[]`, precision `entrypoint`). `endpoints[]` lists one
  row per endpoint and service, with its auth state and the routes that reach
  it; the `endpoint_anonymous_public` finding; `rewrite` and `match_type` on
  `routes[]`; `metrics.endpoints`. The production route count matches a
  route's stack by name, so it counts routes it missed before.
- 0.16.0: a dotnet `test_run` with `projects` (a solution's list) covers only
  the suites at those folders.
- 0.15.0: .NET support. NuGet drivers and SDK names match case-insensitively, a
  NuGet SDK name ending in `.*` matches its package family, and compose and
  kubernetes look an engine up by its full image path first (`mssql/server`).
- 0.14.0: `image_pinning` findings name the image repository, and the gate
  reads a new tag of a repository that was already unpinned in the base as an
  upgrade.
- 0.13.0: `driver_without_store` reports a database driver with no data store
  of its engine described. A driver confirmation confirms or dismisses it.
- 0.12.0: only a job whose command starts the stack counts toward production.
  A job that only renders or validates it (`helm template`, `kustomize build`,
  `--dry-run`) gives none.
- 0.11.0: the production rule reads `if:` as expressions per trigger and push
  branch, and reads tag and ignore filters as GitHub does. Party grading needs
  two facts.
- 0.10.0: metrics name what each count counts (`items`), so a report can trace
  every number to its facts.
- 0.9.0: `tests[]` lists the test suites per service and the commands that run
  them.
- 0.8.0: surface metrics with size as context, the module graph built from
  import facts, and its coupling.
- 0.7.0: gate rules `new_transitive_dependency` (off by default) and
  `licence_not_allowed`.
- 0.6.0: transitive dependencies are never attached to a service and are never
  an SDK signal.
- 0.5.0: tables are merged across schema sources, with sensitive-looking column
  names flagged for review.
- 0.4.0: stack confirmations made by a caller are applied.
- 0.3.0: images built in CI are boxes of their own.
- 0.2.0: test jobs are never production.

## Plugin agent-config

- 0.1.0: first version.

## Plugin caddy

- 0.2.0: `match_type` and `rewrite` on routes.
- 0.1.1: a repeated route keeps the first upstreams. Import cycles and URL
  credentials are handled.
- 0.1.0: first version.

## Plugin commands

- 0.3.3: a `dotnet test` of a tracked `.sln` or `.slnx` records the folders
  of the projects it lists (`projects`) on the test run.
- 0.3.2: a stack whose last compose file is an override file (`compose.override.yml`)
  is named `default`, as compose names it.
- 0.3.1: `dotnet test` is a test run (dotnet), with its directory taken from a
  project or solution argument. `--list-tests` alone gives none.
- 0.3.0: files read with `source`, path variables derived from the script's
  location, arrays set by functions and copied, wrapper chains, conditional
  files, and assumptions on stacks.
- 0.2.1: a literal `cd` in a workflow step moves later paths. The `workflows[]`
  entry of a stack says whether the job starts it; `--dry-run` does not.
- 0.2.0: workflow references carry ignore and tag filters and step conditions.
  Folded `run:` lines are read. URL credentials are dropped.
- 0.1.0: first version.

## Plugin compose

- 0.1.2: the engine is looked up by its full image path (`mssql/server`).
- 0.1.1: image references are matched in one pass and nesting is capped.
- 0.1.0: first version.

## Plugin deps-go

- 0.2.0: `// indirect` requirements are reported as transitive dependencies.
- 0.1.0: first version.

## Plugin deps-npm

- 0.2.1: URL credentials are dropped from versions and specs.
- 0.1.0: first version.

## Plugin deps-nuget

- 0.1.0: NuGet dependencies from project files, props files, `packages.config`
  and `packages.lock.json`.

## Plugin deps-python

- 0.2.2: compiled requirements files give transitive dependencies.
- 0.2.1: URL credentials are dropped from versions.
- 0.1.0: first version.

## Plugin dockerfile

- 0.2.0: `dotnet publish` and `dotnet build` entrypoints are resolved to
  project folders; `other_commands` lists RUN commands that are neither a known
  build nor setup.
- 0.1.1: ADD URLs and remote build contexts drop credentials.
- 0.1.0: first version.

## Plugin dotnet

- 0.2.1: a RequireAuthorization or AllowAnonymous on a route group inside an
  `if` decides an endpoint's auth only when the endpoint is under the same
  condition, else the state is `unknown` and the marker shows its condition. A
  fully qualified `AllowAnonymous` or `Authorize` on a lambda is read. A Choose
  cut at its caps gives an unresolved ProjectReference import, so narrowing
  keeps the link.
- 0.2.0: ASP.NET endpoints (minimal APIs, controllers, Razor Pages, Blazor)
  and `auth_policy` facts; linked items and unresolved ProjectReferences as
  item imports.
- 0.1.0: test suites, the project graph, EF Core tables, environment variables
  and outbound hosts.

## Plugin env

- 0.1.1: Caddyfile heredoc placeholders and long dotenv lines are handled.
- 0.1.0: first version.

## Plugin kubernetes

- 0.1.2: the engine is looked up by its full image path (`mssql/server`).
- 0.1.1: Helm repository and kustomize remote URLs drop credentials.
- 0.1.0: first version.

## Plugin migrations-sql

- 0.2.0: the `public.` schema and row-level security are only reported for a
  set the SQL shows to be PostgreSQL. A dialect is recorded in `ext.dialect`.
- 0.1.1: Flyway ordering, and MySQL detection and lexing.
- 0.1.0: first version.

## Plugin nginx

- 0.3.0: `match_type` and `rewrite` on routes.
- 0.2.0: route keys carry the listen scheme and port. Include cycles and URL
  credentials are handled.
- 0.1.0: first version.

## Plugin outbound

- 0.2.1: the Python and JavaScript readers cap nesting and handle unclosed base
  lists.
- 0.1.0: first version.

## Plugin ownership

- 0.1.0: first version.

## Plugin prisma

- 0.1.0: first version.

## Plugin size

- 0.1.1: test lines of C#, F# and VB follow the .NET test convention.
- 0.1.0: first version.

## Plugin tests

- 0.1.0: first version.

## Plugin traefik

- 0.2.0: `match_type` and `rewrite` on routes.
- 0.1.1: URL credentials are dropped.
- 0.1.0: first version.
