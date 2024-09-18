all: init-hooks patch-swagger-doc format

buf-gen: init-hooks
	./buf.gen.yaml

patch-swagger-doc: buf-gen
	./scripts/update_swagger.sh docs/openapiv2/apidocs.swagger.json

format: buf-gen
	buf format -w

init-hooks:
	git config --local core.hooksPath .githooks/
