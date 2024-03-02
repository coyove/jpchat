package main

import (
	"crypto/rand"
	"encoding/base64"
	"hash/crc32"
	"net"
	"net/http"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/sirupsen/logrus"
)

func ipuid(r *http.Request) string {
	t, _ := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if t == nil {
		return ""
	}

	h1 := crc32.ChecksumIEEE(t.IP.To16())
	h2 := crc32.ChecksumIEEE([]byte(r.UserAgent()))

	// 35bits = 25 + 10
	v := uint64(h1&0x1FFFFFF)<<10 | uint64(h2&0x3FF)

	const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	res := make([]byte, 4, 8)
	for i := 0; i < 4; i++ {
		res = append(res, base58[v%58])
		v = v / 58
	}
	copy(res[:4], wordDict[v%uint64(len(wordDict))])
	return *(*string)(unsafe.Pointer(&res))
}

func getuid(r *http.Request) string {
	ck, _ := r.Cookie("uid")
	if ck == nil {
		return ""
	}
	v, err := base64.URLEncoding.DecodeString(ck.Value)
	if err != nil {
		logrus.Errorf("base64 %q: %v", ck.Value, err)
		return ""
	}
	return string(v)
}

func setuid(w http.ResponseWriter, r *http.Request, uid string) string {
	if uid == "" {
		uid = ipuid(r)
	} else {
		uid = sanitizeStrict(uid, 20)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "uid",
		Value:    base64.URLEncoding.EncodeToString([]byte(uid)),
		Expires:  time.Now().AddDate(1, 0, 0),
		HttpOnly: true,
	})

	return uid
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	uq := r.URL.Query()
	name := sanitizeChannelName(uq.Get("channel"))
	if name != "" {
		uid := uq.Get("uid")
		if uq.Get("randomize") != "" {
			var tmp [3]byte
			rand.Read(tmp[:])
			name += "--" + time.Now().Format("0102") + base64.RawURLEncoding.EncodeToString(tmp[:])
		}

		uq.Del("channel")
		uq.Del("uid")

		setuid(w, r, uid)
		http.Redirect(w, r, "/"+name+"?"+uq.Encode(), 302)
		return
	}

	name = sanitizeChannelName(r.URL.Path[1:])
	if name == "" {
		world.Lock()
		activeCh := len(world.channels)
		world.Unlock()

		var totalCh int
		if tx, _ := world.store.Begin(false); tx != nil {
			defer tx.Rollback()
			if bk := tx.Bucket([]byte("channel")); bk != nil {
				totalCh = int(bk.Sequence())
			}
		}

		uid := getuid(r)
		uid = setuid(w, r, uid)
		httpTemplates.ExecuteTemplate(w, "index.html", map[string]any{
			"uid":       uid,
			"widths":    [2][2]any{{uid, 400}, {uid, 800}},
			"totalCh":   totalCh,
			"activeCh":  activeCh,
			"totalUser": world.totalUsers.Load(),
		})
	} else {
		uid := getuid(r)
		if uid == "" {
			setuid(w, r, "")
			http.Redirect(w, r, r.URL.String(), 302)
			return
		}

		width, _ := strconv.Atoi(uq.Get("w"))
		width2 := width
		switch width {
		case 800:
			width, width2 = 800, 400
		default:
			width, width2 = 400, 800
		}

		httpTemplates.ExecuteTemplate(w, "channel.html", map[string]any{
			"uid":    uid,
			"name":   name,
			"width":  width,
			"width2": width2,
		})
	}
}

func sanitizeChannelName(in string) string {
	return sanitizeStrict(in, 50)
}

func sanitizeStrict(in string, max int) string {
	var tmp []byte
	for _, r := range in {
		if r > 255 && !unicode.IsSpace(r) {
			tmp = utf8.AppendRune(tmp, r)
		} else {
			switch r {
			case '-', '.', '_',
				'0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
				'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z',
				'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O', 'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z':
				tmp = append(tmp, byte(r))
			}
		}
		if len(tmp) >= max {
			break
		}
	}
	return string(tmp)
}

func sanitizeMessage(in string) string {
	var tmp []byte
	var lines int
	for _, r := range in {
		if r == '\r' {
			continue
		}
		if r == '\n' {
			lines++
		}
		tmp = utf8.AppendRune(tmp, r)
		if len(tmp) >= 1024 || lines >= 10 {
			break
		}
	}
	return string(tmp)
}
