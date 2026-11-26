package bridgewire

// What clown vendored from spinclass, recorded where spinclass's gate can read
// it (review r6, M1). The contract's one home is spinclass
// (internal/0/bridge/testdata); clown vendors vectors.json and its .sha256 byte
// for byte. These constants say which bytes:
//
//   - clown's own test pins them to the vendored files, so a re-vendor that
//     forgets to update them is red here;
//   - spinclass's gate (which locks clown) compares the sha256 of ITS vectors.json
//     with VectorsSHA256 of the clown it locks, so spinclass publishing new
//     vectors, with clown not re-vendored, is red THERE (the one gate that sees
//     both sides). `just verify-bridge-vectors DIR` is the same comparison by
//     hand, against the files themselves.
const (
	// VectorsSHA256 is the sha256 (hex) of testdata/vectors.json as vendored.
	VectorsSHA256 = "84b92ac44b9e47d3728f762225af3028c776d1ad4dad62d231c96bdee46a141e"
	// VectorsSource names where the vendored files came from. The commit was on
	// spinclass's branch slim-myrtle--bridge-cut, local and unpushed when it was
	// vendored; spinclass says when it lands and this line is then updated to the
	// landed commit (the sha above, not this text, is what is compared).
	VectorsSource = "spinclass internal/0/bridge/testdata/vectors.json at commit 5ac7c7ef"
)
