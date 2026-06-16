package db

import (
	"encoding/json"
	"time"
)

// Role represents an admin role — a named bundle of permissions that can be
// assigned to one or more user accounts.
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RolePermission is a single permission granted to a role. The Params field
// carries permission-specific configuration (currently unused for all built-in
// permissions, but preserved for future use).
type RolePermission struct {
	ID       string `json:"id"`
	RoleID   string `json:"role_id"`
	Permission string `json:"permission"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// AdminRoleRelation links a user account to a role. The primary key is the
// combination of UserID and RoleID (enforced at the storage layer).
type AdminRoleRelation struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"` // account email (the account's canonical ID)
	RoleID string `json:"role_id"`
}
