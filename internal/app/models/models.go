package models

import (
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis"
	"github.com/jwma/jump-jump/internal/app/db"
	"github.com/jwma/jump-jump/internal/app/utils"
	"log"
	"time"
)

const RoleUser = 1
const RoleAdmin = 2

type User struct {
	Username    string    `json:"username"`
	Role        int       `json:"role"`
	RawPassword string    `json:"-"`
	Password    []byte    `json:"password"`
	Salt        []byte    `json:"salt"`
	CreateTime  time.Time `json:"create_time"`
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsExists() bool {
	if u.Username == "" {
		return false
	}

	client := db.GetRedisClient()
	exists, err := client.HExists("users", u.Username).Result()
	if err != nil {
		log.Printf("fail to check user exists with username: %s, error: %v\n", u.Username, err)
		return false
	}
	return exists
}

func (u *User) Save() error {
	if u.Username == "" || u.RawPassword == "" {
		return fmt.Errorf("username or password can not be empty string")
	}
	if u.IsExists() {
		return fmt.Errorf("%s already exitis", u.Username)
	}
	salt, _ := utils.RandomSalt(32)
	dk, _ := utils.EncodePassword([]byte(u.RawPassword), salt)
	u.Password = dk
	u.Salt = salt
	u.CreateTime = time.Now()

	client := db.GetRedisClient()
	j, _ := json.Marshal(u)
	client.HSet("users", u.Username, j)
	return nil
}

func (u *User) Get() error {
	if u.Username == "" {
		return fmt.Errorf("username can not be empty string")
	}

	client := db.GetRedisClient()
	j, err := client.HGet("users", u.Username).Result()
	if err != nil {
		log.Printf("fail to get user with username: %s, error: %v\n", u.Username, err)
		return fmt.Errorf("用户不存在")
	}
	err = json.Unmarshal([]byte(j), u)
	if err != nil {
		log.Printf("fail to Unmarshal user with username: %s, error: %v\n", u.Username, err)
		return fmt.Errorf("用户不存在")
	}
	return nil
}

type ShortLink struct {
	Id          string    `json:"id"`
	Url         string    `json:"url"`
	Description string    `json:"description"`
	IsEnable    bool      `json:"is_enable"`
	CreatedBy   string    `json:"created_by"`
	CreateTime  time.Time `json:"create_time"`
	UpdateTime  time.Time `json:"update_time"`
}

type UpdateShortLinkParameter struct {
	Url         string `json:"url" binding:"required"`
	Description string `json:"description"`
	IsEnable    bool   `json:"is_enable"`
}

func (s *ShortLink) key() string {
	return fmt.Sprintf("link:%s", s.Id)
}

func (s *ShortLink) GenerateId() error {
	client := db.GetRedisClient()

	for true {
		s.Id = utils.RandStringRunes(6)
		_, err := client.Get(s.key()).Result()
		if err == redis.Nil {
			return nil
		}
		if err != nil {
			log.Println(err)
			return err
		}
	}

	return nil
}

func (s *ShortLink) Save() error {
	if s.Id == "" {
		return fmt.Errorf("id错误")
	}
	if s.Url == "" {
		return fmt.Errorf("请填写url")
	}
	if s.CreatedBy == "" {
		return fmt.Errorf("未设置创建者，请通过接口创建短链接")
	}

	s.CreateTime = time.Now()
	s.UpdateTime = time.Now()
	c := db.GetRedisClient()
	j, _ := json.Marshal(s)
	// 保存短链接
	c.Set(s.key(), j, 0)
	// 保存用户的短链接记录，保存到创建者及全局
	record := redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: s.Id,
	}
	c.ZAdd(fmt.Sprintf("links:%s", s.CreatedBy), record)
	c.ZAdd("links", record)

	return nil
}

func (s *ShortLink) Get() error {
	if s.Id == "" {
		return fmt.Errorf("短链接不存在")
	}

	c := db.GetRedisClient()
	rs, err := c.Get(s.key()).Result()
	if err != nil {
		log.Printf("fail to get short link with key: %s, error: %v\n", s.key(), err)
		return fmt.Errorf("短链接不存在")
	}

	err = json.Unmarshal([]byte(rs), s)
	if err != nil {
		log.Printf("fail to unmarshal short link, key: %s, error: %v\n", s.key(), err)
		return fmt.Errorf("短链接不存在")
	}

	return nil
}

func (s *ShortLink) Update(u *UpdateShortLinkParameter) error {
	s.Url = u.Url
	s.Description = u.Description
	s.IsEnable = u.IsEnable
	s.UpdateTime = time.Now()

	return s.Save()
}

func (s *ShortLink) Delete() {
	c := db.GetRedisClient()

	// 删除短链接本身
	c.Del(s.key())
	// 删除用户的短链接记录及全局短链接记录
	c.ZRem(fmt.Sprintf("links:%s", s.CreatedBy), s.Id)
	c.ZRem("links", s.Id)

	// 删除访问历史
	keys, _ := c.Keys(fmt.Sprintf("history:%s:*", s.Id)).Result()
	if len(keys) > 0 {
		c.Del(keys...)
	}
}

type RequestHistory struct {
	link *ShortLink `json:"-"`
	Url  string     `json:"url"` // 由于短链接的目标连接可能会被修改，可以在访问历史记录中记录一下当前的目标连接
	IP   string     `json:"ip"`
	UA   string     `json:"ua"`
	Time time.Time  `json:"time"`
}

func NewRequestHistory(link *ShortLink, IP string, UA string) *RequestHistory {
	return &RequestHistory{link: link, IP: IP, UA: UA, Url: link.Url}
}

func (r *RequestHistory) SetLink(link *ShortLink) {
	r.link = link
}

func (r *RequestHistory) key() string {
	return fmt.Sprintf("history:%s:%s", r.link.Id, time.Now().Format("20060102"))
}

func (r *RequestHistory) Save() {
	r.Time = time.Now()
	c := db.GetRedisClient()
	j, err := json.Marshal(r)
	if err != nil {
		log.Printf("fail to save short link request history with key: %s, error: %v\n", r.key(), err)
		return
	}

	c.LPush(r.key(), j)
}

func (r *RequestHistory) GetAll() ([]*RequestHistory, error) {
	histories := make([]*RequestHistory, 0)
	c := db.GetRedisClient()
	all, err := c.LRange(r.key(), 0, -1).Result()
	if err != nil {
		log.Printf("fail to get all request history with key: %s, error: %v\n", r.key(), err)
		return histories, err
	}

	for _, one := range all {
		h := &RequestHistory{}
		_ = json.Unmarshal([]byte(one), h)
		histories = append(histories, h)
	}
	return histories, err
}
