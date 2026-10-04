-- 城市体育馆 羽毛球场地预约 / 灯控 / 计费 / 爽约处罚 初始化 schema
-- 金额单位：分（bigint），时间：timestamptz

create extension if not exists btree_gist;

-- 迁移记录
create table if not exists schema_migrations (
  version text primary key,
  applied_at timestamptz not null default now()
);

-- ============ 场馆 ============
create table if not exists venues (
  id                              bigserial primary key,
  name                            text not null unique,
  address                         text not null default '',
  opens_at                        time not null default '08:00',
  closes_at                       time not null default '22:00',
  price_cents_per_hour            bigint not null default 6000,   -- 平时段 60 元/小时
  peak_multiplier                 numeric(6,4) not null default 1.6667, -- 高峰系数（60 -> 100）
  overtime_multiplier             numeric(6,4) not null default 1.5000,
  racket_fee_cents                bigint not null default 1000,   -- 租拍 10 元/支
  shuttle_fee_cents               bigint not null default 1500,   -- 球 15 元/筒
  default_court_capacity          int not null default 6,
  overlimit_fee_cents             bigint not null default 2000,   -- 超员 20 元/人
  payment_timeout_seconds         int not null default 900,       -- 支付超时 15 分钟
  light_warmup_minutes            int not null default 10,        -- 开灯预热提前量
  checkin_window_minutes          int not null default 20,        -- 提前到场放行窗口
  late_grace_minutes              int not null default 15,        -- 迟到宽限
  noshow_judge_minutes            int not null default 20,        -- 开场后多久裁决
  idle_light_off_minutes          int not null default 15,        -- 开灯无人自动关灯
  max_auto_overtime_minutes       int not null default 30,        -- 系统允许的自动延时
  member_only_peak_release_hours  int not null default 24,        -- 高峰时段会员优先窗口
  noshow_penalty_count            int not null default 1,
  created_at                      timestamptz not null default now()
);

-- ============ 场地 ============
create table if not exists courts (
  id          bigserial primary key,
  venue_id    bigint not null references venues(id),
  court_no    text not null,
  capacity    int,                                   -- null 时用场馆默认
  status      text not null default 'active',        -- active / maintenance
  created_at  timestamptz not null default now(),
  unique (venue_id, court_no)
);

-- ============ 会员 ============
create table if not exists members (
  id             bigserial primary key,
  phone          text not null unique,
  name           text not null,
  password_hash  text,
  member_type    text not null default 'normal' check (member_type in ('normal','silver','gold')),
  no_show_count  int not null default 0,
  status         text not null default 'active',
  created_at     timestamptz not null default now()
);

-- 会员权益策略（每个场馆、每种会员类型一行）
create table if not exists member_type_policies (
  venue_id              bigint not null references venues(id),
  member_type           text not null check (member_type in ('normal','silver','gold')),
  discount_percent      int not null default 100,   -- 应付百分比，90 = 9 折
  free_hours_per_month  numeric(5,1) not null default 0,
  max_advance_hours     int not null default 48,    -- 可提前预约小时数（会员优先）
  primary key (venue_id, member_type)
);

-- 会员当月免费时长已用额度
create table if not exists member_benefit_usage (
  id              bigserial primary key,
  member_id       bigint not null references members(id),
  venue_id        bigint not null references venues(id),
  year_month      text not null,                    -- '2026-10'
  free_minutes_used int not null default 0,
  free_minutes_reserved int not null default 0,    -- held 未支付订单的预占
  unique (member_id, venue_id, year_month)
);

-- ============ 规则 ============
-- 高峰窗口（weekday: 0=周日 ... 6=周六）
create table if not exists peak_windows (
  id          bigserial primary key,
  venue_id    bigint not null references venues(id),
  weekday     smallint not null check (weekday between 0 and 6),
  start_time  time not null,
  end_time    time not null
);

-- 退订规则：距离开场 >= min_hours_before 时，退 refund_percent%
create table if not exists cancellation_rules (
  id                 bigserial primary key,
  venue_id           bigint not null references venues(id),
  is_peak            boolean not null,
  min_hours_before   numeric(6,1) not null,
  refund_percent     int not null check (refund_percent between 0 and 100),
  unique (venue_id, is_peak, min_hours_before)
);

-- 临时加场（在常规营业时段之外开放的时段；court_id 为空表示全场馆）
create table if not exists extra_slots (
  id          bigserial primary key,
  venue_id    bigint not null references venues(id),
  court_id    bigint references courts(id),
  slot_date   date not null,
  opens_at    time not null,
  closes_at   time not null,
  note        text not null default '',
  created_by  bigint,
  created_at  timestamptz not null default now()
);

-- ============ 场地维护锁定 ============
create table if not exists maintenance_windows (
  id            bigserial primary key,
  venue_id      bigint not null references venues(id),
  court_id      bigint not null references courts(id),
  start_time    timestamptz not null,
  end_time      timestamptz not null,
  reason        text not null default '',
  status        text not null default 'active' check (status in ('active','released')),
  created_by    bigint,
  released_by   bigint,
  released_at   timestamptz,
  created_at    timestamptz not null default now()
);
create index if not exists idx_maintenance_court_time on maintenance_windows(court_id, start_time, end_time);

-- ============ 预约 ============
create table if not exists bookings (
  id                    bigserial primary key,
  booking_no            text not null unique,
  venue_id              bigint not null references venues(id),
  court_id              bigint not null references courts(id),
  member_id             bigint not null references members(id),
  member_type_snap      text not null,
  member_name_snap      text not null,
  party_size            int not null check (party_size >= 1),
  capacity_snapshot     int not null,
  start_time            timestamptz not null,
  end_time              timestamptz not null,
  rent_racket           boolean not null default false,
  racket_count          int not null default 0,
  buy_shuttle           boolean not null default false,
  shuttle_count         int not null default 0,
  -- 计费（单位：分）
  court_fee_cents       bigint not null default 0,
  addon_cents           bigint not null default 0,
  discount_cents        bigint not null default 0,
  benefit_cents         bigint not null default 0, -- 会员免费时长抵扣
  benefit_minutes       int not null default 0,    -- 本次使用的权益分钟数
  benefit_monthly_minutes int not null default 0,  -- 其中月度免费时长
  benefit_grant_minutes int not null default 0,    -- 其中补偿券时长
  overlimit_cents       bigint not null default 0,
  payable_cents         bigint not null default 0,
  paid_cents            bigint not null default 0,
  refunded_cents        bigint not null default 0,
  overtime_cents        bigint not null default 0,
  late_adjust_cents     bigint not null default 0, -- 因前一场延时、晚开的补偿
  -- 状态
  status                text not null check (status in (
                          'held','paid','warming','in_use',
                          'completed','cancelled','expired','no_show','venue_fault','disputed')),
  entry_code            text not null unique,
  expires_at            timestamptz,
  paid_at               timestamptz,
  -- 到场 / 灯控关键时间点（全部留痕）
  first_gate_at         timestamptz,
  gate_waiting_at       timestamptz,  -- 到场但前一场延时，场边等待
  front_checkin_at      timestamptz,
  first_presence_at     timestamptz,  -- 任一渠道首次确认到场
  warmup_at             timestamptz,
  light_on_at           timestamptz,
  light_off_at          timestamptz,
  effective_start       timestamptz,  -- 实际开打（可能晚于 start_time）
  actual_end            timestamptz,
  overtime_minutes      int not null default 0,
  late_start_minutes    int not null default 0,
  comp_bonus_minutes    int not null default 0,
  overlimit_persons     int not null default 0,
  verdict               text check (verdict is null or verdict in
                          ('on_time','late','no_show','venue_fault','ambiguous','excused')),
  judged_at             timestamptz,
  judge_source          text,
  note                  text not null default '',
  created_source        text not null default 'member', -- member / operator
  version               bigint not null default 0,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  check (end_time > start_time)
);

-- 硬性防重：同一场地的活跃预约时间段不得重叠（终端状态不占位）
create index if not exists idx_bookings_court_time on bookings(court_id, start_time, end_time);
create index if not exists idx_bookings_member on bookings(member_id, created_at desc);
create index if not exists idx_bookings_status_start on bookings(status, start_time);

do $$
begin
  if not exists (select 1 from pg_constraint where conname = 'bookings_court_exclusive') then
    alter table bookings add constraint bookings_court_exclusive
      exclude using gist (
        court_id with =,
        tstzrange(start_time, end_time, '[)') with &&
      )
      where (status in ('held','paid','warming','in_use'));
  end if;
end$$;

-- ============ 拼场人员 ============
create table if not exists booking_participants (
  id            bigserial primary key,
  booking_id    bigint not null references bookings(id),
  member_id     bigint references members(id),
  name          text not null,
  phone         text,
  checked_in    boolean not null default false,
  checked_in_at timestamptz,
  created_at    timestamptz not null default now(),
  unique (booking_id, phone)
);

-- ============ 支付 / 退款 ============
create table if not exists payments (
  id              bigserial primary key,
  booking_id      bigint not null references bookings(id),
  kind            text not null check (kind in ('payment','refund','compensation')),
  amount_cents    bigint not null check (amount_cents >= 0),
  method          text not null default 'mock',
  status          text not null default 'succeeded',
  transaction_no  text not null,
  reason          text not null default '',
  created_source  text not null default 'member', -- member/front/operator/cs/system
  created_by      bigint,
  created_at      timestamptz not null default now()
);
create index if not exists idx_payments_booking on payments(booking_id, created_at);

create table if not exists refund_requests (
  id            bigserial primary key,
  booking_id    bigint not null references bookings(id),
  member_id     bigint not null references members(id),
  amount_cents  bigint not null,
  reason        text not null default '',
  status        text not null default 'pending' check (status in ('pending','approved','rejected')),
  reviewed_by   bigint,
  reviewed_at   timestamptz,
  review_note   text not null default '',
  created_at    timestamptz not null default now()
);
create index if not exists idx_refund_request_booking on refund_requests(booking_id);

-- ============ 设备 / 前台 / 事件留痕 ============
create table if not exists gate_records (
  id          bigserial primary key,
  booking_id  bigint references bookings(id),
  entry_code  text not null default '',
  device_id   text not null default '',
  result      text not null check (result in ('allowed','denied','waiting')),
  reason      text not null default '',
  scanned_at  timestamptz not null default now()
);
create index if not exists idx_gate_booking on gate_records(booking_id, scanned_at);

create table if not exists light_records (
  id            bigserial primary key,
  booking_id    bigint not null references bookings(id),
  court_id      bigint not null,
  action        text not null check (action in ('warmup','on','off')),
  command_source text not null check (command_source in ('scheduler','device','front','operator','system')),
  success       boolean not null default true,
  detail        text not null default '',
  occurred_at   timestamptz not null default now()
);
create index if not exists idx_light_booking on light_records(booking_id, occurred_at);

create table if not exists front_records (
  id          bigserial primary key,
  booking_id  bigint not null references bookings(id),
  staff_id    bigint,
  action      text not null check (action in (
                'checkin','checkout','grant_comp','waive_overlimit',
                'start_session','end_session','adjust','override')),
  detail      jsonb not null default '{}'::jsonb,
  created_at  timestamptz not null default now()
);
create index if not exists idx_front_booking on front_records(booking_id, created_at);

-- 统一事件溯源表：任何状态变化都有明确来源，客服不必只猜订单表
create table if not exists event_log (
  id          bigserial primary key,
  booking_id  bigint references bookings(id),
  venue_id    bigint,
  court_id    bigint,
  event_type  text not null,
  source      text not null check (source in
                ('member','gate','light','front','operator','cs','system','scheduler')),
  actor_id    bigint,
  payload     jsonb not null default '{}'::jsonb,
  created_at  timestamptz not null default now()
);
create index if not exists idx_event_booking on event_log(booking_id, created_at);
create index if not exists idx_event_venue_time on event_log(venue_id, created_at desc);

-- ============ 爽约 / 争议 ============
create table if not exists disputes (
  id                bigserial primary key,
  booking_id        bigint not null unique references bookings(id),
  opened_by         bigint,
  auto_opened       boolean not null default false,
  status            text not null default 'open' check (status in ('open','resolved')),
  issue             text not null default '',
  resolution_type   text check (resolution_type is null or resolution_type in
                      ('refund','partial_refund','compensation','rescind_noshow','reject','mixed')),
  refund_cents      bigint not null default 0,
  comp_minutes      int not null default 0,
  resolution_note   text not null default '',
  resolved_by       bigint,
  resolved_at       timestamptz,
  created_at        timestamptz not null default now()
);

-- 客服/前台赠送的补偿时长（可用于未来预约抵扣）
create table if not exists benefit_grants (
  id              bigserial primary key,
  member_id       bigint not null references members(id),
  venue_id        bigint not null references venues(id),
  minutes         int not null check (minutes > 0),
  reason          text not null default '',
  granted_source  text not null check (granted_source in ('front','cs','system')),
  granted_by      bigint,
  reserved_booking_id bigint references bookings(id), -- held 订单预占
  used_booking_id bigint references bookings(id),
  used_at         timestamptz,
  created_at      timestamptz not null default now()
);
create index if not exists idx_grants_member on benefit_grants(member_id) where used_at is null;

-- ============ 员工账号 ============
create table if not exists staff (
  id            bigserial primary key,
  username      text not null unique,
  password_hash text not null,
  name          text not null,
  role          text not null check (role in ('operator','front','cs')),
  venue_id      bigint references venues(id),
  created_at    timestamptz not null default now()
);

-- ============ 幂等键（客户端重试安全） ============
create table if not exists idempotency_keys (
  idempotency_key text primary key,
  booking_id      bigint references bookings(id),
  created_at      timestamptz not null default now()
);
