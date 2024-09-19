# OpenFGA API
This project contains the definitions of [Protocol Buffers](https://developers.google.com/protocol-buffers/) used by OpenFGA.

[Buf](https://github.com/bufbuild/buf) is used to manage, package, and generate source code from the protocol buffer definitions. The API definitions
are pushed to the [`buf.build/openfga/api`](https://buf.build/openfga/api) repository in the Buf Registry.

## Building the Generated Sources
To generate source code from the protobuf definitions contained in this project you can run the following command:

> **Note**: You must have [Buf CLI](https://docs.buf.build/installation) installed to run the following command.
> 
```bash
make buf-gen
```

The command above will generate source code in the `proto/` directory. It will also configure a local git hook to check
that files requiring auto-generation after `.proto` changes have been updated. There are some cases where that git hook
may be overly strict. In those cases you can bypass it with `commit --no-verify`.

## Use the generated sources in OpenFGA

1. Generate the sources as above
2. In the `proto` directory execute the following commands:
    ```
    go mod init go.buf.build/openfga/go/openfga/api
    go mod tidy
    ```
3. In OpenFGA, add the following line to your `go.mod`:
    ```
    replace github.com/openfga/api/proto => /path/to/proto
    ```

## Generating OpenAPI Documentation
To generate the OpenAPI documentation from the protobuf sources you can run the following commands:

> **Note**: You must have [jq](https://jqlang.github.io/jq/download/) installed to run the `format` step below

```bash
./buf.gen.yaml
./scripts/update_swagger.sh docs/openapiv2/apidocs.swagger.json
buf format -w
```

Or you can just use
```bash
make
```
