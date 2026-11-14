---
title: Deployment stacks and the production rule
description: How Cernly decides which deployment stack is production from the CI jobs that start it, and how a repo or an operator confirms or overrides that.
path: /how/stacks/
section: How
order: 30
---
# Deployment stacks and the production rule

**TL;DR** A stack is production when a CI job on the default branch starts it and nothing in the job's conditions limits it to another branch. A condition Cernly cannot read gives "production (unconfirmed)", which a person confirms once.

## What a stack is

A deployable unit: a Compose project, a Kubernetes overlay or set of manifests. Plugins report each stack as a fact with the files that define it, and the commands in CI workflows that start it.

## The rule

For every workflow job that reaches a stack:

1. The trigger must run on the default branch (`main` unless `--default-branch` says otherwise): a push to it, a schedule, a manual run.
2. Every `if:` of the job, of the jobs it needs and of the step that reaches the stack must admit that branch.
3. The most production-like trigger wins.

The roles:

| Role | When |
|---|---|
| `production` | A job meets both conditions and every condition could be read. |
| `production (unconfirmed)` | A job would deploy, but a condition could not be read. |
| `none` | No job reaches the stack from the default branch, or only a test job does. |

A job that only renders a stack (`helm template`, `kustomize build`, `docker compose config`, `--dry-run`) never starts it and does not count. Each role in the map lists its reasons: which workflow, which job, which condition.

## Confirming

- In the repo: `stacks.<name>.role` in [.cernly.yml](/use/config/).
- Outside the repo: `cernly map --confirmations` with a JSON list of `{kind, subject, value, reason}`, for example `stack_role`, `stack:compose:prod`, `production`.

Either way the derived role stays beside the declared one, with who declared it. The same mechanism confirms or dismisses an outside party or host.

## In the code

`rules/production.go` (the rule), `rules/ifexpr.go` (reading `if:` expressions), `rules/confirm.go` (confirmations).
