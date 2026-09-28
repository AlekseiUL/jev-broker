package broker

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	DefaultAddr  = "127.0.0.1:8768"
	DefaultModel = "typesafe/jev-1.13"
	providerURL  = "https://openrouter.ai/api/alpha/decisions"
)

var profileIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type Config struct {
	APIKey  string
	Clients string
	Audit   string
	Addr    string
	Model   string
}

func ConfigFromEnv() (Config, error) {
	c := Config{
		APIKey:  os.Getenv("OPENROUTER_API_KEY"),
		Clients: os.Getenv("JEV_BROKER_CLIENTS_FILE"),
		Audit:   os.Getenv("JEV_BROKER_AUDIT_FILE"),
		Addr:    os.Getenv("JEV_BROKER_ADDR"),
		Model:   os.Getenv("JEV_BROKER_MODEL"),
	}
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.APIKey == "" || strings.ContainsAny(c.APIKey, "\r\n") || c.Clients == "" || c.Audit == "" || c.Model == "" {
		return Config{}, errors.New("missing or invalid broker configuration")
	}
	if err := ValidateLoopbackAddr(c.Addr); err != nil {
		return Config{}, err
	}
	return c, nil
}

func ValidateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return errors.New("JEV_BROKER_ADDR must be a loopback host:port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("JEV_BROKER_ADDR must use a numeric loopback address")
	}
	return nil
}

type Caller struct {
	ID     string
	Digest [sha256.Size]byte
}

type Registry struct {
	Callers []Caller
}

func (r Registry) Authenticate(header string) *Caller {
	if !strings.HasPrefix(header, "Bearer ") {
		return nil
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return nil
	}
	digest := sha256.Sum256([]byte(token))
	for i := range r.Callers {
		if subtle.ConstantTimeCompare(digest[:], r.Callers[i].Digest[:]) == 1 {
			return &r.Callers[i]
		}
	}
	return nil
}

func readPrivateFile(path string, maxBytes int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("private file path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || before.Size() > maxBytes {
		return nil, errors.New("private file must be a regular 0600 file of valid size")
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("private file must be owned by the broker user and have one link")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("private file cannot be opened")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("private file changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, errors.New("private file could not be read safely")
	}
	return data, nil
}

func LoadRegistry(path string) (Registry, error) {
	data, err := readPrivateFile(path, 64<<10)
	if err != nil {
		return Registry{}, err
	}
	var wire struct {
		Profiles []struct {
			ID          string `json:"id"`
			TokenSHA256 string `json:"token_sha256"`
		} `json:"profiles"`
	}
	if err := decodeStrict(data, &wire); err != nil || len(wire.Profiles) == 0 || len(wire.Profiles) > 64 {
		return Registry{}, errors.New("invalid broker clients file")
	}
	r := Registry{}
	ids := map[string]bool{}
	digests := map[[sha256.Size]byte]bool{}
	for _, p := range wire.Profiles {
		if !profileIDPattern.MatchString(p.ID) || ids[p.ID] || len(p.TokenSHA256) != 64 || p.TokenSHA256 != strings.ToLower(p.TokenSHA256) {
			return Registry{}, errors.New("invalid or duplicate broker profile")
		}
		bytes, err := hex.DecodeString(p.TokenSHA256)
		if err != nil || len(bytes) != sha256.Size {
			return Registry{}, errors.New("invalid broker token digest")
		}
		var digest [sha256.Size]byte
		copy(digest[:], bytes)
		if digests[digest] {
			return Registry{}, errors.New("duplicate broker token digest")
		}
		ids[p.ID], digests[digest] = true, true
		r.Callers = append(r.Callers, Caller{ID: p.ID, Digest: digest})
	}
	return r, nil
}

// decodeStrict rejects unknown fields, duplicate JSON object keys and trailing
// data. This is especially important for auth/config input and agent plans.
func decodeStrict(data []byte, target any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
