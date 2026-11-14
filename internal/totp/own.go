package totp

import "github.com/leeska/TZA-ControlCenter/internal/account"

// ownLikeDir hands the file to the data directory's owner when run as root
// (the terminal reset), like the account file.
func ownLikeDir(file, dir string) error { return account.OwnLikeDir(file, dir) }
