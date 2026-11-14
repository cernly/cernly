// Package schema embeds the published JSON schemas, so the scanner can validate
// what a WebAssembly plugin returns against the
// same file plugin authors read. See https://cernly.dev/how/plugins/.
package schema

import _ "embed"

// ScanV1 is scan.v1.schema.json: the fact vocabulary every plugin emits.
//
//go:embed scan.v1.schema.json
var ScanV1 []byte

// ScanV1ID is the $id of ScanV1.
const ScanV1ID = "https://cernly.dev/schema/scan.v1.schema.json"

// PluginV1 is plugin.v1.schema.json: the manifest, the input a plugin reads
// and the output it writes.
//
//go:embed plugin.v1.schema.json
var PluginV1 []byte

// PluginV1ID is the $id of PluginV1.
const PluginV1ID = "https://cernly.dev/schema/plugin.v1.schema.json"
