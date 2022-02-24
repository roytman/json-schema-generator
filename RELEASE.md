# Release Process

The process of creating a release is described in this document. Replace `X.Y.Z` with the version to be released.

## 1. Create a `vX.Y.Z` tar 

Manually run the `Create tag` action. This will create a git tag and update the `VERSION` file 

## 2. Create a [new release](https://github.com/fybrik/json-schema-generator/releases/new) 

Use the `vX.Y.Z` tag.

Ensure that the release notes explicitly mention upgrade instructions and any breaking change.
