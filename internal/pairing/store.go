// Package pairing manages one-time local enrollment codes and durable controller
// grants. No pairing codes or raw bearer tokens are ever persisted.
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

const (
	CodeLifetime = 10 * time.Minute
	MaxAttempts  = 5
	fileName     = "pairing.json"
)

var (
	ErrNoCode      = errors.New("no local pairing window is open")
	ErrExpired     = errors.New("pairing code expired")
	ErrInvalidCode = errors.New("invalid pairing code")
	ErrRateLimited = errors.New("pairing attempts exhausted; generate a new code locally")
	ErrUnauthorized = errors.New("controller authorization failed")
	ErrInvalidIDs = errors.New("invalid controller or device ID")
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	codePattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type grant struct {
	ControllerID string    `json:"controller_id"`
	DeviceID     string    `json:"device_id"`
	TokenHash    string    `json:"token_hash"`
	PairedAt     time.Time `json:"paired_at"`
}
type record struct {
	Version   int       `json:"version"`
	AgentID   string    `json:"agent_id"`
	CodeHash  string    `json:"code_hash,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Attempts  int       `json:"attempts"`
	Grant     *grant    `json:"grant,omitempty"`
}
type Result struct {
	AgentID      string    `json:"agent_id"`
	ControllerID string    `json:"controller_id"`
	DeviceID     string    `json:"device_id"`
	AccessToken  string    `json:"access_token"`
	PairedAt     time.Time `json:"paired_at"`
}
type Store struct {
	dir     string
	agentID string
	now     func() time.Time
}

// New verifies a private, local state directory and rejects damaged state.
// It never regenerates an existing controller grant on read errors.
func New(dir, agentID string) (*Store, error) {
	if !uuidPattern.MatchString(agentID) { return nil, errors.New("invalid agent ID") }
	i, err := os.Lstat(dir)
	if err != nil { return nil, err }
	if !filepath.IsAbs(dir) || !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return nil, errors.New("pairing store requires a private absolute directory")
	}
	s := &Store{dir:dir, agentID:agentID, now:time.Now}
	err = s.withLock(func(*record)(bool,error){return false,nil})
	if err != nil {return nil,err}
	return s,nil
}

func hash(value string) string {
	sum:=sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func secureEqualHex(a,b string) bool {
	aa,errA:=hex.DecodeString(a)
	bb,errB:=hex.DecodeString(b)
	if errA!=nil||errB!=nil||len(aa)!=sha256.Size||len(bb)!=sha256.Size{return false}
	return subtle.ConstantTimeCompare(aa,bb)==1
}
func randomHex(size int) (string,error) {
	b:=make([]byte,size)
	if _,err:=rand.Read(b);err!=nil{return "",err}
	return hex.EncodeToString(b),nil
}
func validUUID(id string) bool {return uuidPattern.MatchString(id)}

// Generate creates a new 128-bit one-time code. Only a local privileged CLI
// is allowed to invoke this method. Opening a new window keeps any old grant
// alive until a successful, atomic re-pair.
func (s *Store) Generate() (string,time.Time,error) {
	code,err:=randomHex(16)
	if err!=nil{return "",time.Time{},err}
	var expiry time.Time
	err=s.withLock(func(r *record)(bool,error){
		expiry=s.now().UTC().Add(CodeLifetime)
		r.CodeHash=hash(code)
		r.ExpiresAt=expiry
		r.Attempts=0
		return true,nil
	})
	if err!=nil{return "",time.Time{},err}
	return code,expiry,nil
}

// Exchange consumes the code once, then returns a freshly minted bearer token.
// Failed attempts, including the fifth, are durably recorded before returning.
func (s *Store) Exchange(controllerID,deviceID,code string) (Result,error) {
	if !validUUID(controllerID)||!validUUID(deviceID) {
		return Result{},errors.New("invalid controller or device ID")
	}
	token,err:=randomHex(32)
	if err!=nil{return Result{},err}
	var result Result
	err=s.withLock(func(r *record)(bool,error){
		if r.CodeHash=="" {return false,ErrNoCode}
		if !s.now().Before(r.ExpiresAt) {return false,ErrExpired}
		if r.Attempts>=MaxAttempts {return false,ErrRateLimited}
		if !codePattern.MatchString(code)||!secureEqualHex(r.CodeHash,hash(code)){
			r.Attempts++
			if r.Attempts>=MaxAttempts{return true,ErrRateLimited}
			return true,ErrInvalidCode
		}
		pairedAt:=s.now().UTC()
		r.Grant=&grant{ControllerID:controllerID,DeviceID:deviceID,TokenHash:hash(token),PairedAt:pairedAt}
		r.CodeHash=""
		r.ExpiresAt=time.Time{}
		r.Attempts=0
		result=Result{AgentID:s.agentID,ControllerID:controllerID,DeviceID:deviceID,AccessToken:token,PairedAt:pairedAt}
		return true,nil
	})
	if err!=nil{return Result{},err}
	return result,nil
}

// Authorized is fail-closed when the store is missing/corrupt or unavailable.
// The public API never receives the raw token hash.
func (s *Store) Authorized(controllerID,agentID,token string) bool {
	if agentID!=s.agentID||!validUUID(controllerID)||len(token)!=64{return false}
	ok:=false
	if err:=s.withLock(func(r *record)(bool,error){
		if r.Grant!=nil {
			ok=r.Grant.ControllerID==controllerID&&secureEqualHex(r.Grant.TokenHash,hash(token))
		}
		return false,nil
	});err!=nil{return false}
	return ok
}

// Revoke discards the grant and any outstanding local code, keeping Agent UUID
// and TLS identity intact. Persistence must succeed before returning 204.
func (s *Store) Revoke(controllerID,agentID,token string) error {
	if agentID!=s.agentID||!validUUID(controllerID)||len(token)!=64{return ErrUnauthorized}
	return s.withLock(func(r *record)(bool,error){
		if r.Grant==nil||r.Grant.ControllerID!=controllerID||!secureEqualHex(r.Grant.TokenHash,hash(token)){
			return false,ErrUnauthorized
		}
		r.Grant=nil
		r.CodeHash=""
		r.ExpiresAt=time.Time{}
		r.Attempts=0
		return true,nil
	})
}
func (s *Store) withLock(action func(*record)(bool,error)) error {
	lockName:=filepath.Join(s.dir,".pairing.lock")
	if i,e:=os.Lstat(lockName);e==nil {
		if !i.Mode().IsRegular()||i.Mode().Perm()&0077!=0{return errors.New("insecure pairing lock file")}
	}else if !errors.Is(e,os.ErrNotExist){return e}
	f,err:=os.OpenFile(lockName,os.O_CREATE|os.O_RDWR,0600)
	if err!=nil{return err}
	defer f.Close()
	if err=syscall.Flock(int(f.Fd()),syscall.LOCK_EX);err!=nil{return err}
	defer syscall.Flock(int(f.Fd()),syscall.LOCK_UN)
	path:=filepath.Join(s.dir,fileName)
	r:=record{Version:1,AgentID:s.agentID}
	if i,e:=os.Lstat(path);e==nil {
		if !i.Mode().IsRegular()||i.Mode().Perm()&0077!=0{return errors.New("insecure pairing state file")}
		b,readErr:=os.ReadFile(path)
		if readErr!=nil{return readErr}
		if err=json.Unmarshal(b,&r);err!=nil{return fmt.Errorf("corrupt pairing state: %w",err)}
		if r.Version!=1||r.AgentID!=s.agentID||r.Attempts<0||r.Attempts>MaxAttempts{
			return errors.New("invalid or mismatched pairing state")
		}
		if r.Grant!=nil && (!validUUID(r.Grant.ControllerID)||!validUUID(r.Grant.DeviceID)||len(r.Grant.TokenHash)!=64) {
			return errors.New("invalid stored grant")
		}
	}else if !errors.Is(e,os.ErrNotExist){return e}
	dirty,actionErr:=action(&r)
	if dirty {
		if err:=save(s.dir,path,r);err!=nil{return fmt.Errorf("persist pairing state: %w",err)}
	}
	return actionErr
}
func save(dir,path string,r record) error {
	b,err:=json.MarshalIndent(r,"","  ")
	if err!=nil{return err}
	f,err:=os.CreateTemp(dir,".pairing-*.tmp")
	if err!=nil{return err}
	defer os.Remove(f.Name())
	if err:=f.Chmod(0600);err!=nil{f.Close();return err}
	if _,err:=f.Write(b);err!=nil{f.Close();return err}
	if err:=f.Sync();err!=nil{f.Close();return err}
	if err:=f.Close();err!=nil{return err}
	if err:=os.Rename(f.Name(),path);err!=nil{return err}
	if d,e:=os.Open(dir);e==nil{_ = d.Sync();_ = d.Close()}
	return nil
}
