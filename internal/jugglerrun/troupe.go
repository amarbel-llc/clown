package jugglerrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Credential is `troupe mint`'s output: the account JID and the path of the
// file holding its password (the credential by reference, never the secret).
type Credential struct {
	JID          string `json:"jid"`
	PasswordFile string `json:"password_file"`
}

// Localpart is the JID's localpart (TROUPE_XMPP_USER).
func (c Credential) Localpart() string {
	local, _, _ := strings.Cut(c.JID, "@")
	return local
}

// Domain is the JID's domain (TROUPE_XMPP_DOMAIN, the c2s vhost).
func (c Credential) Domain() string {
	_, domain, _ := strings.Cut(c.JID, "@")
	domain, _, _ = strings.Cut(domain, "/")
	return domain
}

// Identity is who a troupe call acts as. The zero Identity is the ambient one
// (the caller's own CLOWN_SESSION_ID and TROUPE_XMPP_* environment).
type Identity struct {
	SessionKey   string
	User         string
	PasswordFile string
	Domain       string
}

// IdentityFor is the identity of a minted principal.
func IdentityFor(sessionKey string, c Credential) Identity {
	return Identity{SessionKey: sessionKey, User: c.Localpart(), PasswordFile: c.PasswordFile, Domain: c.Domain()}
}

func (id Identity) isAmbient() bool { return id == Identity{} }

func (id Identity) environ() []string {
	if id.isAmbient() {
		return nil
	}
	return EnvWith(os.Environ(), map[string]string{
		SessionIDEnv:                id.SessionKey,
		"TROUPE_XMPP_USER":          id.User,
		"TROUPE_XMPP_PASSWORD_FILE": id.PasswordFile,
		"TROUPE_XMPP_DOMAIN":        id.Domain,
	})
}

// Wake is one standalone waking message to a principal's channel.
type Wake struct {
	Target    string
	From      string
	Source    string
	Message   string
	ResultRef string
}

// Troupe is the messaging surface juggler consumes.
type Troupe interface {
	// Mint creates the account for sessionKey and writes its password to
	// passwordFile: one file per identity, so a mint never overwrites the
	// ambient identity's credential (TROUPE_XMPP_PASSWORD_FILE).
	Mint(ctx context.Context, sessionKey, passwordFile string) (Credential, error)
	RevokeMint(ctx context.Context, sessionKey, passwordFile string) error
	// PostStanza posts one stanza into a MUC room as `as` and returns the
	// posted message id.
	PostStanza(ctx context.Context, as Identity, room, source string, stanza []byte) (string, error)
	SendWake(ctx context.Context, w Wake) error
}

// ExecTroupe shells the troupe binary (troupe(1)). Argv shapes:
//
//	mint --session-key K --password-file P   -> {"jid","password_file"}
//	mint-revoke --session-key K --password-file P
//	muc send --room R --subject <stanza JSON> --body "" --source S [--from K]
//	message --target T --source S --message M [--from F] [--result-ref R]
//
// The muc send shape (shared by run, spawn and decide) is clown-hook-tee's:
// the stanza JSON has no blank line, so all of it rides --subject with an
// empty --body and the wire body is exactly the stanza.
type ExecTroupe struct {
	Bin string
}

func (t ExecTroupe) run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return runCommand(ctx, Command{Argv: append([]string{t.Bin}, args...), Env: env})
}

func (t ExecTroupe) Mint(ctx context.Context, sessionKey, passwordFile string) (Credential, error) {
	out, err := t.run(ctx, nil, "mint", "--session-key", sessionKey, "--password-file", passwordFile)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err := json.Unmarshal(bytes.TrimSpace(out), &c); err != nil {
		return Credential{}, fmt.Errorf("troupe mint: parsing output: %w", err)
	}
	if c.JID == "" || c.PasswordFile == "" {
		return Credential{}, fmt.Errorf("troupe mint: output lacks jid or password_file: %s", bytes.TrimSpace(out))
	}
	return c, nil
}

func (t ExecTroupe) RevokeMint(ctx context.Context, sessionKey, passwordFile string) error {
	_, err := t.run(ctx, nil, "mint-revoke", "--session-key", sessionKey, "--password-file", passwordFile)
	return err
}

// MUCSendArgv is the `troupe muc send` argv for one stanza.
func MUCSendArgv(bin, room, source, from string, stanza []byte) []string {
	argv := []string{bin, "muc", "send", "--room", room, "--subject", string(stanza), "--body", "", "--source", source}
	if from != "" {
		argv = append(argv, "--from", from)
	}
	return argv
}

func (t ExecTroupe) PostStanza(ctx context.Context, as Identity, room, source string, stanza []byte) (string, error) {
	argv := MUCSendArgv(t.Bin, room, source, as.SessionKey, stanza)
	out, err := t.run(ctx, as.environ(), argv[1:]...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (t ExecTroupe) SendWake(ctx context.Context, w Wake) error {
	args := []string{"message", "--target", w.Target, "--source", w.Source, "--message", w.Message}
	if w.From != "" {
		args = append(args, "--from", w.From)
	}
	if w.ResultRef != "" {
		args = append(args, "--result-ref", w.ResultRef)
	}
	_, err := t.run(ctx, nil, args...)
	return err
}

// ErrRoomProvisioningUnavailable: troupe has no verb that creates a MUC room,
// configures it (non-anonymous, unlocked) and makes the operator its owner.
// MUC provisioning is troupe's and circus's lane (FDR 0019 §7); until a verb
// exists, a run must be given an existing, already-configured room.
var ErrRoomProvisioningUnavailable = errors.New("creating a run MUC with the operator as owner is not available: troupe's lane (no troupe verb exists yet); pass an existing room with --room")

// RoomProvisioner creates and tears down a run's MUC.
type RoomProvisioner interface {
	CreateRoom(ctx context.Context, room string) error
	DestroyRoom(ctx context.Context, room string) error
}

// UnavailableRoomProvisioner is the only production RoomProvisioner: every
// call fails with ErrRoomProvisioningUnavailable.
type UnavailableRoomProvisioner struct{}

func (UnavailableRoomProvisioner) CreateRoom(context.Context, string) error {
	return ErrRoomProvisioningUnavailable
}

func (UnavailableRoomProvisioner) DestroyRoom(context.Context, string) error {
	return ErrRoomProvisioningUnavailable
}
