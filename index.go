package main

import (
	"crypto/rand"
	"encoding/base64"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func handleIndex(c Ctx) {
	name := sanitizeChannelName(c.Query.Get("channel"))
	if name != "" {
		if c.Query.Get("randomize") != "" {
			var tmp [6]byte
			rand.Read(tmp[:])
			name += "--" + time.Now().Format("0102") + base64.RawURLEncoding.EncodeToString(tmp[:])
		}

		var pwd string
		c.Uid, pwd, _ = strings.Cut(c.Query.Get("uid"), "!")
		if uid := strings.ToLower(c.Uid); strings.Contains(uid, "root") || strings.Contains(uid, "admin") {
			if !c.isAdmin() {
				c.Uid = ""
			}
		}
		c.SetUidCookie()
		if pwd := hmacHex(pwd); pwd == onlineKeyhash {
			http.SetCookie(c.ResponseWriter, &http.Cookie{
				Name:     "admin",
				Value:    pwd,
				Expires:  time.Now().AddDate(0, 0, 3),
				HttpOnly: true,
				Path:     "/",
			})
		}
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

func handleLink(c Ctx) {
	name := sanitizeChannelName(c.URL.Path[7:])
	idx, _ := strconv.ParseInt(c.Query.Get("link"), 16, 64)

	var links []string
	var link string

	if ch, ok := findChannel(name); ok {
		ch.mu.Lock()
		links = ch.links
		ch.mu.Unlock()
		if 0 <= idx && int(idx) < len(ch.links) {
			link = links[idx]
		}
	}

	if link == "" {
		c.ResponseWriter.Header().Add("Content-Type", "text/html")
		if len(links) > 0 {
			c.Printf(`
            <p>
            Link %x doesn't exist in the current channel.<br>
            Here are currently available links on screen:<br>
            `, idx)
			for i := 0; i < 16 && i < len(links); i++ {
				u := html.EscapeString(links[i])
				c.Printf(`%x: <a href="%s">%s</a><br>`, i, u, u)
			}
		} else {
			c.Printf(`<p>This channel doesn't have any links. 
                New link appeared on chat screen will be assigned a tag, so you know which to open.
                </p>`)
		}
	} else {
		c.Redirect(302, link)
	}
}

func handlePing(c Ctx) {
	name := sanitizeChannelName(c.URL.Path[7:])
	if ch, ok := findChannel(name); ok {
		ch.mu.Lock()
		if arr := ch.onlines[c.Uid]; len(arr) > 0 {
			u := arr[len(arr)-1]
			u.timeout.Reset(pingTimeout)
		}
		ch.mu.Unlock()
	}
	c.WriteHeader(200)
	c.Write([]byte(`<html><meta http-equiv="refresh" content="10">`))
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
