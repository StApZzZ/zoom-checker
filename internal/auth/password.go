package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP 2024+ minimum: m=19 MiB, t=2, p=1).
const (
	argonMemory  = 19456 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16

	// Upper bounds accepted when verifying stored hashes, so a tampered or
	// foreign hash cannot make verification allocate gigabytes.
	maxArgonMemory  = 256 * 1024 // KiB
	maxArgonTime    = 16
	maxArgonThreads = 16

	// MinPasswordLength / MaxPasswordLength bound new local passwords (runes).
	MinPasswordLength = 8
	MaxPasswordLength = 256
)

var b64 = base64.RawStdEncoding // PHC strings use unpadded standard base64

// Password policy errors; their text is shown to users as is.
var (
	ErrPasswordTooShort = fmt.Errorf("Пароль должен быть не короче %d символов.", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("Пароль слишком длинный: не больше %d символов.", MaxPasswordLength)
	ErrPasswordRepeated = errors.New("Пароль не должен состоять из одного повторяющегося символа.")
)

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

// ErrBusy is returned by HashPasswordContext / VerifyPasswordContext when no
// hashing slot became free in time (or the caller gave up waiting). Its text is
// Russian and can be shown to users.
var ErrBusy = errors.New("Сервер сейчас занят. Попробуйте войти ещё раз через несколько секунд.")

// argonQueueWait is how long a request-bound hash waits for a free slot before
// ErrBusy: a burst of logins queues briefly instead of failing at once.
const argonQueueWait = 2 * time.Second

// argonPool bounds how many argon2id computations run at once. Every one
// allocates its whole memory block (19 MiB) up front, so without a bound a
// burst of unauthenticated logins (unknown usernames hash too, for equal
// timing) could make the process allocate gigabytes and get OOM-killed. With
// p=1 a hash uses one core, so more slots than cores adds no throughput.
type argonPool struct {
	slots chan struct{}
	wait  atomic.Int64 // time.Duration a bounded caller waits for a slot
	// key computes the hash; replaced in tests to observe concurrency.
	key func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte
}

func newArgonPool(size int, wait time.Duration) *argonPool {
	p := &argonPool{slots: make(chan struct{}, max(size, 1)), key: argon2.IDKey}
	p.wait.Store(int64(wait))
	return p
}

// argonSlotCount is 2..4 by core count: 4 slots keep the peak at ~76 MiB,
// which still fits a 256 MB server.
func argonSlotCount() int { return max(2, min(runtime.GOMAXPROCS(0), 4)) }

// passwordPool is shared by every caller (login, password change, admin
// create/reset, bootstrap, CLI), so the bound holds by construction.
var passwordPool = newArgonPool(argonSlotCount(), argonQueueWait)

// idKey takes a slot, computes the key and frees the slot. bounded callers
// (HTTP requests) give up after p.wait with ErrBusy; unbounded ones (CLI,
// bootstrap) wait until ctx ends. Nothing is allocated before a slot is held.
func (p *argonPool) idKey(ctx context.Context, bounded bool, pw, salt []byte, t, m uint32, th uint8, keyLen uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w (%w)", ErrBusy, err)
	}
	select {
	case p.slots <- struct{}{}:
	default:
		var timeout <-chan time.Time
		if wait := time.Duration(p.wait.Load()); bounded && wait > 0 {
			tm := time.NewTimer(wait)
			defer tm.Stop()
			timeout = tm.C
		}
		select {
		case p.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (%w)", ErrBusy, ctx.Err())
		case <-timeout:
			return nil, ErrBusy
		}
	}
	defer func() { <-p.slots }()
	return p.key(pw, salt, t, m, th, keyLen), nil
}

// HoldPasswordSlots takes every password hashing slot until release is
// called, and makes request-bound callers give up after wait instead of the
// usual couple of seconds. It exists so the handling of ErrBusy can be tested
// end to end; production code never calls it.
func HoldPasswordSlots(wait time.Duration) (release func()) {
	p := passwordPool
	prev := p.wait.Swap(int64(wait))
	for range cap(p.slots) {
		p.slots <- struct{}{}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for range cap(p.slots) {
				<-p.slots
			}
			p.wait.Store(prev)
		})
	}
}

// HashPassword hashes pw with argon2id and returns a PHC string:
// "$argon2id$v=19$m=19456,t=2,p=1$<salt b64>$<hash b64>".
// It waits as long as needed for a hashing slot (CLI, bootstrap); request
// handlers use HashPasswordContext. The error is always nil.
func HashPassword(pw string) (string, error) {
	return passwordPool.hash(context.Background(), false, pw)
}

// HashPasswordContext is HashPassword for request handlers: it waits at most
// a couple of seconds for a free hashing slot, then returns ErrBusy (also
// when ctx ends first, e.g. the client went away).
func HashPasswordContext(ctx context.Context, pw string) (string, error) {
	return passwordPool.hash(ctx, true, pw)
}

func (p *argonPool) hash(ctx context.Context, bounded bool, pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	_, _ = rand.Read(salt) // never fails since Go 1.24
	key, err := p.idKey(ctx, bounded, []byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the PHC argon2id hash.
// A malformed or unsupported hash returns an error (and false).
// The comparison is constant-time. Like HashPassword it waits as long as
// needed for a hashing slot; request handlers use VerifyPasswordContext.
func VerifyPassword(hash, pw string) (bool, error) {
	return passwordPool.verify(context.Background(), false, hash, pw)
}

// VerifyPasswordContext is VerifyPassword for request handlers: ErrBusy when
// no hashing slot became free within a couple of seconds or ctx ended.
func VerifyPasswordContext(ctx context.Context, hash, pw string) (bool, error) {
	return passwordPool.verify(ctx, true, hash, pw)
}

func (p *argonPool) verify(ctx context.Context, bounded bool, hash, pw string) (bool, error) {
	ph, err := parseArgonHash(hash)
	if err != nil {
		return false, err
	}
	key, err := p.idKey(ctx, bounded, []byte(pw), ph.salt, ph.time, ph.memory, ph.threads, uint32(len(ph.key)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, ph.key) == 1, nil
}

// NeedsRehash reports whether hash was produced with parameters other than
// the current ones (or cannot be parsed) and should be replaced after the
// next successful login.
func NeedsRehash(hash string) bool {
	p, err := parseArgonHash(hash)
	if err != nil {
		return true
	}
	return p.memory != argonMemory || p.time != argonTime || p.threads != argonThreads ||
		len(p.key) != argonKeyLen || len(p.salt) != argonSaltLen
}

func parseArgonHash(hash string) (argonParams, error) {
	var p argonParams
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, errors.New("auth: not an argon2id PHC hash")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, fmt.Errorf("auth: unsupported argon2 version %q", parts[2])
	}
	seen := map[string]bool{}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil || seen[k] {
			return p, fmt.Errorf("auth: bad argon2 parameter %q", kv)
		}
		seen[k] = true
		switch k {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.time = uint32(n)
		case "p":
			if n > 255 {
				return p, fmt.Errorf("auth: bad argon2 parameter %q", kv)
			}
			p.threads = uint8(n)
		default:
			return p, fmt.Errorf("auth: unknown argon2 parameter %q", kv)
		}
	}
	if p.time < 1 || p.time > maxArgonTime || p.threads < 1 || p.threads > maxArgonThreads ||
		p.memory < 8*uint32(p.threads) || p.memory > maxArgonMemory {
		return p, errors.New("auth: argon2 parameters out of range")
	}
	var err error
	if p.salt, err = b64.DecodeString(parts[4]); err != nil || len(p.salt) < 8 || len(p.salt) > 64 {
		return p, errors.New("auth: bad argon2 salt")
	}
	if p.key, err = b64.DecodeString(parts[5]); err != nil || len(p.key) < 16 || len(p.key) > 64 {
		return p, errors.New("auth: bad argon2 hash value")
	}
	return p, nil
}

// passwordAlphabet has no look-alike characters (0/O/o, 1/l/I).
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a random 16-character password from an alphabet
// without ambiguous characters (~93 bits of entropy), using crypto/rand.
func GeneratePassword() string {
	const n = 16
	// Rejection sampling keeps the distribution uniform: 256 is not a multiple
	// of len(alphabet), so bytes >= limit are discarded.
	limit := byte(256 - 256%len(passwordAlphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, 32)
	for len(out) < n {
		_, _ = rand.Read(buf)
		for _, b := range buf {
			if b < limit && len(out) < n {
				out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			}
		}
	}
	return string(out)
}

// ValidateNewPassword checks the local password policy: at least
// MinPasswordLength characters, at most MaxPasswordLength, not one repeated
// character. The returned error text is Russian and can be shown to users.
func ValidateNewPassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if n > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	first, _ := utf8.DecodeRuneInString(pw)
	if strings.Count(pw, string(first)) == n {
		return ErrPasswordRepeated
	}
	return nil
}
