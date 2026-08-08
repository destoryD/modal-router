package models

import "time"

type Channel struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	Key             string            `json:"key"`
	Enabled         bool              `json:"enabled"`
	Weight          int               `json:"weight"`
	DisableOnStatus []int             `json:"disable_on_status"`
	AutoReenableSec int               `json:"auto_reenable_sec"`
	DisabledReason  string            `json:"disabled_reason,omitempty"`
	DisabledAt      *time.Time        `json:"disabled_at,omitempty"`
	AuthHeader      string            `json:"auth_header"`
	AuthPrefix      string            `json:"auth_prefix"`
	Headers         map[string]string `json:"headers,omitempty"`
	RequestCount    int64             `json:"request_count"`
	SuccessCount    int64             `json:"success_count"`
	FailCount       int64             `json:"fail_count"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type Stats struct {
	TotalChannels  int   `json:"total_channels"`
	ActiveChannels int   `json:"active_channels"`
	DisabledCount  int   `json:"disabled_count"`
	TotalRequests  int64 `json:"total_requests"`
	TotalSuccess   int64 `json:"total_success"`
	TotalFail      int64 `json:"total_fail"`
}
