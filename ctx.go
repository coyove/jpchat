package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
	"unsafe"

	"github.com/sirupsen/logrus"
)

//go:embed static/*
var httpStaticPages embed.FS

//go:embed static/assets/*
var httpStaticAssets embed.FS

var uuid = strconv.Itoa(int(time.Now().Unix()))

var httpTemplates = template.Must(template.New("ts").Funcs(template.FuncMap{
	"ServeUUID": func() string {
		return uuid
	},
	"randomChannelSuffix": func(n int) [][2]any {
		res := make([][2]any, n)
		for i := range res {
			tmp := make([]byte, 3)
			rand.Read(tmp)
			if i == n-1 {
				res[i] = [2]any{100, hex.EncodeToString(tmp)}
			} else {
				res[i] = [2]any{i * 100 / n, hex.EncodeToString(tmp)}
			}
		}
		return res
	},
}).ParseFS(httpStaticPages, "static/*.*"))

type Ctx struct {
	*http.Request
	http.ResponseWriter
	IP    net.IP
	Query url.Values
	Uid   string
}

func handle(p string, f func(Ctx)) {
	http.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		for ua := r.UserAgent(); ; {
			if idx := strings.Index(ua, "Chrome/"); idx > 0 {
				major, _, _ := strings.Cut(ua[idx+7:], ".")
				if i, _ := strconv.Atoi(major); i >= 32 {
					break
				}
			}
			if idx := strings.Index(ua, "Firefox/"); idx > 0 {
				major, _, _ := strings.Cut(ua[idx+8:], ".")
				if i, _ := strconv.Atoi(major); i >= 65 {
					break
				}
			}
			if idx := strings.Index(ua, "Version/"); idx > 0 && strings.Contains(ua, "Safari/") {
				major, _, _ := strings.Cut(ua[idx+8:], ".")
				if i, _ := strconv.Atoi(major); i >= 14 {
					break
				}
			}

			fmt.Fprintf(w, "Browser is too old: %s", ua)
			return
		}

		addr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
		if err != nil {
			fmt.Fprintf(w, "Invalid ip: %s", r.RemoteAddr)
			return
		}

		c := Ctx{
			ResponseWriter: w,
			Request:        r,
			IP:             addr.IP.To16(),
			Query:          r.URL.Query(),
		}

		ck, _ := r.Cookie("uid2")
		if ck != nil {
			v, err := base64.URLEncoding.DecodeString(ck.Value)
			if err != nil {
				logrus.Errorf("base64 %q: %v", ck.Value, err)
			} else {
				c.Uid = string(v)
			}
		}

		c.SetUidCookie()
		c.ResponseWriter.Header().Add("Content-Security-Policy", "script-src none")

		f(c)
	})
}

func (c Ctx) SetUidCookie() {
	if c.Uid == "" {
		c.Uid = ipuid(c.IP, c.UserAgent())
	} else {
		c.Uid = sanitizeStrict(c.Uid, 20)
	}
	c.ResponseWriter.Header().Del("Set-Cookie")
	http.SetCookie(c.ResponseWriter, &http.Cookie{
		Name:     "uid2",
		Value:    base64.URLEncoding.EncodeToString([]byte(c.Uid)),
		Expires:  time.Now().AddDate(1, 0, 0),
		HttpOnly: true,
		Path:     "/",
	})
}

func (c Ctx) Redirect(code int, url string) {
	http.Redirect(c.ResponseWriter, c.Request, url, code)
}

func (c Ctx) Printf(p string, args ...any) {
	fmt.Fprintf(c.ResponseWriter, p, args...)
}

func (c Ctx) Template(name string, arg any) {
	httpTemplates.ExecuteTemplate(c.ResponseWriter, name, arg)
}

func (c Ctx) Write(p []byte) (int, error) {
	return c.ResponseWriter.Write(p)
}

func ipuid(ip net.IP, ua string) string {
	h := hmac.New(sha1.New, []byte(*onlineKey))
	h.Write(ip.To16())
	h.Write([]byte(ua))

	// 35bits
	v := binary.BigEndian.Uint64(h.Sum(nil)) & 0x7FFFFFFFF

	const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	res := make([]byte, 4, 8)
	for i := 0; i < 4; i++ {
		res = append(res, base58[v%58])
		v = v / 58
	}
	copy(res[:4], wordDict[v%uint64(len(wordDict))])
	return *(*string)(unsafe.Pointer(&res))
}

func (c Ctx) isAdmin() bool {
	if ck, _ := c.Cookie("admin"); ck == nil || ck.Value != onlineKeyhash {
		return false
	}
	return true
}

func hmacHex(v string) string {
	h := hmac.New(sha1.New, []byte(*onlineKey))
	h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))
}

var cdMap sync.Map

func (c Ctx) CheckIP() (ok bool) {
	var ip [16]byte
	copy(ip[:], c.IP)
	_, exist := cdMap.Load(ip)
	return !exist
}

func (c Ctx) AddIP() {
	var ip [16]byte
	copy(ip[:], c.IP)
	old, _ := cdMap.Swap(ip, 1)
	if old != 1 {
		time.AfterFunc(time.Second*1, func() {
			cdMap.Delete(ip)
		})
	}
}
