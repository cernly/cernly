---
title: Output formats
description: The facts file and the map are canonical JSON described by JSON Schema 2020-12 documents, published for validation and tooling.
path: /use/schema/
section: Use
order: 100
---
# Output formats

**TL;DR** Both outputs are canonical JSON (sorted keys, no extra whitespace) described by JSON Schema 2020-12. Within v1, changes are additive only.

| Schema | Describes |
|---|---|
| [scan.v1.schema.json](/schema/scan.v1.schema.json) | The facts file, `"schema": "cernly.scan/v1"`. |
| [map.v1.schema.json](/schema/map.v1.schema.json) | The map, `"schema": "cernly.map/v1"`. |
| [plugin.v1.schema.json](/schema/plugin.v1.schema.json) | What a WebAssembly plugin reads and writes. |

## Stability

- Tools written against v1 keep working: new fields and kinds may appear, none disappear or change meaning.
- A breaking change would be v2, with its own schema.

## Example map entry

```json
{
  "id": "stripe",
  "name": "Stripe",
  "kind": "provider",
  "category": "payments",
  "grade": "confirmed",
  "services": ["service:compose:prod:api"],
  "signals": [
    { "kind": "sdk", "fact": "dependency:npm:api:stripe" },
    { "kind": "env", "fact": "env_var:STRIPE_API_KEY" }
  ]
}
```
