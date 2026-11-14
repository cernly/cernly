---
title: Architecture docs (arc42, C4)
description: How cernly docs turns one scan into arc42 sections and C4 views in Mermaid, with a footnote of evidence for every statement and blanks where the code cannot answer.
path: /how/architecture-docs/
section: How
order: 110
---
# Architecture docs (arc42, C4)

**TL;DR** `cernly docs` writes the parts of an architecture document the code can prove: arc42 sections and C4 views in Mermaid. Every statement has a footnote with its facts and their file and line. What the code cannot say stays blank.

## What gets written

- **Context and scope:** the system, the routes it serves and its outside parties from the map.
- **Building blocks:** per stack its services and data stores, size per service, and the module graph of the repo's own packages.
- **Deployment:** stacks, which is production and why, the images they run.
- **Cross-cutting concepts:** data (tables, columns to check) and the AI coding tools the repo is set up for.
- **Risks and technical debt:** the map's findings, grouped by rule.
- **C4 context** (the system and the parties around it) and **C4 container** (services and data stores) views as Mermaid, which GitHub and most Markdown tools render.

## Footnotes

Each sentence ends in a footnote that lists the fact keys behind it and their evidence. A reader who doubts a line can open the file at that line. A statement without facts is not written.

## What stays blank

Introduction and goals, constraints, solution strategy, runtime view, decisions, quality requirements and the glossary: those are intent, and the code only shows what is, not why. Each section is there, marked "Not in the code", so the structure stays complete for a person to fill in.

## Determinism

Same scan, same map, same options: the same bytes. Committed as `ARCHITECTURE.md`, it changes only when the architecture does, and the diff shows exactly what changed.

## Finding words

The open command prints each finding's rule id. Explanations in plain words are a separate layer, not part of the facts.

## In the code

`archdoc` (sections, Mermaid, footnotes), `cli/docs.go` (the command). Command: [cernly docs](/use/docs/).
