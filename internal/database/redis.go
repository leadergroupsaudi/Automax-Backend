package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

func ConnectRedis(cfg *config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	RedisClient = client
	log.Println("Redis connected successfully")
	return client, nil
}

func CloseRedis(client *redis.Client) error {
	return client.Close()
}

type SessionStore struct {
	client *redis.Client
}

func NewSessionStore(client *redis.Client) *SessionStore {
	return &SessionStore{client: client}
}

func (s *SessionStore) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key, data, expiration).Err()
}

func (s *SessionStore) Get(ctx context.Context, key string, dest interface{}) error {
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func (s *SessionStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

func (s *SessionStore) Exists(ctx context.Context, key string) (bool, error) {
	result, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return result > 0, nil
}

func (s *SessionStore) SetUserSession(ctx context.Context, userID string, sessionData interface{}, expiration time.Duration) error {
	key := fmt.Sprintf("session:%s", userID)
	return s.Set(ctx, key, sessionData, expiration)
}

func (s *SessionStore) GetUserSession(ctx context.Context, userID string, dest interface{}) error {
	key := fmt.Sprintf("session:%s", userID)
	return s.Get(ctx, key, dest)
}

func (s *SessionStore) DeleteUserSession(ctx context.Context, userID string) error {
	key := fmt.Sprintf("session:%s", userID)
	return s.Delete(ctx, key)
}

func (s *SessionStore) BlacklistToken(ctx context.Context, token string, expiration time.Duration) error {
	key := fmt.Sprintf("blacklist:%s", token)
	return s.client.Set(ctx, key, "1", expiration).Err()
}

func (s *SessionStore) IsTokenBlacklisted(ctx context.Context, token string) (bool, error) {
	key := fmt.Sprintf("blacklist:%s", token)
	return s.Exists(ctx, key)
}
