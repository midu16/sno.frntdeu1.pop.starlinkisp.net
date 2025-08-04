# go-webcache

This represents a simple webcache server from your laptop to expose the `agent.x86_64.iso` file to the BMC Server. There is no SSL-certificate required, username or password required since the usage its strictly in an AirGapped environment.

- [go-webcache](#go-webcache)
  - [How to build](#how-to-build)
  - [How to use](#how-to-use)

## How to build

```bash
go build -ldflags="-s -w" -o webcache
```

## How to use

```bash
./webcache 
Enter full path to agent.x86_64.iso file: /home/midu/sno.frntdeu1.pop.starlinkisp.net/workdir/agent.x86_64.iso
Serving agent.x86_64.iso on http://0.0.0.0:9090/agent.x86_64.iso
```

