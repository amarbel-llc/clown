package peercheck

import (
	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

// ReadUnitCgroupFile reads the file clown writes with the unit's exact cgroup
// (FDR 0048: <runtime>/clown/launch/<key>/bridge.cgroup), the listener's side of
// the seam. The reader and the writer are one package's pair
// (bridgewire.ReadCgroupFile / WriteCgroupFile), so they cannot drift; this is
// the name spinclass's listener calls. It is read FRESH on every call (a
// listener calls it per connection), and fails closed (see ReadCgroupFile). The
// error is what a listener logs; the answer to the peer is always
// `ERR refused` (bridgewire.RefusedUnadmitted), never a silent close.
func ReadUnitCgroupFile(path string) (string, error) {
	return bridgewire.ReadCgroupFile(path)
}

// AdmitCgroup is the listener's admission decision for a peer whose cgroup is
// peerCgroup: it reads the file now and admits only on strict equality
// (CheckCgroupExact). nil admits; every error refuses.
func AdmitCgroup(cgroupFile, peerCgroup string) error {
	want, err := ReadUnitCgroupFile(cgroupFile)
	if err != nil {
		return err
	}
	return CheckCgroupExact(peerCgroup, want)
}
