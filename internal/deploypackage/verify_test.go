package deploypackage

import (
    "archive/tar"
    "bytes"
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "errors"
    "io"
    "os"
    "path/filepath"
    "testing"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"

type testEntry struct {
    name string
    data []byte
    mode int64
    kind byte
}
func testData() ([]testEntry,Envelope) {
    entries:=[]testEntry{
        {"manifest/app.yaml",[]byte("schema_version: 1\nid: portainer\n"),0644,tar.TypeReg},
        {"portainer/deploy/docker-compose.yaml",[]byte("name: portainer\nservices: {}\n"),0644,tar.TypeReg},
        {"portainer/deploy/install.sh",[]byte("#!/bin/sh\necho install\n"),0755,tar.TypeReg},
        {"portainer/deploy/update.sh",[]byte("#!/bin/sh\necho update\n"),0755,tar.TypeReg},
    }
    manifestSum:=sha256.Sum256(entries[0].data)
    env:=Envelope{FormatVersion:1,AppID:"portainer",GitRevision:testRevision,
        ManifestSHA256:hex.EncodeToString(manifestSum[:]),ManifestPath:"manifest/app.yaml",
        AllowedActions:map[string]string{"install":"script","update":"script","uninstall":"compose_down"},
    }
    for _,entry:=range entries[1:] {
        hash:=sha256.Sum256(entry.data)
        mode:="0644"
        if entry.mode==0755{mode="0755"}
        env.Files=append(env.Files,File{
            Source:entry.name,Destination:entry.name,SHA256:hex.EncodeToString(hash[:]),
            Size:int64(len(entry.data)),Mode:mode,
        })
    }
    return entries,env
}
func makeTar(t *testing.T, entries []testEntry, env Envelope) ([]byte,string) {
    t.Helper()
    raw,err:=json.Marshal(env)
    if err!=nil {t.Fatal(err)}
    all:=append([]testEntry{{"bundle.json",raw,0644,tar.TypeReg}},entries...)
    var buffer bytes.Buffer
    writer:=tar.NewWriter(&buffer)
    for _,entry:=range all {
        kind:=entry.kind
        if kind==0 {kind=tar.TypeReg}
        hdr:=&tar.Header{Name:entry.name,Mode:entry.mode,Size:int64(len(entry.data)),Typeflag:kind,Format:tar.FormatUSTAR}
        if err:=writer.WriteHeader(hdr);err!=nil{t.Fatal(err)}
        if _,err:=writer.Write(entry.data);err!=nil{t.Fatal(err)}
    }
    if err:=writer.Close();err!=nil{t.Fatal(err)}
    digest:=sha256.Sum256(buffer.Bytes())
    return buffer.Bytes(),hex.EncodeToString(digest[:])
}
func TestBundleValidatesAndRejectsForeignContent(t *testing.T) {
    entries,envelope:=testData()
    good,digest:=makeTar(t,entries,envelope)
    verified,err:=Verify(bytes.NewReader(good),digest)
    if err!=nil{t.Fatal(err)}
    if verified.Envelope.AppID!="portainer" || verified.FileCount!=3{t.Fatal(verified)}
    if _,err:=Verify(bytes.NewReader(good),"f"+digest[1:]);!errors.Is(err,ErrInvalid){
        t.Fatalf("wrong SHA accepted: %v",err)
    }
    if _,err:=Verify(bytes.NewReader(good),"not-sha");!errors.Is(err,ErrInvalid){t.Fatal(err)}
    mutated:=append([]testEntry(nil),entries...)
    mutated[2].data=[]byte("different script")
    bad,checksum:=makeTar(t,mutated,envelope)
    if _,err:=Verify(bytes.NewReader(bad),checksum);!errors.Is(err,ErrInvalid){t.Fatalf("tampered script accepted: %v",err)}
    envelope.AllowedActions["uninstall"]="run_shell"
    bad,checksum=makeTar(t,entries,envelope)
    if _,err:=Verify(bytes.NewReader(bad),checksum);!errors.Is(err,ErrInvalid){t.Fatalf("arbitrary action accepted: %v",err)}
}
func TestPathTraversalSecretsLinksDuplicateAndSizeBounds(t *testing.T){
    orig,env:=testData()
    cases:=[]struct{name string;mutate func([]testEntry,*Envelope) []testEntry}{
        {"traversal",func(e []testEntry,_ *Envelope) []testEntry {e[2].name="../escape.sh";return e}},
        {"secret",func(e []testEntry,_ *Envelope) []testEntry {e=append(e,testEntry{"portainer/.secrets",[]byte("s"),0644,tar.TypeReg});return e}},
        {"link",func(e []testEntry,_ *Envelope) []testEntry {e[2].kind=tar.TypeSymlink;return e}},
        {"duplicate",func(e []testEntry,_ *Envelope) []testEntry{return append(e,e[2])}},
        {"wrong_mode",func(e []testEntry,_ *Envelope) []testEntry{e[2].mode=0644;return e}},
        {"unknown_file",func(e []testEntry,_ *Envelope) []testEntry{return append(e,testEntry{"portainer/bonus.txt",[]byte("s"),0644,tar.TypeReg})}},
        {"duplicate_dest_manifest",func(e []testEntry,b *Envelope) []testEntry{
            b.Files[0].Destination=b.Files[1].Destination;return e}},
    }
    for _,tc:=range cases{t.Run(tc.name,func(t *testing.T){
        entries:=append([]testEntry(nil),orig...)
        copyEnv:=env
        copyEnv.Files=append([]File(nil),env.Files...)
        entries=tc.mutate(entries,&copyEnv)
        archive,digest:=makeTar(t,entries,copyEnv)
        if _,err:=Verify(bytes.NewReader(archive),digest);err==nil{t.Fatal("malformed package accepted")}
    })}
    if _,err:=Verify(io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("x"),MaxArchiveBytes+1)),MaxArchiveBytes+1),
        hex.EncodeToString(make([]byte,32)));!errors.Is(err,ErrTooLarge){t.Fatalf("oversize accepted: %v",err)}
}
func TestImmutableStoreRetentionAndIdempotency(t *testing.T){
    state:=t.TempDir()
    store,err:=New(state);if err!=nil{t.Fatal(err)}
    entries,env:=testData()
    archive,digest:=makeTar(t,entries,env)
    receipt,err:=store.Put(context.Background(),bytes.NewReader(archive),digest)
    if err!=nil{t.Fatal(err)}
    if receipt.Reused || receipt.ReadyForAgent || receipt.InstallEnabled{t.Fatal(receipt)}
    stored,err:=store.Lookup("portainer",testRevision)
    if err!=nil || stored.PackageSHA256!=digest{t.Fatalf("stored receipt: %+v %v",stored,err)}
    duplicate,err:=store.Put(context.Background(),bytes.NewReader(archive),digest)
    if err!=nil || !duplicate.Reused{t.Fatalf("idempotency failed: %+v %v",duplicate,err)}
    entries[2].data=[]byte("#!/bin/sh\necho changed\n")
    // A different archive claiming the same fixed Git revision is refused,
    // even if the adversary recomputes all file digests.
    h:=sha256.Sum256(entries[2].data)
    env.Files[1].SHA256=hex.EncodeToString(h[:])
    env.Files[1].Size=int64(len(entries[2].data))
    different,otherSHA:=makeTar(t,entries,env)
    if _,err:=store.Put(context.Background(),bytes.NewReader(different),otherSHA);!errors.Is(err,ErrConflict){
        t.Fatalf("different bytes overwrote same revision: %v",err)
    }
    check,err:=store.Lookup("portainer",testRevision)
    if err!=nil || check.PackageSHA256!=digest{t.Fatal("original package replaced")}
    if err:=os.Remove(filepath.Join(state,"deploy-packages",digest+".tar"));err!=nil{t.Fatal(err)}
    if _,err:=store.Lookup("portainer",testRevision);!errors.Is(err,ErrConflict){
        t.Fatalf("missing retained payload ignored: %v",err)
    }
}
