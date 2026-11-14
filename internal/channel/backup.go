package channel

// The backup capability: the Server sends an encrypted backup and the Agent
// stores it in a directory, keeping the newest Keep of them.
const (
	CapabilityBackup = "backup"
	// KindBackupWrite is followed by exactly Size raw bytes; answers
	// BackupWriteResult.
	KindBackupWrite = "backup.write"
	// MaxBackupKeep bounds how many backups a directory keeps.
	MaxBackupKeep = 100
)

type BackupWriteParams struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Keep   int    `json:"keep"`
}

// BackupFile is a backup kept in the directory.
type BackupFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type BackupWriteResult struct {
	Error string       `json:"error,omitempty"`
	Kept  []BackupFile `json:"kept,omitempty"`
}
