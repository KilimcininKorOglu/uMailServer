package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// RBAC: Role management -----------------------------------------------------------

func (d *DB) CreateRole(role *db.Role) error {
	if role.ID == "" {
		return fmt.Errorf("role id is required")
	}
	now := time.Now()
	if role.CreatedAt.IsZero() {
		role.CreatedAt = now
	}
	role.UpdatedAt = now
	_, err := d.pool.Exec(context.Background(), `
		INSERT INTO admin_roles (id, name, description, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO NOTHING`,
		role.ID, role.Name, role.Description, role.CreatedAt, role.UpdatedAt,
	)
	return err
}

func (d *DB) GetRole(id string) (*db.Role, error) {
	ctx := context.Background()
	row := d.pool.QueryRow(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM admin_roles WHERE id=$1`, id,
	)
	var role db.Role
	err := row.Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt, &role.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get role %q: %w", id, err)
	}
	return &role, nil
}

func (d *DB) ListRoles() ([]*db.Role, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM admin_roles ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list roles: %w", err)
	}
	defer rows.Close()

	var roles []*db.Role
	for rows.Next() {
		var role db.Role
		if err := rows.Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt, &role.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan role: %w", err)
		}
		roles = append(roles, &role)
	}
	return roles, rows.Err()
}

func (d *DB) UpdateRole(role *db.Role) error {
	role.UpdatedAt = time.Now()
	tag, err := d.pool.Exec(context.Background(), `
		UPDATE admin_roles SET name=$2, description=$3, updated_at=$4
		WHERE id=$1`,
		role.ID, role.Name, role.Description, role.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: update role %q: %w", role.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return db.ErrNotFound
	}
	return nil
}

func (d *DB) DeleteRole(id string) error {
	tag, err := d.pool.Exec(context.Background(), `DELETE FROM admin_roles WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete role %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return db.ErrNotFound
	}
	return nil
}

// RBAC: Permission management ---------------------------------------------------

func (d *DB) GetRolePermissions(roleID string) ([]*db.RolePermission, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `
		SELECT id, role_id, permission, params
		FROM admin_role_permission_relation WHERE role_id=$1`,
		roleID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: get role permissions %q: %w", roleID, err)
	}
	defer rows.Close()

	var perms []*db.RolePermission
	for rows.Next() {
		var p db.RolePermission
		var params []byte
		if err := rows.Scan(&p.ID, &p.RoleID, &p.Permission, &params); err != nil {
			return nil, fmt.Errorf("postgres: scan permission: %w", err)
		}
		if len(params) > 0 {
			p.Params = json.RawMessage(params)
		}
		perms = append(perms, &p)
	}
	return perms, rows.Err()
}

func (d *DB) SetRolePermissions(roleID string, perms []*db.RolePermission) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin set role permissions: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Verify role exists
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_roles WHERE id=$1)`, roleID).Scan(&exists); err != nil {
		return fmt.Errorf("postgres: check role exists: %w", err)
	}
	if !exists {
		return db.ErrNotFound
	}

	// Delete existing permissions
	if _, err := tx.Exec(ctx, `DELETE FROM admin_role_permission_relation WHERE role_id=$1`, roleID); err != nil {
		return fmt.Errorf("postgres: delete old permissions: %w", err)
	}

	// Insert new permissions
	for _, p := range perms {
		p.RoleID = roleID
		params := []byte("{}")
		if len(p.Params) > 0 {
			params = p.Params
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_role_permission_relation (id, role_id, permission, params)
			VALUES ($1,$2,$3,$4)`,
			p.ID, roleID, p.Permission, params,
		); err != nil {
			return fmt.Errorf("postgres: insert permission: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// RBAC: User-role assignment ---------------------------------------------------

func (d *DB) AssignRoleToUser(userID, roleID string) error {
	ctx := context.Background()

	// Verify role exists
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_roles WHERE id=$1)`, roleID).Scan(&exists); err != nil {
		return fmt.Errorf("postgres: check role exists: %w", err)
	}
	if !exists {
		return db.ErrNotFound
	}

	rel := db.AdminRoleRelation{UserID: userID, RoleID: roleID}
	_, err := d.pool.Exec(ctx, `
		INSERT INTO admin_user_role_relation (id, user_id, role_id)
		VALUES ($1,$2,$3)
		ON CONFLICT (user_id, role_id) DO NOTHING`,
		rel.ID, rel.UserID, rel.RoleID,
	)
	return err
}

func (d *DB) RemoveRoleFromUser(userID, roleID string) error {
	ctx := context.Background()
	tag, err := d.pool.Exec(ctx, `
		DELETE FROM admin_user_role_relation WHERE user_id=$1 AND role_id=$2`,
		userID, roleID,
	)
	if err != nil {
		return fmt.Errorf("postgres: remove role from user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return db.ErrNotFound
	}
	return nil
}

func (d *DB) GetUserRoles(userID string) ([]*db.Role, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `
		SELECT r.id, r.name, r.description, r.created_at, r.updated_at
		FROM admin_roles r
		JOIN admin_user_role_relation ur ON ur.role_id = r.id
		WHERE ur.user_id=$1
		ORDER BY r.name`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: get user roles %q: %w", userID, err)
	}
	defer rows.Close()

	var roles []*db.Role
	for rows.Next() {
		var role db.Role
		if err := rows.Scan(&role.ID, &role.Name, &role.Description, &role.CreatedAt, &role.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan user role: %w", err)
		}
		roles = append(roles, &role)
	}
	return roles, rows.Err()
}

func (d *DB) GetUsersByRole(roleID string) ([]string, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `
		SELECT user_id FROM admin_user_role_relation WHERE role_id=$1`,
		roleID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: get users by role %q: %w", roleID, err)
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("postgres: scan user id: %w", err)
		}
		users = append(users, userID)
	}
	return users, rows.Err()
}
