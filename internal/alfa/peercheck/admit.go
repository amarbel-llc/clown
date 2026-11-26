package peercheck

import (
	"net"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

// AdmissionVectorsSHA256 is the sha256 (hex) of testdata/admission-vectors.json
// as vendored from spinclass (the same commit as bridgewire.VectorsSource).
// spinclass's gate compares it with the sha256 of its own file, as it does for
// bridgewire.VectorsSHA256; clown's test pins it to the vendored file.
const AdmissionVectorsSHA256 = "c7ed934b2fc6d2661d752d9279ba7137c278dd17680b4f41559a8dc5785c6599"

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

// AdmitPeer is the listener's admission decision for a connection: it reads the
// cgroup file now and admits the peer only on strict equality through
// CheckPeerExact (the peer taken by SO_PEERPIDFD, its cgroup read through the
// pidfd, liveness confirmed after). This is the ONE name a listener should call;
// AdmitCgroup takes a cgroup string the caller read by some other route and is
// for the vectors. nil admits; every error refuses.
func AdmitPeer(c net.Conn, cgroupFile string) error {
	want, err := ReadUnitCgroupFile(cgroupFile)
	if err != nil {
		return err
	}
	return CheckPeerExact(c, want)
}

// AdmitCgroup is the listener's admission decision for a peer whose cgroup is
// peerCgroup: it reads the file now and admits only on strict equality
// (CheckCgroupExact). It is the vectors' entry point (they supply the peer's
// cgroup as a string); a listener calls AdmitPeer, which cannot be handed a
// cgroup read by a pid that could be recycled. nil admits; every error refuses.
func AdmitCgroup(cgroupFile, peerCgroup string) error {
	want, err := ReadUnitCgroupFile(cgroupFile)
	if err != nil {
		return err
	}
	return CheckCgroupExact(peerCgroup, want)
}
