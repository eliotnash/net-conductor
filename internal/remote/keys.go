package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"golang.org/x/crypto/ssh"
	"os"
	"strings"
)

func Authorize(path string, pub []byte) error {
	key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		return err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range strings.Split(string(old), "\n") {
		k, _, _, _, e := ssh.ParseAuthorizedKey([]byte(entry))
		if e == nil && string(k.Marshal()) == string(key.Marshal()) {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + line + " net-conductor\n")
	return err
}
func InitKey(path, authorized string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, private, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		block, e := ssh.MarshalPrivateKey(private, "net-conductor-local")
		if e != nil {
			return e
		}
		b = pem.EncodeToMemory(block)
		if e = os.WriteFile(path, b, 0600); e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return err
	}
	return Authorize(authorized, ssh.MarshalAuthorizedKey(signer.PublicKey()))
}
