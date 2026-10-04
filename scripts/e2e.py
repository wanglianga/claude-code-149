#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
端到端验证：城市体育馆羽毛球场地预约 · 灯控计费与爽约处罚服务。
仅依赖 Python 标准库。运行前请用 scripts/reset-and-e2e.sh 重置数据库并启动服务。
"""
import json
import sys
import time
import urllib.request
import urllib.error
from datetime import datetime, timedelta, timezone

BASE = "http://127.0.0.1:18080"
DEVICE_KEY = "device-key-123"
PASS, FAIL = 0, 0
DETAIL = []


def http(method, path, token=None, body=None, device=False, expect=None):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if device:
        req.add_header("X-Device-Key", DEVICE_KEY)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            code, raw = r.status, r.read()
    except urllib.error.HTTPError as e:
        code, raw = e.code, e.read()
    obj = None
    if raw:
        try:
            obj = json.loads(raw)
        except Exception:
            obj = {"raw": raw.decode(errors="replace")}
    if expect is not None and code != expect:
        fatal(f"{method} {path} 期望 {expect} 实得 {code}: {obj}")
    return code, obj


def check(name, cond, extra=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  ✅ {name}")
    else:
        FAIL += 1
        print(f"  ❌ {name} {extra}")
        DETAIL.append(f"{name}: {extra}")


def fatal(msg):
    print(f"\n💥 致命错误: {msg}")
    sys.exit(2)


def iso(t):
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


def section(t):
    print(f"\n{'='*70}\n{t}\n{'='*70}")


# ---------- 登录 ----------
def staff_login(u, pw):
    _, o = http("POST", "/api/auth/staff/login", body={"username": u, "password": pw}, expect=200)
    return o["token"]


def member_login(phone):
    _, o = http("POST", "/api/auth/member/login",
                body={"phone": phone, "sms_code": "123456"}, expect=200)
    return o["token"], o["member"]["id"]


def set_clock(admin, t):
    http("POST", "/api/admin/clock/set", token=admin, body={"at": iso(t)}, expect=200)


def run_worker(admin):
    http("POST", "/api/admin/worker/run-once", token=admin, expect=200)


def book(member, venue, court, st, en, players=4, **kw):
    body = {"venue_id": venue, "court_id": court, "start_time": iso(st), "end_time": iso(en),
            "player_count": players}
    body.update(kw)
    return http("POST", "/api/bookings", token=member, body=body)


def get_bid(o):
    return o["booking"]["id"]


def main():
    section("0. 登录与初始化")
    admin = staff_login("admin", "admin123")
    oper = staff_login("operator", "oper123")
    front = staff_login("front", "front123")
    cs = staff_login("cs", "cs123")
    check("四类员工登录", all([admin, oper, front, cs]))
    _, obj = http("POST", "/api/auth/staff/login", body={"username": "admin", "password": "x"})
    check("错误密码被拒", _ == 401)
    mNormal, nid1 = member_login("13800000001")
    mSilver, nid2 = member_login("13800000002")
    mGold, nid3 = member_login("13800000003")
    check("三类会员登录", all([mNormal, mSilver, mGold]))
    _, cat = http("GET", "/api/catalog", token=mGold, expect=200)
    check("会员目录含场馆/场地/价格/策略",
          len(cat.get("venues", [])) >= 1 and len(cat.get("courts", [])) == 8 and
          len(cat.get("prices", [])) == 3 and len(cat.get("policies", [])) >= 1, str({k: len(v) for k, v in cat.items() if isinstance(v, list)}))

    # 固定时钟到周三 12:00 UTC，便于构造高峰/工作日场景
    t0 = datetime(2026, 10, 7, 12, 0, tzinfo=timezone.utc)
    set_clock(admin, t0)

    section("1. 报价：跨高峰分片 / 会员折扣 / 租拍购球 / 补偿时长")
    st = datetime(2026, 10, 7, 17, 30, tzinfo=timezone.utc)  # 跨 18:00
    en = st + timedelta(hours=1)
    _, q = http("POST", "/api/bookings/quote", token=mNormal,
                body={"venue_id": 1, "court_id": 1, "start_time": iso(st), "end_time": iso(en),
                      "player_count": 4, "rent_racket": True, "buy_shuttle": True}, expect=200)
    # 30min@6000 + 30min@10000 = 8000；租拍1000+购球2500 => 11500
    check("跨高峰场地费=8000", q["slot_cents"] == 8000, str(q))
    check("租拍+购球=3500", q["addons_cents"] == 3500, str(q))
    check("应付=11500", q["payable_cents"] == 11500, str(q))
    _, qg = http("POST", "/api/bookings/quote", token=mGold,
                 body={"venue_id": 1, "court_id": 1, "start_time": iso(st), "end_time": iso(en),
                       "player_count": 2}, expect=200)
    check("金卡 8 折：8000-1600=6400", qg["discount_cents"] == 1600 and qg["payable_cents"] == 6400, str(qg))

    section("2. 并发预约防重复锁定（排他约束）")
    st2 = datetime(2026, 10, 8, 14, 0, tzinfo=timezone.utc)  # 周四
    en2 = st2 + timedelta(hours=1)
    c1, o1 = book(mNormal, 1, 2, st2, en2)
    c2, o2 = book(mSilver, 1, 2, st2 + timedelta(minutes=30), en2 + timedelta(minutes=30))
    check("第一笔下单成功 201", c1 == 201, str(c1))
    check("重叠时段第二笔 409 冲突", c2 == 409 and "锁定" in o2.get("error", ""), str(o2))
    # 相邻时段不冲突
    c3, o3 = book(mSilver, 1, 2, en2, en2 + timedelta(hours=1))
    check("首尾相接不冲突 201", c3 == 201, str(o3))
    bid_b = get_bid(o1)
    bid_back = get_bid(o3)
    # 不同场地可同时段
    c4, o4 = book(mGold, 1, 3, st2, en2)
    check("不同场地同时段可预约", c4 == 201, str(o4))

    section("3. 支付超时释放 & 预热订单不被释放")
    b1 = get_bid(o1)  # pending_payment, court2 14:00
    set_clock(admin, t0 + timedelta(minutes=20))  # 超过 15 分钟支付时限
    run_worker(admin)
    _, expired = http("GET", f"/api/bookings/{bid_b}", token=mNormal, expect=200)
    check("超时未支付 → expired 释放", expired["status"] == "expired", str(expired["status"]))
    # 释放后场地可被再次预订
    c5, o5 = book(mSilver, 1, 2, st2, en2)
    check("释放后场地可再预订", c5 == 201, str(o5))
    warm_bid = get_bid(o5)
    _, paid = http("POST", f"/api/bookings/{warm_bid}/pay", token=mSilver, body={"method": "wechat"}, expect=200)
    check("支付成功发放入场码", paid["status"] == "paid_waiting" and bool(paid["entry_code"]), str(paid))
    # 时钟推到开场前 5 分钟（warmup=10min 窗口内）
    set_clock(admin, st2 - timedelta(minutes=5))
    run_worker(admin)
    _, wb = http("GET", f"/api/bookings/{warm_bid}", token=mSilver, expect=200)
    check("开场前 10 分钟自动预热 warmup", wb["status"] == "warmup" and bool(wb["light_warmup_at"]), str(wb["status"]))
    # 在 13:55 创建一笔 15:00 场订单：支付截止 14:10；创建后不跑 worker
    _, o408 = book(mSilver, 1, 2, en2, en2 + timedelta(hours=1))
    pay408 = get_bid(o408)
    # 14:11 直接付款（不触发 worker）→ 408
    set_clock(admin, st2 + timedelta(minutes=11))
    c6, o6 = http("POST", f"/api/bookings/{pay408}/pay", token=mSilver, body={})
    check("支付超时订单拒绝付款 408", c6 == 408, str(o6))
    _, wb2 = http("GET", f"/api/bookings/{warm_bid}", token=mSilver, expect=200)
    check("预热后的订单不被超时释放（已自动开灯 in_play）", wb2["status"] in ("warmup", "in_play"), str(wb2["status"]))
    run_worker(admin)
    _, expired2 = http("GET", f"/api/bookings/{pay408}", token=mSilver, expect=200)
    check("超时订单经 worker 释放为 expired", expired2["status"] == "expired", str(expired2["status"]))
    # 相邻时段的首笔未支付订单此前已被释放
    _, expiredBack = http("GET", f"/api/bookings/{bid_back}", token=mSilver, expect=200)
    check("首尾相接那笔未支付订单已 expired", expiredBack["status"] == "expired", str(expiredBack["status"]))

    section("4. 闸机放行窗口 / 早到 / 迟到罚金")
    gate_st = datetime(2026, 10, 9, 15, 0, tzinfo=timezone.utc)  # 周五 非高峰
    gate_en = gate_st + timedelta(hours=1)
    set_clock(admin, gate_st - timedelta(hours=2))
    _, og = book(mNormal, 1, 4, gate_st, gate_en)
    gate_bid = get_bid(og)
    http("POST", f"/api/bookings/{gate_bid}/pay", token=mNormal, body={}, expect=200)
    _, gg = http("GET", f"/api/bookings/{gate_bid}", token=mNormal, expect=200)
    code = gg["entry_code"]
    # 太早（提前超过 20 分钟）拒绝
    set_clock(admin, gate_st - timedelta(minutes=40))
    gc1, gr1 = http("POST", "/api/devices/gate/scan", device=True,
                    body={"entry_code": code, "gate_id": "G1", "signal_id": "sig-tooearly"})
    check("入场窗口外闸机拒绝 403", gc1 == 403, str(gr1))
    # 提前 15 分钟放行，arrival=early
    set_clock(admin, gate_st - timedelta(minutes=15))
    gc2, gr2 = http("POST", "/api/devices/gate/scan", device=True,
                    body={"entry_code": code, "gate_id": "G1", "signal_id": "sig-early"})
    check("提前到场放行", gc2 == 200 and gr2["result"]["arrival"] == "early", str(gr2))
    # 信号重放幂等
    gc3, gr3 = http("POST", "/api/devices/gate/scan", device=True,
                    body={"entry_code": code, "signal_id": "sig-early"})
    check("硬件信号重放幂等", gr2 is not None and gr3["result"].get("duplicate") is True, str(gr3))

    # 迟到场景：另一张单，开场 20 分钟后前台签到
    late_st = datetime(2026, 10, 9, 16, 30, tzinfo=timezone.utc)
    late_en = late_st + timedelta(hours=1)
    set_clock(admin, late_st - timedelta(hours=2))
    _, ol = book(mNormal, 1, 5, late_st, late_en)
    late_bid = get_bid(ol)
    http("POST", f"/api/bookings/{late_bid}/pay", token=mNormal, body={}, expect=200)
    set_clock(admin, late_st + timedelta(minutes=20))
    _, ls = http("POST", f"/api/staff/bookings/{late_bid}/frontdesk-sign", token=front, body={"note": "手机没电"}, expect=200)
    check("前台迟到签到 arrival=late", ls["arrival_status"] == "late", str(ls["arrival_status"]))
    _, led = http("GET", f"/api/staff/bookings/{late_bid}", token=front, expect=200)
    has_late_fee = any(x["kind"] == "late_fee" and x["amount_cents"] > 0 for x in led["ledger"])
    check("迟到罚金 20% 入台账且来源明确", has_late_fee, json.dumps(led["ledger"], ensure_ascii=False))

    section("5. 完整场次：开灯 / 延时 / 补偿时长抵扣 / 关灯结算")
    play_st = datetime(2026, 10, 10, 10, 0, tzinfo=timezone.utc)  # 周六 90/h
    play_en = play_st + timedelta(hours=1)
    set_clock(admin, play_st - timedelta(hours=2))
    _, op2 = book(mSilver, 1, 6, play_st, play_en, players=4)
    play_bid = get_bid(op2)
    http("POST", f"/api/bookings/{play_bid}/pay", token=mSilver, body={}, expect=200)
    set_clock(admin, play_st - timedelta(minutes=5))
    run_worker(admin)
    set_clock(admin, play_st)
    run_worker(admin)  # 自动开灯
    _, pb = http("GET", f"/api/staff/bookings/{play_bid}", token=front, expect=200)
    check("到点自动开灯 in_play", pb["booking"]["status"] == "in_play", str(pb["booking"]["status"]))
    # 闸机扫码（已到场）
    pcode = pb["booking"]["entry_code"]
    http("POST", "/api/devices/gate/scan", device=True, body={"entry_code": pcode, "signal_id": "sig-play"})
    # 前台补偿 15 分钟
    _, cm = http("POST", f"/api/staff/bookings/{play_bid}/comp-minutes", token=front,
                 body={"minutes": 15, "note": "前面场地等待补偿"}, expect=200)
    check("前台补偿 15 分钟", cm["comp_minutes_granted"] == 15, str(cm))
    # 实际打了 40 分钟延时（end+15 免费 => 计费 25 分钟 → 进位 30 分钟，周末 90/h => 4500）
    off_at = play_en + timedelta(minutes=40)
    set_clock(admin, off_at)
    _, lo = http("POST", "/api/devices/light/off", device=True,
                 body={"booking_id": play_bid, "off_at": iso(off_at), "signal_id": "sig-off-1"}, expect=200)
    check("延时 40min/补偿15 → 计费30min", lo["overtime_minutes"] == 40 and lo["overtime_fee_cents"] == 4500,
          str(lo))
    _, pl = http("GET", f"/api/staff/bookings/{play_bid}", token=front, expect=200)
    check("订单 finished 且实际结束时间留痕", pl["booking"]["status"] == "finished" and
          pl["booking"]["actual_end_time"] is not None, str(pl["booking"]["status"]))
    srcs = {x["kind"]: x["source"] for x in pl["ledger"]}
    check("台账来源含 frontdesk / light / system",
          srcs.get("comp_time", "").startswith("frontdesk:") and
          srcs.get("overtime_fee") == "light:off_overtime" and
          srcs.get("slot_fee") == "system:quote", json.dumps(srcs, ensure_ascii=False))

    section("6. 灰度：灯开了人没上场 / 前一场延时下一场等待")
    # 前一场 13:00-14:00 延时到 14:15（手动延时关灯）
    prev_st = datetime(2026, 10, 11, 13, 0, tzinfo=timezone.utc)  # 周日
    prev_en = prev_st + timedelta(hours=1)
    next_st = prev_en
    next_en = next_st + timedelta(hours=1)
    set_clock(admin, prev_st - timedelta(hours=2))
    _, opv = book(mNormal, 1, 7, prev_st, prev_en, players=4)
    prev_bid = get_bid(opv)
    http("POST", f"/api/bookings/{prev_bid}/pay", token=mNormal, body={}, expect=200)
    _, onx = book(mSilver, 1, 7, next_st, next_en, players=4)
    next_bid = get_bid(onx)
    http("POST", f"/api/bookings/{next_bid}/pay", token=mSilver, body={}, expect=200)
    set_clock(admin, prev_st)
    run_worker(admin)  # 前一场开灯（没人扫码 → 灯开但人没上场）
    # 下一场 13:55 到场扫码：前一场灯未关 → waiting
    set_clock(admin, next_st - timedelta(minutes=5))
    _, nb = http("GET", f"/api/bookings/{next_bid}", token=mSilver, expect=200)
    _, gr = http("POST", "/api/devices/gate/scan", device=True,
                 body={"entry_code": nb["entry_code"], "signal_id": "sig-next"})
    check("前一场延时：下一场扫码后在场边等待", gr["result"]["waiting_courtside"] is True, str(gr))
    _, nb2 = http("GET", f"/api/bookings/{next_bid}", token=mSilver, expect=200)
    check("arrival_status=waiting", nb2["arrival_status"] == "waiting", str(nb2["arrival_status"]))
    # 14:15 前一场关灯（延时 15 分钟 → 进位 15 分钟收费）
    set_clock(admin, prev_en + timedelta(minutes=15))
    _, lov = http("POST", "/api/devices/light/off", device=True,
                  body={"booking_id": prev_bid, "off_at": iso(prev_en + timedelta(minutes=15)),
                        "signal_id": "sig-prev-off"}, expect=200)
    check("前一场延时 15 分钟计费", lov["overtime_fee_cents"] > 0, str(lov))
    # 下一场前台确认开始，开灯
    http("POST", f"/api/staff/bookings/{next_bid}/waiting", token=front, body={"note": "14:15 上场"})
    _, lon = http("POST", "/api/devices/light/on", device=True,
                  body={"booking_id": next_bid, "signal_id": "sig-next-on"}, expect=200)
    check("等待后开灯进入 in_play", lon["status"] == "in_play", str(lon["status"]))
    # 事件流能区分“灯开”和“人上场”
    _, ev = http("GET", f"/api/staff/bookings/{prev_bid}", token=cs, expect=200)
    types_ = [e["event_type"] for e in ev["events"]]
    check("事件流含 light.on 且灯开时无人在场记录",
          "light.on" in types_, str(types_))
    # 正常关灯
    http("POST", "/api/devices/light/off", device=True,
         body={"booking_id": next_bid, "off_at": iso(next_en), "signal_id": "sig-next-off"}, expect=200)

    section("7. 灰度：会员带朋友拼场导致超员")
    po_st = datetime(2026, 10, 12, 10, 0, tzinfo=timezone.utc)  # 周一
    set_clock(admin, po_st - timedelta(hours=2))
    _, opo = book(mGold, 1, 8, po_st, po_st + timedelta(hours=1), players=4)
    po_bid = get_bid(opo)
    http("POST", f"/api/bookings/{po_bid}/pay", token=mGold, body={}, expect=200)
    set_clock(admin, po_st)
    run_worker(admin)
    c7, o7 = http("POST", f"/api/staff/bookings/{po_bid}/players", token=front, body={"players": 8})
    check("8 人超上限 2 人 → 4000 分超员费", c7 == 200 and o7["over_player_fee_cents"] == 4000, str(o7))
    c7b, o7b = http("POST", f"/api/staff/bookings/{po_bid}/players", token=front, body={"players": 8})
    check("重复上报同人数不重复收费", o7b["over_player_fee_cents"] == 0, str(o7b))
    # 预约阶段拒绝超员
    c8, o8 = book(mNormal, 1, 1, po_st, po_st + timedelta(hours=1), players=9)
    check("下单时超员被拦截 422", c8 == 422, str(o8))

    section("8. 退订规则（高峰/非高峰）")
    pk_st = datetime(2026, 10, 14, 19, 0, tzinfo=timezone.utc)  # 周三晚高峰
    set_clock(admin, pk_st - timedelta(hours=48))
    _, oc1 = book(mNormal, 1, 1, pk_st, pk_st + timedelta(hours=1), players=2)
    cancel_free = get_bid(oc1)
    http("POST", f"/api/bookings/{cancel_free}/pay", token=mNormal, body={}, expect=200)
    set_clock(admin, pk_st - timedelta(hours=30))
    _, cf = http("POST", f"/api/bookings/{cancel_free}/cancel", token=mNormal,
                 body={"reason": "提前 30 小时计划变更"}, expect=200)
    check("高峰提前≥24h 免费全退", cf["fee_cents"] == 0 and cf["refund_cents"] > 0, str(cf))
    # 不足 24h 高峰退订 50%
    set_clock(admin, pk_st - timedelta(hours=48))
    _, oc2 = book(mSilver, 1, 1, pk_st + timedelta(hours=1), pk_st + timedelta(hours=2), players=2)
    cancel_50 = get_bid(oc2)
    _, payobj = http("POST", f"/api/bookings/{cancel_50}/pay", token=mSilver, body={}, expect=200)
    paid_total = payobj["paid_cents"]
    set_clock(admin, pk_st - timedelta(hours=2))
    _, c50 = http("POST", f"/api/bookings/{cancel_50}/cancel", token=mSilver,
                  body={"reason": "临时有事"}, expect=200)
    check("高峰不足24h 扣 50%", c50["fee_cents"] == int(paid_total * 0.5) and
          c50["refund_cents"] == int(paid_total * 0.5), f"{c50} paid={paid_total}")
    # 未支付直接取消
    _, oc3 = book(mNormal, 1, 2, pk_st, pk_st + timedelta(hours=1), players=2)
    cancel_unpaid = get_bid(oc3)
    _, cu = http("POST", f"/api/bookings/{cancel_unpaid}/cancel", token=mNormal, body={"reason": "不要了"}, expect=200)
    check("未支付订单取消无费用", cu["booking"]["status"] == "cancelled", str(cu))

    section("9. 爽约判定：无到场信号 → 爽约罚没；3 次封禁")
    noshow_st = datetime(2026, 10, 15, 19, 0, tzinfo=timezone.utc)
    set_clock(admin, noshow_st - timedelta(hours=2))
    ns_ids = []
    for court in (1, 2, 3):
        _, ons = book(mNormal, 1, court, noshow_st, noshow_st + timedelta(hours=1), players=4)
        bid = get_bid(ons)
        http("POST", f"/api/bookings/{bid}/pay", token=mNormal, body={}, expect=200)
        ns_ids.append(bid)
    # 开场后 40 分钟（超过 30 分钟截止）且无任何到场信号
    set_clock(admin, noshow_st + timedelta(minutes=40))
    run_worker(admin)
    for i, bid in enumerate(ns_ids):
        _, nb_ = http("GET", f"/api/bookings/{bid}", token=mNormal, expect=200)
        check(f"爽约订单 {i+1} → no_show/forfeited",
              nb_["status"] == "no_show" and nb_["pay_status"] == "forfeited",
              f"{nb_['status']}/{nb_['pay_status']}")
    # 会员已被封禁（阈值 3）
    _, login_resp = http("POST", "/api/auth/member/login",
                         body={"phone": "13800000001", "sms_code": "123456"})
    check("累计 3 次爽约 → 封禁", login_resp["member"]["banned"] is True, str(login_resp["member"]))
    cb, ob_ = book(mNormal, 1, 4, noshow_st + timedelta(hours=2), noshow_st + timedelta(hours=3))
    check("被封禁会员无法预约 422", cb == 422, str(ob_))
    # 灯开但无人扫码的订单仍判爽约（事件流中 light_note 说明）
    bid0 = ns_ids[0]
    _, ev0 = http("GET", f"/api/cs/bookings/{bid0}/evidence", token=cs, expect=200)
    ev_types = [e["event_type"] for e in ev0["events"]]
    check("爽约订单有 noshow.classified 证据事件", "noshow.classified" in ev_types, str(ev_types))

    section("10. 客服争议：撤销爽约 → 退还罚没、次数-1、补偿时长入账")
    # 客服对第 1 笔爽约开争议（主张灯控故障）
    _, do2 = http("POST", "/api/cs/disputes", token=cs,
                  body={"booking_id": bid0, "reason": "会员称到场但闸机故障无法扫码"}, expect=201)
    dispute_id = do2["dispute_id"]
    _, dr = http("POST", f"/api/cs/disputes/{dispute_id}/resolve", token=cs, body={
        "kind": "mixed", "refund_amount_cents": 3000, "comp_minutes": 60,
        "waive_no_show": True, "note": "监控显示确实到场，闸机故障"}, expect=200)
    check("争议裁决：退款+60min补偿+撤销爽约",
          dr["waive_no_show"] is True and dr["comp_minutes"] == 60 and dr["refund_amount_cents"] == 3000,
          str(dr))
    _, eb = http("GET", f"/api/bookings/{bid0}", token=mNormal, expect=200)
    check("爽约标记已撤销", eb["no_show_counted"] is False, str(eb))
    _, cr2 = http("GET", "/api/members/me/credits", token=mNormal, expect=200)
    check("会员补偿时长账户=60 分钟", cr2["remaining_minutes"] == 60, str(cr2))
    # 运营解除封禁
    http("POST", f"/api/admin/members/{nid1}/unban", token=admin, expect=200)
    _, minfo = http("POST", "/api/auth/member/login",
                    body={"phone": "13800000001", "sms_code": "123456"}, expect=200)
    check("解封后可再预约", minfo["member"]["banned"] is False, str(minfo["member"]))

    section("11. 补偿时长用于下次预约（权益抵扣）")
    use_st = datetime(2026, 10, 16, 15, 0, tzinfo=timezone.utc)
    set_clock(admin, use_st - timedelta(hours=2))
    _, uq = http("POST", "/api/bookings/quote", token=mNormal, body={
        "venue_id": 1, "court_id": 4, "start_time": iso(use_st), "end_time": iso(use_st + timedelta(hours=1)),
        "player_count": 2, "use_credit_minutes": 60}, expect=200)
    check("60 分钟补偿全额抵扣场地费", uq["credit_minutes"] == 60 and
          uq["credit_cents"] == uq["slot_cents"] - uq["discount_cents"], str(uq))
    _, ou = book(mNormal, 1, 4, use_st, use_st + timedelta(hours=1), players=2, use_credit_minutes=60)
    use_bid = get_bid(ou)
    http("POST", f"/api/bookings/{use_bid}/pay", token=mNormal, body={}, expect=200)
    _, ub = http("GET", f"/api/bookings/{use_bid}", token=mNormal, expect=200)
    check("抵扣后实付为 0 仍发放入场码", ub["paid_cents"] == 0 and bool(ub["entry_code"]), str(ub))
    _, cr3 = http("GET", "/api/members/me/credits", token=mNormal, expect=200)
    check("补偿时长账户已扣减为 0", cr3["remaining_minutes"] == 0, str(cr3))

    section("12. 场地临时维护 → 自动场馆原因全额退款 + 占用视图可见")
    mt_st = datetime(2026, 10, 17, 10, 0, tzinfo=timezone.utc)
    set_clock(admin, mt_st - timedelta(hours=2))
    _, om = book(mSilver, 1, 5, mt_st, mt_st + timedelta(hours=1), players=4)
    maint_bid = get_bid(om)
    http("POST", f"/api/bookings/{maint_bid}/pay", token=mSilver, body={}, expect=200)
    _, mo = http("POST", "/api/admin/maintenance", token=oper, body={
        "venue_id": 1, "court_id": 5, "start_time": iso(mt_st), "end_time": iso(mt_st + timedelta(hours=2)),
        "reason": "地板抢修"}, expect=201)
    check("维护锁定命中受影响订单", maint_bid in mo["affected_bookings"], str(mo))
    _, mb2 = http("GET", f"/api/bookings/{maint_bid}", token=mSilver, expect=200)
    check("受影响订单 venue_closed + 全额退款",
          mb2["status"] == "venue_closed" and mb2["pay_status"] == "refunded" and
          mb2["refund_cents"] == mb2["paid_cents"], f"{mb2['status']}/{mb2['pay_status']}")
    # 维护期间不能预约
    mc, mresp = book(mGold, 1, 5, mt_st + timedelta(minutes=30), mt_st + timedelta(minutes=90))
    check("维护时段预约被拒 409", mc == 409 and "维护" in mresp.get("error", ""), str(mresp))
    # 占用视图
    _, av = http("GET", f"/api/venues/1/availability?date=2026-10-17", token=mGold, expect=200)
    kinds = [s["kind"] for c in av["courts"] for s in c["slots"]]
    check("占用视图包含 maintenance/booking", "maintenance" in kinds, str(kinds))
    # 解除维护
    maint_id = mo["maintenance_id"]
    http("POST", f"/api/admin/maintenance/{maint_id}/release", token=oper, expect=200)

    section("13. 临时加场：非营业时间可约 / 会员优先预约天数")
    # 营业时间 08:00-22:00；早上 7:00 无加场时拒绝
    early_st = datetime(2026, 10, 18, 7, 0, tzinfo=timezone.utc)
    set_clock(admin, datetime(2026, 10, 17, 12, 0, tzinfo=timezone.utc))
    ec1, er1 = book(mGold, 1, 6, early_st, early_st + timedelta(hours=1))
    check("非营业时间无加场 → 422", ec1 == 422, str(er1))
    http("POST", "/api/admin/extra-sessions", token=oper, body={
        "venue_id": 1, "court_id": 6, "start_time": iso(early_st),
        "end_time": iso(early_st + timedelta(hours=1)), "note": "早鸟加场"}, expect=201)
    ec2, _ = book(mGold, 1, 6, early_st, early_st + timedelta(hours=1))
    check("有临时加场 → 可预约", ec2 == 201)
    # 会员优先：normal 提前 2 天上限，gold 可提前 7 天
    far_st = datetime(2026, 10, 24, 11, 0, tzinfo=timezone.utc)  # 距 10-17 12:00 为 6 天 23 小时
    far_en = far_st + timedelta(hours=1)
    gc1x, gr1x = book(mNormal, 1, 1, far_st, far_en)
    check("普通会员提前超 2 天被拒", gc1x == 422 and "提前" in gr1x.get("error", ""), str(gr1x))
    gc2x, _ = book(mGold, 1, 1, far_st, far_en)
    check("金卡提前 7 天可约", gc2x == 201, str(gc2x))

    section("14. 退款申请审批 + 证据包完整性")
    _, rr = http("POST", f"/api/bookings/{use_bid}/refund-requests", token=mNormal,
                 body={"reason": "事后发现服务未提供"}, expect=201)
    req_id = rr["refund_request_id"]
    _, rl = http("GET", "/api/cs/refund-requests?status=pending", token=cs, expect=200)
    check("客服看到待审批申请", any(x["id"] == req_id for x in rl["refund_requests"]), str(rl)[:200])
    http("POST", f"/api/cs/refund-requests/{req_id}/decision", token=cs,
         body={"approve": False, "note": "已正常入场使用，驳回"}, expect=200)
    # 重复审批报错
    dc, dresp = http("POST", f"/api/cs/refund-requests/{req_id}/decision", token=cs,
                     body={"approve": True, "note": "x"})
    check("已处理申请不可重复审批", dc == 400, str(dresp))
    # 证据包字段
    _, bundle = http("GET", f"/api/cs/bookings/{play_bid}/evidence", token=cs, expect=200)
    check("证据包六类齐全",
          all(k in bundle for k in ("booking", "events", "ledger", "payments", "maintenance", "disputes")),
          str(list(bundle.keys())))
    check("证据包含闸机/灯控/前台多源事件",
          {"gate", "light", "frontdesk"} <= {e["actor_type"] for e in bundle["events"]},
          str([e["actor_type"] for e in bundle["events"]]))

    section("15. 前台场馆原因退订（全额、不计爽约、补偿时长归还）")
    vf_st = datetime(2026, 10, 19, 10, 0, tzinfo=timezone.utc)
    set_clock(admin, vf_st - timedelta(hours=2))
    _, ovf = book(mSilver, 1, 3, vf_st, vf_st + timedelta(hours=1), players=4)
    vf_bid = get_bid(ovf)
    http("POST", f"/api/bookings/{vf_bid}/pay", token=mSilver, body={}, expect=200)
    _, vf = http("POST", f"/api/staff/bookings/{vf_bid}/cancel", token=front,
                 body={"reason": "场地临时不可用", "venue_fault": True}, expect=200)
    check("前台场馆原因退订全退", vf["booking"]["status"] == "venue_closed" and
          vf["refund_cents"] == vf["booking"]["paid_cents"], f"{vf['booking']['status']}")

    print(f"\n{'#'*70}")
    print(f"#  结果：✅ {PASS} 通过   ❌ {FAIL} 失败")
    print(f"{'#'*70}")
    if FAIL:
        for d in DETAIL:
            print(" -", d)
        sys.exit(1)


if __name__ == "__main__":
    main()
