package provider

import (
	"context"
	"time"
)

// Quota represents a single quota bucket (e.g. 5-hour, weekly).
type Quota struct {
	Label       string    `json:"label"`        // e.g. "5-hour", "weekly"
	PercentLeft float64   `json:"percent_left"` // 0.0 to 100.0
	ResetAt     time.Time `json:"reset_at"`
	ResetIn     string    `json:"reset_in"`     // humanized, e.g. "2h 6m", "1d 11h"
	Description string    `json:"description,omitempty"`
}

// AccountStatus represents the status and quota health of an account.
type AccountStatus struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Plan        string    `json:"plan"` // Free / Pro / Ultra / etc.
	Quotas      []Quota   `json:"quotas"`
	Healthy     bool      `json:"healthy"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

// StoredAccount represents account metadata in storage.
type StoredAccount struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Provider interface for extensible multi-account switching & quota monitoring.
type Provider interface {
	ID() string
	DisplayName() string
	CLIBinary() string
	ListAccounts() ([]StoredAccount, error)
	FetchStatus(ctx context.Context, account StoredAccount) (AccountStatus, error)
	SwitchTo(account StoredAccount) error
	ImportCurrent(id, email string) (*StoredAccount, error)
	AddNew(id, email string) (*StoredAccount, error)
	RemoveAccount(id string) error
	RenameAccount(oldID, newID string) error
	ActiveAccount() (*StoredAccount, error)
	IsCLIRunning() (bool, error)
	LaunchCLI(account StoredAccount) error
	InvalidateCache()
}
