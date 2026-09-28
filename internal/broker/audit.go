package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const AuditHeader = "jev-broker-audit-v1\n"

type Audit struct{ path string }

type auditRecord struct {
	At        string         `json:"at"`
	ProfileID string         `json:"profile_id"`
	Event     string         `json:"event"`
	Modes     map[string]int `json:"modes"`
	CostUSD   *float64       `json:"cost_usd,omitempty"`
}

func OpenAudit(path string) (*Audit, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("JEV_BROKER_AUDIT_FILE must be an absolute path")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		if _, err = f.WriteString(AuditHeader); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, errors.New("audit file initialization failed")
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, errors.New("audit file cannot be created")
	}
	a := &Audit{path: path}
	if err := a.check(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Audit) check() error {
	if a == nil {
		return errors.New("audit unavailable")
	}
	info, err := os.Lstat(a.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < int64(len(AuditHeader)) {
		return errors.New("audit file is unsafe or malformed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errors.New("audit file is unsafe or malformed")
	}
	f, err := os.OpenFile(a.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("audit file is unsafe or malformed")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("audit file changed")
	}
	header := make([]byte, len(AuditHeader))
	if _, err := io.ReadFull(f, header); err != nil || !bytes.Equal(header, []byte(AuditHeader)) {
		return errors.New("audit file is unsafe or malformed")
	}
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return errors.New("audit file is unsafe or malformed")
	}
	last := make([]byte, 1)
	if _, err := io.ReadFull(f, last); err != nil || last[0] != '\n' {
		return errors.New("audit file is unsafe or malformed")
	}
	return nil
}

// Append fsyncs an attempt before the external POST. It stores only metadata:
// no submitted state, item IDs, question text, result text or provider errors.
func (a *Audit) Append(profileID, event string, modes map[string]int, cost *float64) error {
	if a == nil || !profileIDPattern.MatchString(profileID) || (event != "attempt" && event != "ok" && event != "failed") {
		return errors.New("audit metadata invalid")
	}
	if event == "attempt" {
		cost = nil
	}
	before, err := os.Lstat(a.path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 {
		return errors.New("audit file unavailable")
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errors.New("audit file unavailable")
	}
	f, err := os.OpenFile(a.path, os.O_RDWR|os.O_APPEND|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("audit file unavailable")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return errors.New("audit file changed")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return errors.New("audit lock unavailable")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	current, err := os.Lstat(a.path)
	if err != nil || !os.SameFile(current, after) {
		return errors.New("audit file changed")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.New("audit read failed")
	}
	header := make([]byte, len(AuditHeader))
	if _, err := io.ReadFull(f, header); err != nil || string(header) != AuditHeader {
		return errors.New("audit header invalid")
	}
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return errors.New("audit tail invalid")
	}
	last := make([]byte, 1)
	if _, err := io.ReadFull(f, last); err != nil || last[0] != '\n' {
		return errors.New("audit tail invalid")
	}
	record, err := json.Marshal(auditRecord{At: time.Now().UTC().Format(time.RFC3339Nano), ProfileID: profileID, Event: event, Modes: modes, CostUSD: cost})
	if err != nil {
		return errors.New("audit encoding failed")
	}
	record = append(record, '\n')
	n, err := f.Write(record)
	if err != nil || n != len(record) {
		return errors.New("audit write failed")
	}
	if err := f.Sync(); err != nil {
		return errors.New("audit sync failed")
	}
	return nil
}
