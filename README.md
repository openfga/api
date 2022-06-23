# OpenFGA API
This project contains the definitions of [Protocol Buffers](https://developers.google.com/protocol-buffers/) used by OpenFGA.

[Buf](https://github.com/bufbuild/buf) is used to manage, package, and generate source code from the protocol buffer definitions. The API definitions
are pushed to the [`buf.build/openfga/api`](https://buf.build/openfga/api) repository in the Buf Registry.

## Building the Generated Sources
To generate source code from the protobuf definitions contained in this project you can run the following command:

```bash
./buf.gen.yaml
```

The command above will generate source code in the `proto/` directory.
