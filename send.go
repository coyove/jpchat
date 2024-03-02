package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

func handleSend(w http.ResponseWriter, r *http.Request) {
	uid := getuid(r)
	if uid == "" {
		w.Write([]byte("invalid cookie"))
		return
	}

	var name string
	var err string
	if r.Method == "POST" {
		msg := sanitizeMessage(r.FormValue("msg"))
		name = sanitizeChannelName(r.FormValue("channel"))

		tok, _ := strconv.ParseUint(r.FormValue("token"), 10, 64)
		if tok == 0 {
			err = "Invalid token"
			goto NO_SEND
		}
		if _, ok := world.sendDedup.Get(tok); ok {
			goto NO_SEND
		}
		world.sendDedup.Add(tok, struct{}{})

		world.Lock()
		ch, ok := world.channels[name]
		world.Unlock()

		if len(msg) > 0 && ok {
			e := ch.Append(Message{
				From: uid,
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
		name = sanitizeChannelName(r.URL.Path[7:])
	}

NO_SEND:
	if name == "" {
		w.WriteHeader(404)
		return
	}

	tok := time.Now().UnixNano()

	w.Header().Add("Content-Type", "text/html")
	httpTemplates.ExecuteTemplate(w, "send.html", map[string]any{
		"name":  name,
		"uid":   uid,
		"multi": r.URL.Query().Get("multi") != "",
		"err":   err,
		"token": tok,
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
