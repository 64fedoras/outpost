package main

import "outpost/internal/store"

// Safe saves and the backup format live in internal/store. These names keep
// the call sites in package main unchanged.
var (
	writeFileAtomic       = store.WriteFileAtomic
	writeFileAtomicBackup = store.WriteFileAtomicBackup
	readWithBackup        = store.ReadWithBackup
	errBothDamaged        = store.ErrBothDamaged
	writeBackup           = store.WriteBackup
	restoreBackup         = store.RestoreBackup
	verifyBackup          = store.VerifyBackup
	errBadBackup          = store.ErrBadBackup
)

const backupChunk = store.BackupChunk
