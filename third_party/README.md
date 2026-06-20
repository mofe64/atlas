# Third-Party Sources

This directory contains external source dependencies that Atlas uses for code generation or local integration.

## MAVSDK-Proto

Path:

```text
third_party/mavsdk-proto
```

Source:

```text
https://github.com/mavlink/MAVSDK-Proto.git
```

Pinned commit:

```text
38c4330bd1238dab56bd41983ce6ee7adcb0226c
```

Atlas uses these protobuf definitions to generate Go gRPC clients for `mavsdk_server`.
The generated clients live under:

```text
atlas-agent/internal/mavsdkpb
```

Regenerate them with:

```sh
scripts/generate-mavsdk-go.sh
```

After cloning the Atlas repository, initialize submodules with:

```sh
git submodule update --init --recursive
```

To inspect the pinned submodule commit:

```sh
git submodule status
```
