<h1 align="center">Welcome to HTTP-Portal 👋</h1>
<p>
  <a href="https://github.com/cascax/http_portal/blob/master/LICENSE">
    <img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-yellow.svg" target="_blank" />
  </a>
</p>

> 像传送门一样穿透NAT网络访问HTTP服务
>
> Access the HTTP service through a NAT network like a portal

### 🏠 [Homepage](https://github.com/cascax/http_portal)

![portal](https://user-images.githubusercontent.com/7347114/61183485-8936d500-a674-11e9-8195-c0d8d94505d2.png)

## 📦 Install

```sh
git clone https://github.com/cascax/http_portal.git

# install agent
cd portal_agent
go install

# install server
cd portal_server
go install
```

## 🚀 Usage

start portal server

```sh
portal_server -c ./portal_server.yml
```

start portal agent

```sh
portal_agent -c ./portal_agent.yml
```

## 🔧 Configuration

An example for the server's config file

```yaml
http_server:
  listen: 0.0.0.0
  port: 8080

proxy_server:
  listen: 0.0.0.0
  port: 10625
  # forward request to the client(mccode) according to the Host
  portal:
    mccode:
      - "local.mccode.info"

# configuration of the log file
log:
  path: "."
  name: server.log
  # log files' max size (MB)
  max_size: 100
  max_backup: 5
  # valid value: debug info warn error panic fatal
  level: info
```

An example for the agent's config file

```yaml
portal_agent:
  name: mccode
  # portal server's address
  remote_addr: 127.0.0.1:10625
  # replace the request's host
  host_rewrite:
    local.mccode.info: local.mccode.info:8081

# same as above
log:
  path: "."
  name: agent.log
  max_size: 100
  max_backup: 5
  level: info
```

## Author

👤 **cascax**


## Show your support

Give a ⭐️ if this project helped you!

## 📝 License

This project is [MIT](https://github.com/cascax/http_portal/blob/master/LICENSE) licensed.
