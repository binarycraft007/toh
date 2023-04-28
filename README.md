# Introduction

`toh` is tcp over http. short words: proxy your network over websocket
## Table of contents
- [ToH server](#toh-server-as-backend)
- [Nginx or Caddy](#nginx-or-caddy)
- [Buildin port-forward tool `pf` act as ToH client](#buildin-port-forward-tool-pf-act-as-toh-client)
- [Buildin socks5 proxy server `s5` act as ToH client](#buildin-socks5-proxy-server-s5-act-as-toh-client)

### ToH server
- Build
```sh
$ git clone https://github.com/rkonfj/toh.git
$ go build -ldflags "-s -w"
```

- Run
```sh
$ ./toh serve --help
ToH server daemon

Usage:
  toh serve [flags]

Flags:
      --acl string      file path for authentication (default "acl.json")
  -h, --help            help for serve
  -l, --listen string   http server listen address (default "0.0.0.0:9986")

Global Flags:
      --log-level string   logrus logger level (default "info")
$ ./toh serve
time="2023-04-26T21:49:33+08:00" level=info msg="initializing ack file acl.json"
{
    "keys": [
        {
            "name": "default",
            "key": "29bc9263-0669-4dac-bfb2-a90461aa2ade"
        }
    ]
}
time="2023-04-26T21:49:33+08:00" level=info msg="acl: load 1 keys"
time="2023-04-26T21:49:33+08:00" level=info msg="server listen on 0.0.0.0:9986 now"
```
the `key` here will used by `pf` or `socks5`

### Caddy or Nginx
- Caddy
```sh
$ caddy reverse-proxy --from https://l4us.synf.in --to 127.0.0.1:9986
```

- Nginx
```
server {
	listen 443 ssl;
	server_name l4us.synf.in;

	ssl_certificate     tls.crt;
	ssl_certificate_key tls.key;

	location /ws {
		proxy_pass http://127.0.0.1:9986;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $remote_addr;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_http_version 1.1;
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection upgrade;
	}
}
```
### Buildin port-forward tool `pf` act as ToH client

```sh
$ ./toh pf --help
Port-forwarding daemon act as ToH client

Usage:
  toh pf [flags]

Flags:
  -k, --api-key string    the ToH api-key for authcate
  -f, --forward strings   tunnel mapping (<net>/<local>/<remote>, ie: udp/0.0.0.0:53/8.8.8.8:53)
  -h, --help              help for pf
  -s, --server string     the ToH server address

$ ./toh pf -s wss://l4us.synf.in/ws -k 5868a941-3025-4c6d-ad3a-41e29bb42e5f -f udp/127.0.0.53:53/8.8.8.8:53 -f tcp/0.0.0.0:1080/google.com:80
time="2023-04-28T13:52:31+08:00" level=info msg="listen on 127.0.0.53:53 for udp://8.8.8.8:53 now"
time="2023-04-28T13:52:31+08:00" level=info msg="listen on 0.0.0.0:1080 for tcp://google.com:80 now"
```

another shell
```sh
$ dig @127.0.0.53 www.google.com +short
142.250.68.4

$ curl 127.0.0.1:8080
<HTML><HEAD><meta http-equiv="content-type" content="text/html;charset=utf-8">
<TITLE>301 Moved</TITLE></HEAD><BODY>
<H1>301 Moved</H1>
The document has moved
<A HREF="http://www.google.com:8080/">here</A>.
</BODY></HTML>
```

### Buildin socks5 proxy server `s5` act as ToH client
```sh
$ ./toh s5 --help
Socks5 proxy server act as ToH client

Usage:
  toh s5 [flags]

Flags:
  -c, --config string       socks5 server config file (default is $HOME/.config/toh/socks5.yml)
      --dns string          dns to use (leave blank to disable local dns)
      --dns-listen string   local dns (default "0.0.0.0:2053")
      --dns-proxy string    leave blank to randomly choose one from the config server section
  -h, --help                help for s5

Global Flags:
      --log-level string   logrus logger level (default "info")
$ ./toh s5
time="2023-04-28T13:46:37+08:00" level=info msg="initializing config file /root/.config/toh/socks5.yml"
geoip2: country.mmdb
listen: 0.0.0.0:2080
servers:
  - name: us1
    api: wss://us-l4-vultr.synf.in/ws
    key: 5868a941-3025-4c6d-ad3a-41e29bb42e5f
    ruleset:
      - https://raw.githubusercontent.com/rkonfj/toh/main/ruleset.txt
    healthcheck: https://www.google.com/generate_204
groups: []
time="2023-04-28T13:46:37+08:00" level=info msg="downloading https://raw.githubusercontent.com/rkonfj/toh/main/ruleset.txt"
time="2023-04-28T13:46:40+08:00" level=info msg="ruleset   us1: special 0, direct 0, wildcard 20"
time="2023-04-28T13:46:40+08:00" level=info msg="downloading country.mmdb to /root/.config/toh. this can take up to 2m0s"
time="2023-04-28T13:46:46+08:00" level=info msg="total 1 proxy servers and 0 groups loaded"
time="2023-04-28T13:46:46+08:00" level=info msg="listen on 0.0.0.0:2080 for socks5 now"
```

the server `us1` is the test server, will stopped in the future

another shell
```sh
$ https_proxy=socks5://127.0.0.1:2080 curl -i https://www.google.com/generate_204
HTTP/2 204
cross-origin-resource-policy: cross-origin
date: Mon, 24 Apr 2023 01:47:57 GMT
alt-svc: h3=":443"; ma=2592000,h3-29=":443"; ma=2592000
```
