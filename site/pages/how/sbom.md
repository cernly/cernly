---
title: Dependencies and SBOM
description: How Cernly reads dependencies from manifests and lockfiles, attaches them to services, and writes a deterministic CycloneDX 1.6 SBOM.
path: /how/sbom/
section: How
order: 90
---
# Dependencies and SBOM

**TL;DR** Direct dependencies come from manifests, transitive ones from lockfiles. Each is attached to the services whose build includes its folder. `cernly sbom` writes that as CycloneDX 1.6, the same bytes for the same scan.

## Reading

- **Manifests** give direct dependencies with their declared version and scope: production, development, test or build.
- **Lockfiles** give what is actually resolved, including transitive dependencies: npm, Go modules, Python lockfiles, NuGet `packages.lock.json`; Maven and Gradle through the Java plugin. A legacy `packages.config` lists every package as direct, because it cannot tell them apart.
- **Licences** where the package manager records them (npm today). Cernly never downloads a package to find out.
- **Internal packages** (workspaces, Go `replace`) are edges inside the repo, not dependencies.

## Attaching

A dependency belongs to a service when the service's build sources include the manifest's folder: the Dockerfile's build context and copied paths, linked through the [map](/how/map/). A manifest no build reaches stays on the repo, not on a service. The attach rule is the same for the map, the gate and the SBOM.

## The SBOM

- One component per dependency, with its package URL, version, scope and licence when known. Package URLs follow the purl spec per type: `pkg:nuget/Newtonsoft.Json@13.0.3` keeps the name's case, as that type requires.
- Per service, or for one stack, or the whole repo.
- Images the service runs or builds from are listed as components. Their contents (OS packages) are not: Cernly never opens an image, and each image says so in a property.
- The serial number comes from the facts file's hash and the timestamp from the commit, so the file is reproducible.

## Gate rules

`new_dependency`, `new_transitive_dependency` and `licence_not_allowed` read the same facts. The transitive rule is off by default: one direct dependency can bring dozens.

## In the code

`plugins/deps` (manifests and lockfiles), `sbom` (CycloneDX), `rules` (attach and licence rules). Command: [cernly sbom](/use/sbom/).
