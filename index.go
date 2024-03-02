package main

import (
	"crypto/rand"
	"encoding/base64"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
)

func handleIndex(c Ctx) {
	name := sanitizeChannelName(c.Query.Get("channel"))
	if name != "" {
		if c.Query.Get("randomize") != "" {
			var tmp [3]byte
			rand.Read(tmp[:])
			name += "--" + time.Now().Format("0102") + base64.RawURLEncoding.EncodeToString(tmp[:])
		}

		c.Uid = c.Query.Get("uid")
		c.SetUidCookie()
		c.Query.Del("channel")
		c.Query.Del("uid")
		c.Redirect(302, "/"+name+"?"+c.Query.Encode())
		return
	}

	name = sanitizeChannelName(c.URL.Path[1:])
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

		c.Template("index.html", map[string]any{
			"uid":       c.Uid,
			"widths":    [2][2]any{{c.Uid, 400}, {c.Uid, 800}},
			"totalCh":   totalCh,
			"activeCh":  activeCh,
			"totalUser": world.totalUsers.Load(),
		})
	} else {
		width, _ := strconv.Atoi(c.Query.Get("w"))
		width2 := width
		switch width {
		case 800:
			width, width2 = 800, 400
		default:
			width, width2 = 400, 800
		}

		c.Template("channel.html", map[string]any{
			"uid":    c.Uid,
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
