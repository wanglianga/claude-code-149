-- 城市体育馆羽毛球场地预约：初始化 schema
-- 关键并发保障：btree_gist 排他约束，同一场地的有效订单时段不可重叠
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ---------- 场馆 / 场地 ----------
CREATE TABLE venues (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    address      TEXT NOT NULL DEFAULT '',
    open_time    TIME NOT NULL DEFAULT '08:00',
    close_time   TIME NOT NULL DEFAULT '22:30',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE courts (
    id         BIGSERIAL PRIMARY KEY,
    venue_id   BIGINT NOT NULL REFERENCES venues(id),
    court_no   TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active','maintenance','disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue_id, court_no)
);
CREATE INDEX idx_courts_venue ON courts(venue_id);

-- ---------- 会员 / 员工 ----------
CREATE TABLE members (
    id             BIGSERIAL PRIMARY KEY,
    phone          TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    member_tier    TEXT NOT NULL DEFAULT 'normal'
                     CHECK (member_tier IN ('normal','silver','gold')),
    no_show_count  INT NOT NULL DEFAULT 0,
    banned         BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE staff_users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    name          TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('admin','operator','frontdesk','cs')),
    venue_id      BIGINT REFERENCES venues(id), -- NULL = 全部场馆
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 场馆规则与计价 ----------
CREATE TABLE venue_policies (
    venue_id                  BIGINT PRIMARY KEY REFERENCES venues(id),
    payment_timeout_min       INT  NOT NULL DEFAULT 15,   -- 支付超时（分钟），超时释放
    warmup_minutes            INT  NOT NULL DEFAULT 10,   -- 提前灯控预热
    early_entry_minutes       INT  NOT NULL DEFAULT 20,   -- 闸机提前放行窗口
    grace_minutes             INT  NOT NULL DEFAULT 10,   -- 开场后宽限，不算迟到
    late_cutoff_minutes       INT  NOT NULL DEFAULT 30,  -- 超过仍无入场信号 => 爽约
    late_fee_ratio            NUMERIC(6,4) NOT NULL DEFAULT 0.20,
    peak_start                TIME NOT NULL DEFAULT '18:00',
    peak_end                  TIME NOT NULL DEFAULT '22:00',
    peak_cancel_free_hours    NUMERIC(6,2) NOT NULL DEFAULT 24,
    peak_cancel_fee_ratio     NUMERIC(6,4) NOT NULL DEFAULT 0.50,
    offpeak_cancel_free_hours NUMERIC(6,2) NOT NULL DEFAULT 2,
    offpeak_cancel_fee_ratio  NUMERIC(6,4) NOT NULL DEFAULT 0.30,
    no_show_ban_threshold     INT  NOT NULL DEFAULT 3,
    max_players               INT  NOT NULL DEFAULT 6,
    per_extra_player_cents    INT  NOT NULL DEFAULT 2000, -- 超员每人加价（分）
    tier_book_ahead_days      JSONB NOT NULL DEFAULT '{"normal":2,"silver":4,"gold":7}'::jsonb,
    tier_discount_pct         JSONB NOT NULL DEFAULT '{"normal":0,"silver":10,"gold":20}'::jsonb,
    updated_by                BIGINT REFERENCES staff_users(id),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- scope: weekday_offpeak / weekday_peak / weekend
CREATE TABLE price_rules (
    id               BIGSERIAL PRIMARY KEY,
    venue_id         BIGINT NOT NULL REFERENCES venues(id),
    scope            TEXT NOT NULL CHECK (scope IN ('weekday_offpeak','weekday_peak','weekend')),
    hourly_rate_cents  INT NOT NULL,
    racket_fee_cents   INT NOT NULL DEFAULT 0, -- 租拍（按次/整场）
    shuttle_fee_cents  INT NOT NULL DEFAULT 0, -- 购球（按次/整场）
    updated_by       BIGINT REFERENCES staff_users(id),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue_id, scope)
);

-- ---------- 临时加场（非常规开放时段） ----------
CREATE TABLE extra_sessions (
    id          BIGSERIAL PRIMARY KEY,
    venue_id    BIGINT NOT NULL REFERENCES venues(id),
    court_id    BIGINT NOT NULL REFERENCES courts(id),
    start_time  TIMESTAMPTZ NOT NULL,
    end_time    TIMESTAMPTZ NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    created_by  BIGINT REFERENCES staff_users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    cancelled_at TIMESTAMPTZ,
    CHECK (end_time > start_time)
);
CREATE INDEX idx_extra_court_time ON extra_sessions(court_id, start_time, end_time);

-- ---------- 场地维护锁定 ----------
CREATE TABLE maintenance_windows (
    id          BIGSERIAL PRIMARY KEY,
    venue_id    BIGINT NOT NULL REFERENCES venues(id),
    court_id    BIGINT NOT NULL REFERENCES courts(id),
    start_time  TIMESTAMPTZ NOT NULL,
    end_time    TIMESTAMPTZ NOT NULL,
    reason      TEXT NOT NULL,
    created_by  BIGINT REFERENCES staff_users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ,
    CHECK (end_time > start_time)
);
CREATE INDEX idx_maint_court_time ON maintenance_windows(court_id, start_time, end_time);

-- ---------- 预约订单 ----------
CREATE TABLE bookings (
    id                   BIGSERIAL PRIMARY KEY,
    order_no             TEXT NOT NULL UNIQUE,
    venue_id             BIGINT NOT NULL REFERENCES venues(id),
    court_id             BIGINT NOT NULL REFERENCES courts(id),
    member_id            BIGINT NOT NULL REFERENCES members(id),
    member_tier_snapshot TEXT NOT NULL,
    start_time           TIMESTAMPTZ NOT NULL,
    end_time             TIMESTAMPTZ NOT NULL,
    player_count         INT NOT NULL,
    rent_racket          BOOLEAN NOT NULL DEFAULT false,
    buy_shuttle          BOOLEAN NOT NULL DEFAULT false,

    status               TEXT NOT NULL DEFAULT 'pending_payment' CHECK (status IN (
                           'pending_payment', -- 待支付，临时锁场
                           'paid_waiting',    -- 已支付待开场
                           'warmup',          -- 灯控预热中（独立状态，不可按支付超时释放）
                           'in_play',         -- 进行中（灯已开）
                           'finished',        -- 正常结束
                           'cancelled',       -- 用户/前台退订
                           'expired',         -- 支付超时释放
                           'no_show',         -- 爽约
                           'venue_closed')),  -- 场馆原因（维护等）关闭
    arrival_status       TEXT NOT NULL DEFAULT 'unknown' CHECK (arrival_status IN (
                           'unknown','early','ontime','late','waiting','no_show','venue_fault')),

    pay_status           TEXT NOT NULL DEFAULT 'unpaid' CHECK (pay_status IN (
                           'unpaid','paid','partially_refunded','refunded','forfeited')),
    pay_deadline         TIMESTAMPTZ,
    paid_at              TIMESTAMPTZ,
    entry_code           TEXT UNIQUE,

    -- 报价快照（分）
    slot_cents           INT NOT NULL DEFAULT 0,
    discount_cents       INT NOT NULL DEFAULT 0, -- 会员等级折扣
    credit_cents         INT NOT NULL DEFAULT 0, -- 补偿时长/权益抵扣金额
    credit_minutes_used  INT NOT NULL DEFAULT 0,
    addons_cents         INT NOT NULL DEFAULT 0,
    surcharge_cents      INT NOT NULL DEFAULT 0, -- 迟到罚金/超员/延时累计
    paid_cents           INT NOT NULL DEFAULT 0,
    refund_cents         INT NOT NULL DEFAULT 0,

    -- 闸机 / 灯控 / 前台信号（事件流为准，此处为便于查询的冗余）
    gate_opened_at       TIMESTAMPTZ,
    front_signed_at      TIMESTAMPTZ,
    checked_in_at        TIMESTAMPTZ,
    checkin_via          TEXT CHECK (checkin_via IN ('gate','frontdesk')),
    light_warmup_at      TIMESTAMPTZ,
    light_on_at          TIMESTAMPTZ,
    light_off_at         TIMESTAMPTZ,
    overtime_minutes     INT NOT NULL DEFAULT 0,
    comp_minutes_granted INT NOT NULL DEFAULT 0,
    actual_start_time    TIMESTAMPTZ,
    actual_end_time      TIMESTAMPTZ,
    over_players         INT NOT NULL DEFAULT 0,

    cancel_time          TIMESTAMPTZ,
    cancel_by            TEXT,
    cancel_reason        TEXT,
    no_show_classified_at TIMESTAMPTZ,
    no_show_counted      BOOLEAN NOT NULL DEFAULT false,

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (end_time > start_time)
);

-- 同一场地，所有占场订单（含待支付锁场）的【预约窗口】不得重叠。
-- 注意：延时（actual_end_time）不计入排他范围——延时是现场灰度事件，
-- 下一场可能已付款等待；延时通过 billing_ledger 与事件流记录，不改变锁场区间。
ALTER TABLE bookings ADD CONSTRAINT bookings_court_excl
EXCLUDE USING gist (
    court_id WITH =,
    tstzrange(start_time, end_time, '[)') WITH &&
) WHERE (status IN ('pending_payment','paid_waiting','warmup','in_play'));

CREATE INDEX idx_bookings_member ON bookings(member_id, created_at DESC);
CREATE INDEX idx_bookings_court_time ON bookings(court_id, start_time);
CREATE INDEX idx_bookings_status_start ON bookings(status, start_time);
CREATE INDEX idx_bookings_pay_deadline ON bookings(pay_deadline) WHERE status = 'pending_payment';

-- ---------- 支付 / 退款 ----------
CREATE TABLE payments (
    id          BIGSERIAL PRIMARY KEY,
    booking_id  BIGINT NOT NULL REFERENCES bookings(id),
    direction   TEXT NOT NULL CHECK (direction IN ('pay','refund')),
    amount_cents INT NOT NULL CHECK (amount_cents > 0),
    method      TEXT NOT NULL DEFAULT 'wechat',
    status      TEXT NOT NULL DEFAULT 'succeeded' CHECK (status IN ('succeeded','failed')),
    reason      TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT 'member',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_payments_booking ON payments(booking_id);

-- ---------- 计费台账：每一笔费用/减免都有明确来源 ----------
CREATE TABLE billing_ledger (
    id           BIGSERIAL PRIMARY KEY,
    booking_id   BIGINT NOT NULL REFERENCES bookings(id),
    kind         TEXT NOT NULL CHECK (kind IN (
                   'slot_fee','racket_fee','shuttle_fee','member_discount',
                   'overtime_fee','over_player_fee','late_fee',
                   'cancel_fee','no_show_forfeit',
                   'refund_cancel','refund_venue','refund_dispute','comp_time')),
    amount_cents INT NOT NULL,            -- 正数收费 / 负数退款折扣；comp_time 可为 0
    minutes      INT NOT NULL DEFAULT 0,  -- comp_time / 延时类条目的分钟数
    source       TEXT NOT NULL,           -- system / staff:42 / dispute:7 / maintenance:3
    reason       TEXT NOT NULL DEFAULT '',
    ref_type     TEXT NOT NULL DEFAULT '',
    ref_id       BIGINT,
    created_by   TEXT NOT NULL DEFAULT 'system',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ledger_booking ON billing_ledger(booking_id, id);

-- ---------- 事件流：闸机/灯控/前台/退款/系统 全部留痕 ----------
CREATE TABLE booking_events (
    id          BIGSERIAL PRIMARY KEY,
    booking_id  BIGINT NOT NULL REFERENCES bookings(id),
    event_type  TEXT NOT NULL,
    actor_type  TEXT NOT NULL DEFAULT 'system'
                  CHECK (actor_type IN ('member','system','gate','light','frontdesk','operator','cs')),
    actor_id    TEXT NOT NULL DEFAULT '',
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_booking_time ON booking_events(booking_id, created_at);
CREATE INDEX idx_events_type_time ON booking_events(event_type, created_at);

-- ---------- 退款申请 ----------
CREATE TABLE refund_requests (
    id            BIGSERIAL PRIMARY KEY,
    booking_id    BIGINT NOT NULL REFERENCES bookings(id),
    applicant_id  BIGINT NOT NULL REFERENCES members(id),
    amount_cents  INT NOT NULL,
    reason        TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    decided_by    BIGINT REFERENCES staff_users(id),
    decision_note TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at    TIMESTAMPTZ
);
CREATE INDEX idx_refund_req_booking ON refund_requests(booking_id);

-- ---------- 争议处理 ----------
CREATE TABLE disputes (
    id                  BIGSERIAL PRIMARY KEY,
    booking_id          BIGINT NOT NULL REFERENCES bookings(id),
    opened_by           BIGINT NOT NULL REFERENCES staff_users(id),
    reason              TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
    resolution_kind     TEXT CHECK (resolution_kind IN
                        ('refund','comp_time','exonerate','mixed','no_action')),
    refund_amount_cents INT NOT NULL DEFAULT 0,
    comp_minutes        INT NOT NULL DEFAULT 0,
    waive_no_show       BOOLEAN NOT NULL DEFAULT false,
    decided_by          BIGINT REFERENCES staff_users(id),
    decision_note       TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at         TIMESTAMPTZ
);
CREATE INDEX idx_disputes_booking ON disputes(booking_id);

-- ---------- 会员补偿时长账户（争议/前台补偿入账，预约时抵扣） ----------
CREATE TABLE member_credits (
    id               BIGSERIAL PRIMARY KEY,
    member_id        BIGINT NOT NULL REFERENCES members(id),
    minutes          INT NOT NULL,           -- 授予分钟数
    remaining_minutes INT NOT NULL,          -- 剩余
    source           TEXT NOT NULL,          -- dispute:3 / frontdesk:staff
    booking_id       BIGINT REFERENCES bookings(id),
    note             TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_credits_member ON member_credits(member_id) WHERE remaining_minutes > 0;

-- ---------- 员工登录令牌 ----------
CREATE TABLE staff_tokens (
    token       TEXT PRIMARY KEY,
    staff_id    BIGINT NOT NULL REFERENCES staff_users(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 会员登录令牌 ----------
CREATE TABLE member_tokens (
    token       TEXT PRIMARY KEY,
    member_id   BIGINT NOT NULL REFERENCES members(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 硬件回调幂等（闸机/灯控信号可能重放） ----------
CREATE TABLE signal_dedup (
    signal_key  TEXT PRIMARY KEY,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 运营操作事件（不绑定订单：维护/加场/策略变更等，供客服追溯来源） ----------
CREATE TABLE ops_events (
    id          BIGSERIAL PRIMARY KEY,
    venue_id    BIGINT,
    ref_type    TEXT NOT NULL DEFAULT '',  -- maintenance / extra_session / policy / court
    ref_id      BIGINT,
    event_type  TEXT NOT NULL,
    actor_type  TEXT NOT NULL DEFAULT 'operator',
    actor_id    TEXT NOT NULL DEFAULT '',
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ops_events_ref ON ops_events(ref_type, ref_id);
