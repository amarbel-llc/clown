// Package bridgevectors records what clown vendored from spinclass's published
// bridge vectors (review r6, M1), as two constants and nothing else, so the one
// consumer that needs them (spinclass's gate, which locks clown) imports
// pkgs/bridgevectors and not the whole wire.
//
// The contract's one home is spinclass (internal/0/bridge/testdata); clown
// vendors vectors.json and its .sha256 byte for byte into
// internal/0/bridgewire/testdata. These constants say which bytes:
//
//   - clown's own test (internal/*/bridgewire) pins SHA256 to the vendored
//     file, so a re-vendor that forgets to update it is red in clown;
//   - spinclass's gate compares the sha256 of ITS vectors.json with SHA256 of
//     the clown it locks, so spinclass publishing new vectors with clown not
//     re-vendored is red THERE (the one gate that sees both sides).
//     `just verify-bridge-vectors DIR` is the same comparison by hand.
//
// The admission vectors have their own constant,
// peercheck.AdmissionVectorsSHA256, beside their vendored file.
package bridgevectors

//go:generate dagnabit export

const (
	// SHA256 is the sha256 (hex) of bridgewire's testdata/vectors.json as
	// vendored.
	SHA256 = "84b92ac44b9e47d3728f762225af3028c776d1ad4dad62d231c96bdee46a141e"
	// Source names where the vendored files came from. The commit was on
	// spinclass's branch slim-myrtle--bridge-cut, local and unpushed when it was
	// vendored; this text is updated to the landed commit when spinclass says so
	// (SHA256, not this text, is what is compared).
	Source = "spinclass internal/0/bridge/testdata/vectors.json at commit 5ac7c7ef"
)
