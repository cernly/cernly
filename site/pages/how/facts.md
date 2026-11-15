---
title: Facts and evidence
description: How a Cernly fact is shaped, why every fact carries file and line evidence, and how image references, versions and repo ids are normalised.
path: /how/facts/
section: How
order: 10
---
# Facts and evidence

**TL;DR** A fact is one observation about one commit: a kind, a stable key, attributes, the plugin that found it, and the file and line that prove it. No evidence, no fact.

```json
{
  "kind": "env_var",
  "key": "env_var:STRIPE_API_KEY",
  "plugin": "env",
  "attrs": { "name": "STRIPE_API_KEY", "origins": ["code", "compose"] },
  "evidence": [
    { "path": "api/payments.go", "line": 18 },
    { "path": "docker-compose.yml", "line": 31, "unit": "api" }
  ]
}
```

## Rules

- **One commit, git-tracked files only.** Untracked files, such as a real `.env`, are never read. Two checkouts of the same commit see the same file list.
- **Deterministic.** Facts are sorted and encoded as canonical JSON. Same commit, same binary, same flags: the same bytes.
- **Keys are stable.** A key names the thing, not where it was seen, so a moved line is the same fact with new evidence.
- **Raw values are kept.** A normalised value never replaces what the file said; both are in the attributes.
- **Unknown stays unknown.** A value from a variable without a default is marked unknown, never guessed.

## Normalisation

Plugins translate their stack's syntax into shared concepts, so rules only read those:

- **Image references** become registry, repository, tag and digest, with a pinning grade: `digest`, `exact` (a full version), `floating` (`latest`, no tag, a major like `16`) or `unknown`. Docker Hub names become `docker.io/library/<name>`. `${VAR:-default}` keeps the variable and shows the default.
- **Versions** keep the raw string and add a scheme: semver, calver, date or pseudo.
- **Internal packages** (workspaces, Go `replace`, .NET `ProjectReference`) become edges inside the repo, never third-party dependencies.
- **Stacks share the vocabulary.** A dependency names its ecosystem (`go`, `npm`, `pypi`, `maven`, `nuget`), a test suite its language and framework (`csharp` with `xunit`, `nunit`, `mstest` or `tunit`, say), an `import` its language (`dotnet` for project references). Rules read these fields, never the stack's own files. A `dotnet test` of a tracked solution (`.sln` or `.slnx`) records on its test run the folders of the projects the solution lists (`projects`), and then covers only the test suites at those folders; without that list it covers every suite below its folder.
- **Endpoints** become one `endpoint` fact per registered route, with the auth the code states (`required`, `anonymous` or `unknown`). Host-wide rules are `auth_policy`. A prefix the reader cannot resolve is marked, never guessed.
- **Routes** record how they match and rewrite paths.
- **Repo identity** is `host/owner/name`, from the git remote or `--repo`.

## In the code

`fact` (the types and encoding), `repo` (the read-only view of a commit), `pipeline` (scan and map). Kinds: [fact kinds](/use/facts/).
