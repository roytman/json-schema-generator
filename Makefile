.PHONY: build-tool
build-tool:
	go build

.PHONY: generate-test-data
generate-test-data:
	./json-schema-generator -r ./testPkgs/fybrikobject -o ./testdata/schema

.PHONY: test
test: build-tool generate-test-data
	go test -v ./...
