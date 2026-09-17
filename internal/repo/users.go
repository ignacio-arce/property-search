package repo

import (
	"context"
	"fmt"
)

// User is a delivery target.
type User struct {
	UserID int64
	ChatID int64
}

// ListActiveUsers returns the users the daily digest may run for.
//
// The activation switch is the operator's control: anyone may onboard, but nobody
// receives notifications until the operator enables them
// (UPDATE users SET active = true WHERE chat_id = <id>). Being stopped by the user
// (/stop) is a separate switch and also halts delivery.
func (r *Repo) ListActiveUsers(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT user_id, chat_id
		   FROM users
		  WHERE active = true AND state <> 'stopped'
		  ORDER BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("repo: list active users: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.UserID, &u.ChatID); err != nil {
			return nil, fmt.Errorf("repo: scan user: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo: list active users: %w", err)
	}
	return out, nil
}
