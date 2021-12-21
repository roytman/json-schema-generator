# json-schema-generator

Generate JSON schemas from Go structures using controller-tools markers for validation.

This tools outputs a JSON schema for each scanned package that has `+fybrik:validation:schema` marker.
Types in scanned packages that lack the marker are outputed to `external.json`

```
Usage:
  json-schema-generator [flags]

Flags:
  -h, --help            help for json-schema-generator
  -o, --output string   Directory to save JSON schema artifact to
  -r, --roots strings   Paths and go-style path patterns to use as package roots
  -v, --version         version for json-schema-generator
```

