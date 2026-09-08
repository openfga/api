all:
	$(MAKE) patch-swagger-doc
	buf format -w
	$(MAKE) openapi-v3

buf-gen: init-git-hooks
	./buf.gen.yaml

patch-swagger-doc: buf-gen
	./scripts/update_swagger.sh docs/openapiv2/apidocs.swagger.json

openapi-v3:
	cd tools/openapiv3 && go run .

test-openapi-v3:
	cd tools/openapiv3 && go test ./...

format: buf-gen
	buf format -w

init-git-hooks:
	git config --local core.hooksPath .githooks/
