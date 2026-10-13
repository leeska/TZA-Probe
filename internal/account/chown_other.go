//go:build !unix

package account

func OwnLikeDir(file, dir string) error { return nil }
