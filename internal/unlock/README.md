# internal/unlock — Agent 侧解锁检测

按节点自己的出口 IP 逐项发起 HTTPS 检测，结果用 `model.UnlockResult`
（`yes|no|originals_only|banned|failed`）表达。仅标准库；不起 shell、不下载可执行文件。
整包只有三个文件：`unlock.go`（调度与 HTTP 管道）、`checks.go`（24 项判据）、
`unlock_test.go`（单测）。

## 接口

```go
type Options struct {
    Services    []string      // 目录 id；空 = 全部 24 项
    Family      string        // "ipv4"/"ipv6"/""（跟随系统）
    Timeout     time.Duration // 每项预算，0 → 20s
    Concurrency int           // 0 → 8
    UserAgent   string        // 空 → 包内浏览器 UA
}
func Run(ctx context.Context, opts Options) []model.UnlockResult
```

* `Run` 永远返回 `len(Services)` 条，按目录顺序；单项出错只影响自己，写 `failed`。
* 每项有自己的 `context.WithTimeout`；整轮可被传入的 `ctx` 取消（取消项写 `failed/"canceled"`）。
* **网络错误/超时只写 `failed`**（`timeout` / `canceled` / 错误文本），绝不写成 `no`/`banned` —— 后两者是对 IP 的结论，不能由网络故障伪造。
* `Family` 通过 `net.Dialer.Control` 在 connect 系统调用处按 `"tcp4"`/`"tcp6"` 拦截不匹配族（实测 Go 确实以 `tcp4`/`tcp6` 调用 Control），一个 transport 一种族。
* `Client` 带 `hosts map[string]string` 覆写表（`Client.url` 把 scheme+host 换成 httptest 地址），每个检测项都能用 `httptest` 单测；`Options.hosts` 是测试注入点（未导出）。
* Region/Detail 由 `runOne` 统一截断到 40/80 字节且不切断 UTF-8 rune。

## 24 项：端点与判据

| id | 请求 | 判据 |
|---|---|---|
| netflix | `GET www.netflix.com/title/{81280792,70143836}`（不跟随跳转）→ `GET /title/80018499` | 两个 404→`originals_only`；两个 403→`banned`；否则取第三个 `Location` 里 `/<cc>/…` 的 cc（无 header→`US`）→`yes` |
| netflix_cdn | `GET api.fast.com/netflix/speedtest/v2?…urlCount=5` | 403→`banned`；否则 JSON `client.location.city,country`（缺则 `targets[0].location`）→`yes`「City, CC」 |
| disneyplus | `GET www.disneyplus.com/`（不跟随） | 正文 `forbidden-location` / `Location=…unavailable/`→`banned`；`Location=disneyplus.disney.co.jp/`→`yes JP`；无 Location→斯 cookie `x-dss-country`，否则响应头 `region`，否则空→`yes` |
| youtube_premium | `GET www.youtube.com/premium`（跟随） | `"countryCode":"XX"`（缺则 `"GL":"XX"`）→`yes`；`premiumPurchaseButton`/`manageSubscriptionButton`→`yes`；否则 `no` |
| youtube_cdn | `GET redirector.googlevideo.com/report_mapping` | 取首条 `<ip> => <host>` 的 host，**在整串里**找已知机场码（`lcnrtb-bg`→nrt→东京，`lax31s13`→lax→洛杉矶）→「城市, 国家」；认不出→`failed`「unknown aircode」 |
| prime_video | `GET www.primevideo.com`（跟随） | `"currentTerritory":"XX"`（缺则 `"countryCode":"XX"`）→`yes`；否则 `no` |
| tvb_anywhere | `GET uapisfm.tvbanywhere.com.sg/geoip/check/platform/android` | JSON `allow_in_this_country` 真→`yes` + `country`；假→`no` |
| iqiyi | `GET www.iq.com` | 响应头 `x-custom-client-ip: <ip>:<region>`；`ntw`→`tw`；无→`no` |
| dazn | `POST startup.core.indazn.com/misl/v5/Startup`（跟随，固定 JSON 体） | JSON `Region.IsAllowed` 真→`yes`+`GeolocatedCountry`；假→`banned`+`DisallowedReason`；403/451 且非 JSON→`banned` |
| openai | `GET chat.openai.com/cdn-cgi/trace` + `GET chat.openai.com`（均不跟随） | 正文含 `VPN` 或根 403→`banned`；429→`originals_only`；否则按 `loc` 是否在 ChatGPT 支持国→`yes`/`no` |
| gemini | `GET gemini.google.com`（跟随） | 正文 `,2,1,200,"XXX"`→`yes`+XXX；旧标记 `45631641,null,true`→`yes`；否则 `no` |
| claude | `GET claude.ai/`（跟随） | 终址仍 `claude.ai/`→`yes`；`anthropic.com/app-unavailable-in-region`→`no`；其它→`no`+终址 |
| binance | `GET api.binance.com/api/v3/ping`，非 2xx/451 时回退 `GET www.binance.com/` | 451→`no`「HTTP 451」；2xx→`yes`；网页站 2xx（含 202 挑战页）→`yes`；否则→`failed` |
| bybit | `GET api.bybit.com/v5/market/time`，非 2xx/403 时回退 `GET www.bybit.com/` | 403→`no`「CDN 按国家阻断」；2xx→`yes`；否则→`failed` |
| okx | `GET www.okx.com/`（跟随） | 403/451→`no`；2xx→`yes`+地区（终址 `/en-us` 尾段，缺则 `cdn-cgi/trace` 的 `loc`）|
| bing | `GET www.bing.com/search?q=curl` | `cn.bing.com`→`yes CN`；`Region:"XX"`；含 `sj_cook.set("SRCHHPGUSR","HV"`→Detail `risky`（地区保持 `XX`，不拼进地区名）；恒 `yes` |
| apple | `GET gspe1-ssl.ls.apple.com/pep/gcc` | 纯文本两位国家码→`yes`；空→`failed` |
| google_play | `GET play.google.com/` | `<div class="yVZQTb">` 后文本→`yes`；无→`failed` |
| google_search | `GET www.google.com/search?q=curl…` | 含 `unusual traffic from`/`is blocked`→`no`；含正常结果→`yes`；都无→`failed` |
| wikipedia | `GET zh.wikipedia.org/…action=edit` | 正文（不分大小写）含 `banned`→`no`；否则 `yes` |
| reddit | `GET www.reddit.com/`（Family=ipv6 直接 `failed`） | 403→`no`；200→`yes`；其它→`failed` |
| steam | `GET store.steampowered.com/app/761830` | `priceCurrency` 后 `content="XXX"` →`yes`+币种；无→`failed` |
| onetrust | `GET geolocation.onetrust.com/cookieconsentpub/v1/geo/location` | 剥 JSONP 后 `country`（+`stateName`→`CC [State]`）→`yes` |
| sdggge | `POST api.gl.eternal.channel.or.jp/api/pvt/consent/view?user_id=…`，body 为固定 base64 载荷，头部见 `checks.go` | 200→`yes`；483→`no`；其它→`failed` |

## 单测覆盖（`unlock_test.go`，全 offline）

* `TestCatalogCoverage`：24 项目录与实现一一对应（无缺、无孤儿）。
* `TestRunOrder`：`Run` 顺序稳定、条数=24、状态合法、且整片通过 `model.Result.Validate()`。
* netflix 四态（originals / banned / region JP / 默认 US）、openai 四态（VPN / 429 / 支持国 / 不支持）、reddit、binance（451/200/202/500）、netflix_cdn（client.location 优先 + 403 banned）、prime_video 跟随跳转、apple 地区。
* 超时→`failed/"timeout"`；连接被拒（127.0.0.1:9）→`failed`。
* `TestFamilyControl`：ipv6 pin 拒 `tcp4`、ipv4 pin 拒 `tcp6`、空族不装 hook；`TestIPv6PinCannotReachV4Server`：ipv6 pin 连不上 v4 httptest（端到端证明 pin 生效）。
* `TestRunCanceled`：调用前取消 → 每项 `failed`。
* `TestTruncate`：ASCII 与多字节 rune 截断边界。

## 本机实测（坦帕，双栈）

同一台机器 IPv4 出口在 Google/Netflix 眼里是美国，IPv6 出口被 Google 判到中国 —— 所以下面给两轮。
`go test ./internal/unlock/ -run TestLive -v`（`UNLOCK_LIVE=1`，可选 `UNLOCK_LIVE_FAMILY=ipv4`）复现。

### 默认（Family=""，Happy Eyeballs 选中 v6）

```
netflix          | yes            | US
disneyplus       | yes            | US
youtube_premium  | yes            | US
prime_video      | yes            | US
tvb_anywhere     | yes            | US
iqiyi            | yes            | US
dazn             | banned         |
netflix_cdn      | yes            | Tampa, US
youtube_cdn      | yes            | Dallas, United States
openai           | yes            | US
gemini           | yes            | CHN
claude           | yes            |
bybit            | no             | CDN 按国家阻断
binance          | no             | HTTP 451
okx              | yes            | US
bing             | yes            | US   risky
apple            | yes            | US
google_play      | yes            | China
google_search    | yes            |
wikipedia        | yes            |
reddit           | no             |
steam            | yes            | USD
onetrust         | yes            | US [Florida]
sdggge           | no             |
SCORE 19/24
```

### Family=ipv4（美国口径）

```
netflix          | yes            | US
disneyplus       | yes            | US
youtube_premium  | yes            | US
prime_video      | yes            | US
tvb_anywhere     | yes            | US
iqiyi            | yes            | US
dazn             | banned         |
netflix_cdn      | yes            | Tampa, US
youtube_cdn      | failed         | unknown aircode
openai           | yes            | US
gemini           | yes            | USA
claude           | yes            |
bybit            | no             | CDN 按国家阻断
binance          | no             | HTTP 451
okx              | yes            | US
bing             | yes            | US   risky
apple            | yes            | US
google_play      | yes            | United States
google_search    | yes            |
wikipedia        | yes            |
reddit           | no             |
steam            | yes            | USD
onetrust         | yes            | US [Florida]
sdggge           | no             |
SCORE 18/24
```

可核对项全部对上：netflix yes / apple US / steam USD / claude yes / openai yes US / onetrust US [Florida] / tvb yes US。
两轮差在交易所与 `youtube_cdn`：网页站对哪个出口都回 2xx（`binance` 202、`bybit` 200），按地区回
451 / 403 的是 API，所以两轮都是 `no`；v4 那轮 `report_mapping` 给的主机名里没有已知机场码，按
「不猜地名」的规矩报 `failed`（v6 那轮给 `lcnrtb-bg` → 东京）。

## 会漂的项（已知）

1. **gemini**：上游把可用性标记 `45631641,null,true` 改成了 `false`，且 `,2,1,200,"XXX"` 报的是 Google 对该出口的地理判定（本机 v6 被 Google 判成 `CHN`，v4 是 `USA`）。实现以地区三元组为准，标记只作兜底。
2. **google_play**：`play.google.com/` 的 `yVZQTb` 页脚在无 `gl` 参数时对无 cookie 的请求给的是默认地区（本机 v6 出 `China`、v4 出 `United States`）。要按出口判，需要 Family 指定；根页 URL 依规格未改。
3. **youtube_cdn**：`report_mapping` 给的主机名随出口/边缘节点变（本机 v6 `dfw…`→Dallas，v4 那串里没有已知码→`failed`）。城市表只覆盖常见 IATA，认不出就报 `failed`，不猜地名。
4. **sdggge**：载荷/`x-token`/版本号会过期。本机返回 483（`no`），说明当前 token 仍有效；一旦失效会落到 `failed`（不猜）。
5. **dazn**：本机是 CloudFront 403（`banned`）；若上游改回可解析 JSON，`IsAllowed` 判据才真正生效。
6. **reddit**：数据中心 IP 常见 403（本机即 `no`），与住宅 IP 的 `yes` 不同。
7. **netflix_cdn**：实现优先用 `client.location`（出口自身被 Netflix 判定的城市），而非规格字面的 `targets[0]`——后者是 CDN 节点，可能远离出口（本机 ipv4 曾给 Warsaw）。两者都在 JSON 里，缺 client 时回落到 targets[0]。

## 校验

```
gofmt -l internal/unlock/        # 空
go vet ./internal/unlock/...     # 干净
go test ./internal/unlock/...    # ok（offline；TestLive 默认 skip）
```
