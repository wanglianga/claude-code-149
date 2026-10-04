package service

import (
	"context"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"

	"courtbook/internal/store"
)

// StaffSession 员工登录会话
type StaffSession struct {
	Token string      `json:"token"`
	Staff store.Staff `json:"staff"`
}

func (s *Service) StaffLogin(ctx context.Context, username, password string) (*StaffSession, error) {
	var id int64
	var hash, name, role string
	var venueID *int64
	err := s.Store.Pool.QueryRow(ctx,
		`SELECT id,password_hash,name,role,venue_id FROM staff_users WHERE username=$1`, username).
		Scan(&id, &hash, &name, &role, &venueID)
	if err != nil {
		return nil, errors.New("用户名或密码错误")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errors.New("用户名或密码错误")
	}
	token := genToken("ST", 24)
	if _, err := s.Store.Pool.Exec(ctx,
		`INSERT INTO staff_tokens(token,staff_id,expires_at) VALUES ($1,$2,$3)`,
		token, id, s.Now().Add(12*time.Hour)); err != nil {
		return nil, err
	}
	return &StaffSession{Token: token, Staff: store.Staff{
		ID: id, Username: username, Name: name, Role: role, VenueID: venueID,
	}}, nil
}

func (s *Service) AuthenticateStaff(ctx context.Context, token string) (*store.Staff, error) {
	var id int64
	var username, name, role string
	var venueID *int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id,u.username,u.name,u.role,u.venue_id
		FROM staff_tokens t JOIN staff_users u ON u.id=t.staff_id
		WHERE t.token=$1 AND t.expires_at>now()`, token).
		Scan(&id, &username, &name, &role, &venueID)
	if err != nil {
		return nil, errors.New("未登录或登录已过期")
	}
	return &store.Staff{ID: id, Username: username, Name: name, Role: role, VenueID: venueID}, nil
}

// MemberSession 会员会话。本系统为演示：手机号 + 短信验证码（固定 123456，或任意码当 dev 模式）。
type MemberSession struct {
	Token  string       `json:"token"`
	Member store.Member `json:"member"`
}

func (s *Service) MemberLogin(ctx context.Context, phone, name, smsCode string) (*MemberSession, error) {
	if smsCode != "123456" {
		return nil, errors.New("短信验证码错误（演示环境固定为 123456）")
	}
	var m store.Member
	err := s.Store.Pool.QueryRow(ctx,
		`SELECT id,phone,name,member_tier,no_show_count,banned FROM members WHERE phone=$1`, phone).
		Scan(&m.ID, &m.Phone, &m.Name, &m.MemberTier, &m.NoShowCount, &m.Banned)
	if err != nil {
		if name == "" {
			name = "球友" + phone[len(phone)-4:]
		}
		if err := s.Store.Pool.QueryRow(ctx,
			`INSERT INTO members(phone,name) VALUES ($1,$2) RETURNING id,phone,name,member_tier,no_show_count,banned`,
			phone, name).Scan(&m.ID, &m.Phone, &m.Name, &m.MemberTier, &m.NoShowCount, &m.Banned); err != nil {
			return nil, err
		}
	}
	token := genToken("MB", 24)
	if _, err := s.Store.Pool.Exec(ctx,
		`INSERT INTO member_tokens(token,member_id,expires_at) VALUES ($1,$2,$3)`,
		token, m.ID, s.Now().Add(30*24*time.Hour)); err != nil {
		return nil, err
	}
	return &MemberSession{Token: token, Member: m}, nil
}

func (s *Service) AuthenticateMember(ctx context.Context, token string) (*store.Member, error) {
	var m store.Member
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT m.id,m.phone,m.name,m.member_tier,m.no_show_count,m.banned
		FROM member_tokens t JOIN members m ON m.id=t.member_id
		WHERE t.token=$1 AND t.expires_at>now()`, token).
		Scan(&m.ID, &m.Phone, &m.Name, &m.MemberTier, &m.NoShowCount, &m.Banned)
	if err != nil {
		return nil, errors.New("会员未登录或登录已过期")
	}
	return &m, nil
}

// SetMemberTier 运营调整会员等级（会员优先预约权益）
func (s *Service) SetMemberTier(ctx context.Context, memberID int64, tier string) error {
	if tier != "normal" && tier != "silver" && tier != "gold" {
		return errors.New("非法会员等级")
	}
	_, err := s.Store.Pool.Exec(ctx, `UPDATE members SET member_tier=$2 WHERE id=$1`, memberID, tier)
	return err
}

// UnbanMember 解除封禁
func (s *Service) UnbanMember(ctx context.Context, memberID int64) error {
	_, err := s.Store.Pool.Exec(ctx,
		`UPDATE members SET banned=false,no_show_count=0 WHERE id=$1`, memberID)
	return err
}

// HashPassword 供种子数据使用
func HashPassword(pw string) string {
	b, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b)
}
