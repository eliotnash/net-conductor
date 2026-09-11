package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/pion/webrtc/v4"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationPreservesKeysAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "key")
	authorized := filepath.Join(dir, "authorized_keys")
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(other)
	existing := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	os.WriteFile(authorized, []byte(existing), 0600)
	if err := InitKey(private, authorized); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(authorized)
	if !strings.Contains(string(first), strings.TrimSpace(existing)) {
		t.Fatal("existing key lost")
	}
	if err := InitKey(private, authorized); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(authorized)
	if string(first) != string(second) {
		t.Fatal("duplicate authorization")
	}
	if err := Authorize(authorized, []byte("not-a-key")); err == nil {
		t.Fatal("invalid key accepted")
	}
}
func TestWebRTCAndRelayShareTheSameRPC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	browser, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	dc, err := browser.CreateDataChannel("nc", nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan []byte, 4)
	dc.OnMessage(func(m webrtc.DataChannelMessage) { messages <- m.Data })
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	offer, err := browser.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(browser)
	if err = browser.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-ctx.Done():
		t.Fatal("offer timeout")
	}
	answers := make(chan webrtc.SessionDescription, 1)
	relays := make(chan []byte, 4)
	session, err := New(ctx, Config{}, Offer{Mode: "files", SDP: *browser.LocalDescription()}, func(kind string, v any) error {
		b, _ := json.Marshal(v)
		if kind == "answer" {
			var answer webrtc.SessionDescription
			json.Unmarshal(b, &answer)
			answers <- answer
		} else if kind == "relay" {
			relays <- b
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	select {
	case answer := <-answers:
		if err = browser.SetRemoteDescription(answer); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("answer timeout")
	}
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("P2P failed")
	}
	dc.SendText(`{"id":1,"op":"ping"}`)
	select {
	case data := <-messages:
		if !strings.Contains(string(data), `"ready"`) {
			t.Fatal(string(data))
		}
	case <-ctx.Done():
		t.Fatal("direct RPC timeout")
	}
	session.Relay([]byte(`{"id":2,"op":"ping"}`))
	select {
	case data := <-relays:
		if !strings.Contains(string(data), `"ready"`) {
			t.Fatal(string(data))
		}
	case <-ctx.Done():
		t.Fatal("relay RPC timeout")
	}
}
func TestModeAndDesktopRestrictions(t *testing.T) {
	if _, err := New(context.Background(), Config{}, Offer{Mode: "desktop"}, nil); err == nil {
		t.Fatal("headless desktop accepted")
	}
	if _, err := New(context.Background(), Config{}, Offer{Mode: "arbitrary"}, nil); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
