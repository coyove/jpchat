package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"sync/atomic"
	"time"

	"github.com/coyove/sdss/contrib/plru"
	"github.com/sirupsen/logrus"
)

func randBytes(len int) []byte {
	key := make([]byte, len)
	rand.Read(key)
	return key
}

var aesToken = func() cipher.Block {
	blk, _ := aes.NewCipher(randBytes(16))
	return blk
}()

var tokenStore = plru.New[[12]byte, struct{}](60000, func(v [12]byte) uint64 {
	h := sha1.Sum(v[:])
	return binary.BigEndian.Uint64(h[:8])
}, nil)

var tokenctr atomic.Uint32

func makeToken(c Ctx) string {
	enc, _ := cipher.NewGCM(aesToken)
	nonce := randBytes(12)
	data := sha1.Sum([]byte(c.UserAgent()))
	binary.BigEndian.PutUint32(data[4:8], uint32(time.Now().Unix()))
	binary.BigEndian.PutUint32(data[8:12], tokenctr.Add(1))
	return hex.EncodeToString(enc.Seal(nonce, nonce, data[:12], nil))
}

func validateToken(c Ctx, tok string) int {
	data, _ := hex.DecodeString(tok)

	enc, _ := cipher.NewGCM(aesToken)
	if len(data) < enc.NonceSize() {
		return -1
	}
	nonce := data[:enc.NonceSize()]
	data = data[enc.NonceSize():]

	data, err := enc.Open(nil, nonce, data, nil)
	if err != nil {
		return -1
	}
	if len(data) != 12 {
		return -1
	}
	var v [12]byte
	copy(v[:], data)
	if _, ok := tokenStore.Get(v); ok {
		return -2
	}
	tokenStore.Add(v, struct{}{})

	ipHash := sha1.Sum([]byte(c.UserAgent()))
	if !bytes.Equal(ipHash[:4], v[:4]) {
		logrus.Errorf("validate token: mismatch IPs")
		return -3
	}
	if uint32(time.Now().Unix())-binary.BigEndian.Uint32(v[4:8]) > 86400 {
		logrus.Errorf("validate token: too old")
		return -4
	}
	return 1
}

func handleSend(c Ctx) {
	var name string
	var err string
	if c.Method == "POST" {
		name = sanitizeChannelName(c.FormValue("channel"))
		msg := sanitizeMessage(c.FormValue("msg"))

		if !c.CheckIP() {
			err = "Cooling down"
			goto NO_SEND
		}
		c.AddIP()

		tok := c.FormValue("token")
		switch res := validateToken(c, tok); res {
		case 1:
		case -2, -3, -4:
			err = [...]string{"Token too old", "UA changed", "Cooling down"}[res+4]
			goto NO_SEND
		default:
			err = "Invalid session"
			goto NO_SEND
		}

		world.Lock()
		ch, ok := world.channels[name]
		world.Unlock()

		if len(msg) > 0 && ok {
			e := ch.Append(Message{
				From: c.Uid,
				Text: msg,
			})
			if e == nil {
				ch.Refresh(-1)
			} else {
				logrus.Errorf("append message: %v", e)
				err = "Internal error"
			}
		}
	} else {
		name = sanitizeChannelName(c.URL.Path[7:])
	}

NO_SEND:
	if name == "" {
		c.WriteHeader(404)
		return
	}

	c.Template("send.html", map[string]any{
		"name":  name,
		"uid":   c.Uid,
		"multi": c.Query.Get("multi") != "",
		"err":   err,
		"token": makeToken(c),
		"width": c.Query.Get("width"),
	})
}
