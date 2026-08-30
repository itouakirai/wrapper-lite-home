# wrapper-lite-home（中文说明）

**中文** · [English](README.md)

> 本文是 [英文版 README](README.md) 的中文翻译，内容以英文版为准。

一个面向 Apple Music 解密 wrapper 的轻量级 API 聚合网关。通过 iTunes lookup 检测每个 `adamId` 所属的商店区域，将请求路由到正确的区域上游 API，并自带 AdGuard Home 风格的管理后台。

它聚合上游 **lite 分支** 的
[WorldObservationLog/wrapper (lite)](https://github.com/WorldObservationLog/wrapper/tree/lite)
——即底层的单端口 Apple Music 解密 wrapper——并在同一个端口上统一暴露其所有端点。本项目专门针对 `lite` 分支设计，请确保每个上游都指向 `lite` 分支的实例。

## 特性

- **单端口聚合** — 在一个端口上暴露 `/m3u8`、`/key`、`/lyrics`、`/webplayback`、`/license` 和 `/status`，同时代理到多个上游后端。
- **区域感知路由** — 针对每个传入的 `adamId`，向上游支持的所有区域发起 iTunes lookup，并将请求转发到提供匹配区域的上游。
- **检测缓存** — iTunes lookup 结果按区域缓存（TTL 可配置，默认 30 分钟），避免重复查询。
- **健康探测** — 每分钟轮询各上游的 `/status`。若连续 3 次失败则标记为离线，并降频为每 10 分钟探测一次（退避）。返回空区域同样视为离线。
- **管理后台** — 登录保护的前端页面，展示每日请求统计、按 API 维度的明细，以及 uptime-kuma 风格的状态卡。
- **统计** — 总数 / 按上游 / 按端点计数、按小时明细、近 7 天历史，持久化到 JSON 文件。
- **零外部依赖** — 完全基于 Go 标准库构建。

## 配置

将 `config.example.json` 复制为 `config.json` 并修改：

```json
{
  "listen": ":8080",
  "auth": {
    "username": "admin",
    "password": "admin"
  },
  "session_ttl": "24h",
  "region": {
    "cache_ttl": "30m",
    "not_found_ttl": "10m",
    "concurrency": 4,
    "lookup_timeout": "5s",
    "itunes_lookup_base": "https://itunes.apple.com/lookup"
  },
  "probe": {
    "interval": "1m",
    "retries": 3,
    "retry_delay": "2s",
    "backoff_interval": "10m",
    "timeout": "5s"
  },
  "upstream_timeout": "30s",
  "stats_file": "data/stats.json",
  "stats_save_interval": "30s",
  "reload_interval": "2s",
  "trust_proxy_headers": false,
  "max_client_ips": 10000,
  "upstreams": [
    { "name": "US API", "base_url": "http://127.0.0.1:3001" },
    { "name": "CN API", "base_url": "http://127.0.0.1:3002" }
  ]
}
```

### 字段说明

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `listen` | `:8080` | 监听地址 |
| `auth.username` | `admin` | 后台登录用户名 |
| `auth.password` | `admin` | 后台登录密码（首次运行请务必修改！） |
| `session_ttl` | `24h` | 会话令牌有效期 |
| `region.cache_ttl` | `30m` | adamId 命中区域的缓存时长 |
| `region.not_found_ttl` | `10m` | adamId 未命中区域的缓存时长 |
| `region.concurrency` | `4` | 每个 adamId 的最大并发 iTunes 查询数 |
| `region.lookup_timeout` | `5s` | 每次 iTunes 查询的超时时间 |
| `region.itunes_lookup_base` | `https://itunes.apple.com/lookup` | iTunes lookup 基础地址（在无法访问 apple.com 的地区可配置镜像/代理） |
| `probe.interval` | `1m` | 正常模式下探测上游 /status 的间隔 |
| `probe.retries` | `3` | 探测失败后进入退避前的重试次数 |
| `probe.retry_delay` | `2s` | 重试间隔 |
| `probe.backoff_interval` | `10m` | 退避模式下的探测间隔 |
| `probe.timeout` | `5s` | 每次探测请求的超时时间 |
| `upstream_timeout` | `30s` | 上游代理请求的超时时间 |
| `stats_file` | `data/stats.json` | 统计数据持久化文件路径 |
| `stats_save_interval` | `30s` | 统计数据落盘间隔 |
| `reload_interval` | `2s` | 轮询配置文件并热重载的间隔 |
| `trust_proxy_headers` | `false` | 是否使用 `X-Forwarded-For` / `X-Real-IP` 统计客户端 IP。仅在可信反向代理后启用 |
| `max_client_ips` | `10000` | IP 排行计数器最多保留的独立客户端 IP 数 |
| `upstreams[].name` | — | 上游名称（显示在后台） |
| `upstreams[].base_url` | — | 上游 wrapper API 的基础地址 |
| `upstreams[].enabled` | `true` | 该上游是否启用 |

时长字段支持 Go 时长字符串（`"30s"`、`"5m"`、`"1h"`）或纯数字秒数。

## HTTP API

所有响应使用以下信封格式：

```json
{"code":0,"msg":"SUCCESS","data":{...}}
```

### 公共端点

| 端点 | 方法 | 参数 | 说明 |
|------|------|------|------|
| `/m3u8` | GET | `adamId` | 获取 M3U8 播放列表 |
| `/key` | GET | `adamId`，可选 `uri` | 获取解密密钥 |
| `/lyrics` | GET | `adamId`，可选 `language`，可选 `syllable`（`1`=逐字歌词默认，`0`=整行歌词） | 获取歌词 |
| `/webplayback` | GET | `adamId` | 获取网页播放数据 |
| `/license` | POST | JSON：`adamId`、`challenge`、`uri` | 获取许可证 |
| `/status` | GET | — | 返回所有在线上游合并后的 `regions` |

### 管理 API

所有管理端点都需要登录。请携带会话 cookie `wl_token`（登录后自动设置），或使用 `Authorization: Bearer <token>` 请求头。

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/login` | POST | `{"username":"...","password":"..."}` → 设置 cookie |
| `/api/logout` | POST | 清除会话 |
| `/api/me` | GET | 返回当前用户名 |
| `/api/status` | GET | 上游快照（在线状态、区域、延迟、可用率、退避状态） |
| `/api/stats` | GET | 请求统计（总数、今日按小时、近 7 天、按上游、按端点、客户端 IP 排行） |
| `/api/upstreams` | POST | 添加上游：`{"name":"...","base_url":"...","enabled":true}` |
| `/api/upstreams/{name}` | PATCH | 启用/停用上游：`{"enabled":true}` |
| `/api/upstreams/{name}` | DELETE | 删除上游 |

## 管理后台

浏览器打开 `http://<host>:<port>/`。后台展示：

- **统计卡片** — 总请求数、今日请求数、在线上游数、合并区域
- **上游管理** — 无需手动编辑配置文件，即可添加、启用/停用、删除上游 API
- **状态卡** — 每个上游一张，含在线/离线/退避指示、区域徽章、延迟、可用率百分比、最后检查时间
- **小时柱状图** — 今日请求按小时分布
- **7 日柱状图** — 最近 7 天的每日总数
- **按 API 明细** — 每个上游请求数的横向柱状图
- **按端点明细** — 今日各端点（/m3u8、/key 等）的请求数
- **客户端 IP 排行** — 今日和累计访问公共代理端点的客户端 IP Top 排行

## 配置热重载

程序会按 `reload_interval` 轮询配置文件。认证信息、会话有效期、区域检测、探测设置、上游超时、统计设置、客户端 IP 排行设置和上游列表的变更都会热生效，无需重启。
修改 `listen` 仍需重启，因为 HTTP 监听套接字已经绑定。

在后台添加或删除上游时，wrapper-lite 会把变更写回同一个配置文件，并立即应用。

## 区域路由逻辑

1. 收到一个携带 `adamId` 的请求。
2. 服务从所有 *在线* 上游获取合并的区域列表。
3. 对每个区域发起 iTunes lookup：
   `https://itunes.apple.com/lookup?id=<adamId>&country=<region>`
4. 若 `resultCount` > 0，则该区域可用。
5. 结果按区域缓存（TTL 可配置）。
6. 请求被转发到第一个支持某个可用区域的在线上游（同一区域内轮询以实现负载均衡）。

若无法确定区域或没有上游在线，端点会返回相应错误码（502、503、404）。

## 快速开始

```bash
# 1. 构建
go build -o wrapper-lite.exe .

# 2. 创建配置
cp config.example.json config.json
# 编辑 config.json 设置你的上游和凭据

# 3. 运行
./wrapper-lite.exe --config config.json

# 4. 打开 http://localhost:8080 并登录
```

## 构建

```bash
go build -o wrapper-lite.exe .
```

二进制自包含 — 前端通过 `go:embed` 内嵌。

## 测试

```bash
go test ./...
```

## 离线测试用模拟服务器（可选）

实际部署使用真实的 iTunes lookup API 和你的真实上游，不需要模拟。下面的模拟服务器仅用于无外网环境下的离线验证：

```bash
# 终端 1 — US 上游
go run ./testdata/mock_upstream --name "US" --addr :3001 --regions us

# 终端 2 — CN 上游
go run ./testdata/mock_upstream --name "CN" --addr :3002 --regions cn

# 终端 3 —（仅离线）模拟 iTunes lookup，偶数 adamId -> us，奇数 -> cn
go run ./testdata/mock_itunes --addr :4000

# 启动 wrapper-lite 并使用测试配置验证
curl http://localhost:8080/status
curl -c cookies.txt -d "{\"username\":\"admin\",\"password\":\"admin\"}" http://localhost:8080/api/login
curl -b cookies.txt "http://localhost:8080/m3u8?adamId=111111111"  # 偶数 -> US 上游
```

## License / 许可证

[MIT](LICENSE)


