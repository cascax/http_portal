<h1 align="center">Welcome to HTTP-Portal 👋</h1>
<p>
  <a href="https://github.com/cascax/http_portal/blob/master/LICENSE">
    <img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-yellow.svg" target="_blank" />
  </a>
</p>

> 像传送门一样穿透NAT网络访问HTTP服务

### 🏠 [Homepage](https://github.com/cascax/http_portal)

![portal](https://user-images.githubusercontent.com/7347114/60699454-84f91200-9f26-11e9-8c83-f9cc20140eb5.png)

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

## Author

👤 **cascax**


## Show your support

Give a ⭐️ if this project helped you!

## 📝 License

This project is [MIT](https://github.com/cascax/http_portal/blob/master/LICENSE) licensed.
