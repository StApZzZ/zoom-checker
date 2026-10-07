//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// fixOwnership is for administrators who run CLI commands as root against the
// service's SQLite database: files SQLite created (the database itself, -wal,
// -shm) would be owned by root and the service could no longer write them.
// They are given to the owner of the directory that holds the database.
func fixOwnership(dbPath string) {
	if dbPath == "" || os.Geteuid() != 0 {
		return
	}
	dirInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		return
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid == 0 {
		return
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"} {
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == 0 && fi.Mode().IsRegular() {
			_ = os.Lchown(p, int(dirStat.Uid), int(dirStat.Gid))
		}
	}
}
