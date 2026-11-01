package bundles

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipPayload(t *testing.T, names []string, symlink bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if symlink {
			h.SetMode(os.ModeSymlink | 0777)
		} else {
			h.SetMode(0644)
		}
		f, err := w.CreateHeader(h)
		if err != nil { t.Fatal(err) }
		if _, err := f.Write([]byte("safe application resource")); err != nil { t.Fatal(err) }
	}
	if err := w.Close(); err != nil { t.Fatal(err) }
	return buf.Bytes()
}
func digestOf(data []byte) string {
	d := sha256.Sum256(data)
	return hex.EncodeToString(d[:])
}
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil { t.Fatal(err) }
	return s
}

func TestImmutableBundleAndIdempotence(t *testing.T) {
	cache := newTestStore(t)
	body := zipPayload(t, []string{"sub2api/deploy/install.sh","sub2api/deploy/docker-compose.yaml"},false)
	digest := digestOf(body)
	if ok, _, err := cache.Has(digest); err != nil || ok { t.Fatalf("empty cache: %v, %v",ok,err) }
	size, already, err := cache.Put(context.Background(),digest,bytes.NewReader(body))
	if err != nil || already || size!=int64(len(body)) { t.Fatalf("first PUT: %d %v %v",size,already,err) }
	size, already, err = cache.Put(context.Background(),digest,bytes.NewReader(body))
	if err != nil || !already || size!=int64(len(body)) { t.Fatalf("idempotent PUT: %d %v %v",size,already,err) }
	if ok,n,err:=cache.Has(digest);err!=nil||!ok||n!=size { t.Fatalf("HEAD: %v %d %v",ok,n,err) }
	info,err:=os.Stat(filepath.Join(cache.dir,digest+".zip"))
	if err!=nil||info.Mode().Perm()!=0600 { t.Fatalf("stored file not private: %v %v",info,err) }
}
func TestBundleTrustCannotBeClaimedByDigestOrArchive(t *testing.T) {
	cache:=newTestStore(t)
	valid:=zipPayload(t,[]string{"sub2api/deploy/install.sh"},false)
	if _,_,err:=cache.Put(context.Background(),strings.Repeat("0",64),bytes.NewReader(valid));!errors.Is(err,ErrDigestMismatch) {
		t.Fatalf("incorrect digest accepted: %v",err)
	}
	tests:=[]struct{name string;payload []byte}{
		{"path traversal",zipPayload(t,[]string{"../outside"},false)},
		{"absolute path",zipPayload(t,[]string{"/etc/shadow"},false)},
		{"symlink",zipPayload(t,[]string{"sub2api/link"},true)},
		{"duplicate names",zipPayload(t,[]string{"a/file","a/FILE"},false)},
		{"secrets",zipPayload(t,[]string{"sub2api/.secrets"},false)},
		{"not zip",[]byte("a shell script is not a bundle")},
	}
	for _,tt:=range tests {t.Run(tt.name,func(t *testing.T){
		_,_,err:=cache.Put(context.Background(),digestOf(tt.payload),bytes.NewReader(tt.payload))
		if !errors.Is(err,ErrInvalidArchive) { t.Fatalf("unsafe bundle accepted: %v",err) }
	})}
}
func TestCorruptOrSymlinkedCacheFailsClosed(t *testing.T){
	cache:=newTestStore(t)
	raw:=zipPayload(t,[]string{"sub2api/app.yaml"},false)
	digest:=digestOf(raw)
	file:=filepath.Join(cache.dir,digest+".zip")
	if err:=os.WriteFile(file,[]byte("corrupted"),0600);err!=nil{t.Fatal(err)}
	if _,_,err:=cache.Has(digest);!errors.Is(err,ErrCorruptStored){t.Fatalf("corrupted cache accepted: %v",err)}
	if _,_,err:=cache.Put(context.Background(),digest,bytes.NewReader(raw));!errors.Is(err,ErrCorruptStored){t.Fatalf("corrupt cache silently replaced: %v",err)}
	if err:=os.Remove(file);err!=nil{t.Fatal(err)}
	other:=filepath.Join(t.TempDir(),"outside")
	if err:=os.WriteFile(other,raw,0600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(other,file);err!=nil{t.Fatal(err)}
	if _,_,err:=cache.Has(digest);!errors.Is(err,ErrCorruptStored){t.Fatalf("symlink cache accepted: %v",err)}
}
func TestBundleSizeHardLimit(t *testing.T) {
	cache:=newTestStore(t)
	// LimitReader over an unbounded zero source avoids allocating 64 MiB RAM.
	zeros:=zeroReader{}
	_,_,err:=cache.Put(context.Background(),strings.Repeat("a",64),io.LimitReader(zeros,MaxBundleSize+1))
	if !errors.Is(err,ErrTooLarge){t.Fatalf("oversize bundle accepted: %v",err)}
}
type zeroReader struct{}
func (zeroReader) Read(p []byte)(int,error) {clear(p);return len(p),nil}
