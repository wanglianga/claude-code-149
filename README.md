# 城市体育馆羽毛球场地预约 · 灯控计费与爽约处罚服务

Go + PostgreSQL + Redis 实现的后端服务，覆盖：

- **预约与支付**：报价（分时段分片计价、会员等级折扣、租拍/购球、补偿时长抵扣）、创建即锁场、支付超时释放、入场码
- **闸机 / 灯控**：入场窗口放行、灯控预热（独立状态，不参与超时释放）、自动/手动开关灯、硬件信号幂等
- **计费**：高峰/非高峰/周末三档价、跨高峰按时段拆分、高峰退订违约金、迟到罚金、延时 15 分钟进位、现场超员费、前台当场补偿时长抵扣
- **爽约判定**：综合**闸机扫码、灯控开启、前台签到、退款申请、维护锁定**五类信号，区分爽约 / 迟到 / 在场边等待 / 场馆原因；累计爽约自动封禁
- **客服争议**：完整证据包（订单+事件流+计费台账+支付+维护+退款申请+争议），裁决可退费、补偿时长入账、撤销爽约并退还罚没
- **运营端**：高峰退订与爽约规则、维护锁定（自动安置受影响订单）、临时加场、会员优先预约天数与等级折扣、价格与场地状态管理
- **灰度场景**：前一场延时 → 下一场在场边等待；灯开了但人没上场（灯开≠上场）；会员带朋友拼场超员；前台临时补偿
- **来源留痕**：每一条费用写入 `billing_ledger`（带来源），每一个动作写入 `booking_events` / `ops_events`，客服无需猜订单表

---

## 1. 一键启动（Docker Compose）

```bash
docker compose up --build
```

启动后：

| 服务 | 地址 |
|---|---|
| API | http://localhost:8080 |
| Swagger UI | http://localhost:8080/swagger/ |
| OpenAPI 描述 | http://localhost:8080/openapi.yaml |
| PostgreSQL | localhost:5432（库 courtbook / postgres / postgres） |
| Redis | localhost:6379 |

应用首次启动自动执行数据库迁移并写入种子数据（1 个场馆、8 块场地、4 个员工账号、3 个演示会员、默认策略与价格）。

---

## 2. 测试账号

### 员工（`POST /api/auth/staff/login`）

| 角色 | 用户名 | 密码 | 权限 |
|---|---|---|---|
| 运营主管 | `admin` | `admin123` | 全部，含调度手动触发、虚拟时钟 |
| 运营员 | `operator` | `oper123` | 维护/加场/策略/价格/会员等级 |
| 前台 | `front` | `front123` | 签到、等待登记、补偿时长、人数上报、前台退订 |
| 客服 | `cs` | `cs123` | 订单检索、证据包、爽约判定、退款审批、争议裁决 |

### 会员（`POST /api/auth/member/login`，短信验证码固定 `123456`，新手机号自动注册）

| 手机号 | 等级 | 折扣 | 可提前预约 |
|---|---|---|---|
| 13800000001 | normal 普通 | 无 | 2 天 |
| 13800000002 | silver 银卡 | 9 折 | 4 天 |
| 13800000003 | gold 金卡 | 8 折 | 7 天 |

### 硬件回调

所有闸机/灯控接口需要请求头：`X-Device-Key: device-key-123`

---

## 3. 本地开发（不用 Docker）

依赖：Go 1.19+、PostgreSQL 13+（需 `btree_gist` 扩展，迁移自动创建）、Redis 6+。

```bash
# 1. 准备数据库（迁移由应用启动时自动执行）
createdb courtbook

# 2. 启动
DATABASE_URL='postgres://postgres@127.0.0.1:5432/courtbook?sslmode=disable' \
REDIS_ADDR=127.0.0.1:6379 \
HTTP_ADDR=:8080 \
go run ./cmd/server
```

环境变量：`HTTP_ADDR`、`DATABASE_URL`、`REDIS_ADDR`、`MIGRATE_ON_START`（默认 true）、
`DEVICE_KEY`（默认 device-key-123）、`ENABLE_TEST_CLOCK`（默认 false）、`WORKER_AUTOSTART`（默认 true）。

---

## 4. 验证方式

### 4.1 单元测试（纯计费规则，10 个用例）

```bash
go test ./...
```

覆盖：跨高峰分片计价、会员折扣、租拍购球、补偿时长抵扣、高峰/非高峰退订违约金、
延时 15 分钟进位与补偿抵扣、超员费、到场判定、迟到罚金；以及 OpenAPI 文档合法性与路由覆盖率校验。

### 4.2 端到端验证（66 个断言，15 个业务场景）

```bash
# 需要能免密访问本地 PostgreSQL（脚本默认连 127.0.0.1:5433，可用环境变量覆盖）
bash scripts/reset-and-e2e.sh
```

脚本会：重置数据库 → 构建启动服务 → 运行 `scripts/e2e.py`（仅依赖 Python3 标准库）。
通过**虚拟时钟**（`ENABLE_TEST_CLOCK=true` + `POST /api/admin/clock/set`）确定性地推进支付超时、
预热、开场、爽约截止等时间点。

> 本次交付已实际执行：`go test ./...` 全部通过；e2e **66/66 通过**；
> 另做 10 并发抢同一时段压测，结果 **1 成功 / 9 个 409 / 数据库仅 1 条订单**。

e2e 覆盖场景：

1. 报价（跨高峰分片、金卡 8 折、租拍购球）
2. 并发预约防重复锁定（GiST 排他约束）
3. 支付超时释放；**预热订单不被释放**；超时订单拒绝付款（408）
4. 闸机放行窗口、提前到场、硬件信号幂等、迟到 20% 罚金
5. 完整场次：自动开灯 → 前台补偿 15 分钟 → 延时 40 分钟按 30 分钟计费 → 关灯结算
6. 灰度：灯开人没上场、前一场延时下一场 `waiting` 在场边等待
7. 灰度：带朋友拼场 8 人超员补费、重复上报不重复收费、下单超员拦截
8. 高峰提前 ≥24h 免费全退 / 不足 24h 扣 50% / 未支付免费取消
9. 无到场信号判爽约罚没、3 次自动封禁、封禁后无法预约
10. 客服争议：撤销爽约、退还罚没、爽约次数 -1、60 分钟补偿入账
11. 补偿时长下次预约全额抵扣（0 元订单仍发放入场码）
12. 场地维护锁定：受影响订单场馆原因全额退款、维护时段不可约、占用视图可见
13. 临时加场开放非营业时间；会员优先预约天数（normal 2 天 / gold 7 天）
14. 退款申请审批（不可重复处理）；证据包六类齐全且含闸机/灯控/前台多源事件
15. 前台场馆原因退订全额退款、不计爽约

---

## 5. 快速体验（curl）

```bash
# 会员登录（银卡）
MT=$(curl -s -X POST localhost:8080/api/auth/member/login \
  -d '{"phone":"13800000002","sms_code":"123456"}' | jq -r .token)

# 报价（工作日晚高峰 19:00-20:00）
curl -s -X POST localhost:8080/api/bookings/quote -H "Authorization: Bearer $MT" -d '{
  "venue_id":1,"court_id":1,
  "start_time":"2026-10-07T19:00:00Z","end_time":"2026-10-07T20:00:00Z",
  "player_count":4,"rent_racket":true,"buy_shuttle":true}' | jq

# 下单 → 支付（返回 entry_code）
BID=$(curl -s -X POST localhost:8080/api/bookings -H "Authorization: Bearer $MT" -d '{...同上...}' | jq .booking.id)
CODE=$(curl -s -X POST localhost:8080/api/bookings/$BID/pay -H "Authorization: Bearer $MT" -d '{}' | jq -r .entry_code)

# 闸机扫码（硬件回调）
curl -s -X POST localhost:8080/api/devices/gate/scan -H "X-Device-Key: device-key-123" \
  -d "{\"entry_code\":\"$CODE\",\"signal_id\":\"gate-001\"}"

# 客服查看完整证据包
CT=$(curl -s -X POST localhost:8080/api/auth/staff/login -d '{"username":"cs","password":"cs123"}' | jq -r .token)
curl -s localhost:8080/api/cs/bookings/$BID/evidence -H "Authorization: Bearer $CT" | jq
```

---

## 6. 订单状态机与关键规则

```
pending_payment ──支付成功──▶ paid_waiting ──开场前10min──▶ warmup ──到点开灯──▶ in_play ──关灯──▶ finished
      │                            │                          │                   │
      │支付超时(worker)             │退订                       │退订                │延时→计费台账
      ▼                            ▼                          ▼                    └─场馆原因→ venue_closed
   expired                     cancelled                  cancelled
                                   无到场信号且过 late_cutoff(30min) ─▶ no_show（罚没+计数，3次封禁）
                                   有维护锁定 ─▶ venue_closed（全额退款，不计爽约）
```

- **并发安全**：`bookings` 上 `EXCLUDE USING gist (court_id WITH =, tstzrange(start_time,end_time,'[)') WITH &&)`，
  只对 `pending_payment/paid_waiting/warmup/in_play` 生效；数据库层兜底，应用层无需分布式锁。
  延时不计入排他区间——下一场可已付款等待，延时只进台账。
- **预热独立状态**：进入 `warmup` 后，支付超时扫描只认 `pending_payment`，绝不可能释放已预热订单。
- **爽约证据优先级**：① 有效维护锁定 → 场馆原因；② 闸机或前台任一到场信号 → 已到场
  （`waiting` 为已到场在场边等待）；③ 仅灯控自动开灯而无到场信号 → 爽约（灯开≠上场，可申诉）。
- **金额单位**：全部以**分**存储。延时费按所在时段费率、15 分钟向上取整；当场补偿时长优先抵扣延时。
- **补偿时长两种形态**：前台当场补偿（`comp_minutes_granted`，抵当场延时）；
  争议补偿入会员账户（`member_credits`，未来预约 FIFO 抵扣场地费）。

---

## 7. 代码结构

```
cmd/server/            程序入口（迁移、种子、worker、HTTP）
internal/
  config/              环境配置
  domain/              纯领域规则与计费（无 DB 依赖，含单元测试）
  store/               pgx 连接池、内置 SQL 迁移(go:embed)、行模型
  service/             业务事务：预约/支付/退订、闸机灯控、爽约引擎、争议、运营、认证、种子
  worker/              定时调度（超时释放/预热/自动开关灯/爽约扫描），支持手动触发一轮
  server/              HTTP 路由、角色鉴权、Swagger UI、内嵌 OpenAPI
scripts/e2e.py         端到端验证（66 断言）
scripts/reset-and-e2e.sh  一键重置+启动+e2e
Dockerfile / docker-compose.yml
```

主要数据表：`bookings`（订单与信号冗余）、`booking_events`（事件流）、`billing_ledger`（费用台账）、
`payments`、`maintenance_windows`、`extra_sessions`、`venue_policies`、`price_rules`、
`refund_requests`、`disputes`、`member_credits`、`ops_events`、`signal_dedup`。
