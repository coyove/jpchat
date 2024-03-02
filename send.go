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
	data := sha1.Sum(c.IP)
	binary.BigEndian.PutUint32(data[4:8], uint32(time.Now().Unix()))
	binary.BigEndian.PutUint32(data[8:12], tokenctr.Add(1))
	return hex.EncodeToString(enc.Seal(nonce, nonce, data[:12], nil))
}

func validateToken(c Ctx, tok string) bool {
	data, _ := hex.DecodeString(tok)

	enc, _ := cipher.NewGCM(aesToken)
	if len(data) < enc.NonceSize() {
		return false
	}
	nonce := data[:enc.NonceSize()]
	data = data[enc.NonceSize():]

	data, err := enc.Open(nil, nonce, data, nil)
	if err != nil {
		return false
	}
	if len(data) != 12 {
		return false
	}
	var v [12]byte
	copy(v[:], data)
	if _, ok := tokenStore.Get(v); ok {
		return false
	}
	tokenStore.Add(v, struct{}{})

	ipHash := sha1.Sum(c.IP)
	if !bytes.Equal(ipHash[:4], v[:4]) {
		logrus.Errorf("validate token: mismatch IPs")
		return false
	}
	if uint32(time.Now().Unix())-binary.BigEndian.Uint32(v[4:8]) > 86400 {
		logrus.Errorf("validate token: too old")
		return false
	}
	return true
}

func handleSend(c Ctx) {
	var name string
	var err string
	if c.Method == "POST" {
		msg := sanitizeMessage(c.FormValue("msg"))
		name = sanitizeChannelName(c.FormValue("channel"))

		tok := c.FormValue("token")
		if !validateToken(c, tok) {
			logrus.Infof("bad token: %s", tok)
			err = "Invalid session, please reload"
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
	})
}

// var cdMap sync.Map
//
// type limiter struct {
// 	ctr    int64
// 	freeAt int64
// }
//
// func CheckIP(r *http.Request) (ok bool, remains int64) {
// 	uid := types.IpUserId(r.RemoteIP)
// 	if v, ok := cdMap.Load(uid); ok {
// 		rl := v.(*limiter)
// 		if atomic.AddInt64(&rl.ctr, -1) > 0 {
// 			return true, 0
// 		}
// 		return false, rl.freeAt - clock.Unix()
// 	}
// 	return true, 0
// }
//
// func AddIP(r *http.Request) {
// 	uid := types.IpUserId(r.RemoteIP)
// 	if _, ok := cdMap.Load(uid); ok {
// 		return
// 	}
//
// 	var sec, ctr int64 = 10, 3
//
// 	cdMap.Store(uid, &limiter{
// 		ctr:    ctr,
// 		freeAt: clock.Unix() + sec,
// 	})
//
// 	time.AfterFunc(time.Second*time.Duration(sec), func() {
// 		cdMap.Delete(uid)
// 	})
// }
